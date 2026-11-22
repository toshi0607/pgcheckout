package branch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/toshi0607/pgtwig/internal/config"
	"github.com/toshi0607/pgtwig/internal/marker"
	"github.com/toshi0607/pgtwig/internal/naming"
	"github.com/toshi0607/pgtwig/internal/pg"
)

type BaseInfo struct {
	Name             string `json:"name"`
	Gen              int    `json:"gen"`
	Open             bool   `json:"open"`
	AllowConnections bool   `json:"allow_connections"`
}

type BaseResult struct {
	BaseInfo
	Changed   bool      `json:"changed"`
	Strategy  string    `json:"strategy,omitempty"`
	ElapsedMS *int64    `json:"elapsed_ms,omitempty"`
	Warnings  []string  `json:"warnings,omitempty"`
	Dropped   []string  `json:"dropped,omitempty"`
	Skipped   []Skipped `json:"skipped,omitempty"`
}

func baseInfo(db pg.Database) BaseInfo {
	return BaseInfo{Name: db.Name, Gen: db.Marker.Gen, Open: db.Marker.IsOpen(), AllowConnections: db.AllowConnections}
}

func (svc Service) BaseCreate(ctx context.Context, name, from string, terminate bool) (BaseResult, error) {
	if err := naming.ValidateBase(name); err != nil {
		return BaseResult{}, err
	}
	if from == "" || len(from) > 63 || strings.IndexByte(from, 0) >= 0 {
		return BaseResult{}, config.NewError(config.ConfigInvalid, "invalid source database name", "")
	}
	svc.Config.Base = name
	return run(ctx, svc, func(s *session) (result BaseResult, err error) {
		if name == s.Maintenance || from == s.Maintenance {
			return result, maintenanceError()
		}
		err = s.claim(ctx, &result.Warnings, func() error {
			_, found, err := pg.Lookup(ctx, s.Conn, name)
			if err != nil {
				return err
			}
			if found {
				return config.NewError(config.DatabaseExists, "destination base database already exists", "")
			}
			source, found, err := pg.Lookup(ctx, s.Conn, from)
			if err != nil {
				return err
			}
			if !found {
				return config.NewError(config.BaseNotFound, "source database does not exist", "")
			}
			strategy, err := pg.SelectStrategy(s.VersionNum, s.Config.Strategy)
			if err != nil {
				return err
			}
			if terminate {
				if err := pg.Terminate(ctx, s.Conn, from); err != nil {
					return databaseError(err, "")
				}
			}
			copied, err := s.copyFromBase(ctx, source, 1, strategy)
			if err != nil {
				return databaseError(err, "Use --terminate to disconnect source database sessions.")
			}
			m := marker.New(marker.Base, name, 1)
			m.Open = new(true)
			if err := ctx.Err(); err != nil {
				return s.discardCopy(ctx, copied.database, err)
			}
			transition, cancel := cleanupContext(ctx)
			err = s.renameManaged(transition, copied.database, name, m)
			cancel()
			if err != nil {
				return s.discardCopy(ctx, copied.database, databaseError(err, ""))
			}
			base := pg.Database{Name: name, OID: copied.database.OID, Marker: m}
			if err := s.finishBaseClose(ctx, base, terminate, false); err != nil {
				return err
			}
			result = BaseResult{
				BaseInfo: BaseInfo{Name: name, Gen: 1}, Changed: true,
				Strategy: copied.database.Marker.Strategy, ElapsedMS: &copied.elapsed, Warnings: copied.warnings,
			}
			return nil
		})
		return result, err
	})
}

func (svc Service) BaseAdopt(ctx context.Context, name string, terminate bool) (BaseResult, error) {
	if err := naming.ValidateBase(name); err != nil {
		return BaseResult{}, err
	}
	svc.Config.Base = name
	return run(ctx, svc, func(s *session) (result BaseResult, err error) {
		if s.Base == s.Maintenance {
			return result, maintenanceError()
		}
		err = s.claim(ctx, &result.Warnings, func() error {
			db, found, err := pg.Lookup(ctx, s.Conn, name)
			if err != nil {
				return err
			}
			if !found {
				return config.NewError(config.BaseNotFound, "base database does not exist", "")
			}
			m := db.Marker
			if m.PGTwig == 1 {
				if m.Kind != marker.Base || !m.ValidFor(name) {
					return unmanaged()
				}
				if baseIsOpen(db) {
					return baseOpenError()
				}
				result.BaseInfo = baseInfo(db)
				return nil
			}
			m = marker.New(marker.Base, name, 1)
			if err := s.closeBase(ctx, db, m, terminate, true); err != nil {
				return err
			}
			result.BaseInfo = BaseInfo{Name: name, Gen: m.Gen}
			result.Changed = true
			return nil
		})
		return result, err
	})
}

func (svc Service) BaseOpen(ctx context.Context) (BaseResult, error) {
	return svc.changeBase(ctx, true, false)
}

func (svc Service) BaseClose(ctx context.Context, terminate bool) (BaseResult, error) {
	return svc.changeBase(ctx, false, terminate)
}

func (svc Service) changeBase(ctx context.Context, open, terminate bool) (BaseResult, error) {
	return run(ctx, svc, func(s *session) (result BaseResult, err error) {
		if s.Base != "" && s.Base == s.Maintenance {
			return result, maintenanceError()
		}
		err = s.claim(ctx, &result.Warnings, func() error {
			db, err := s.lookupBase(ctx)
			if err != nil {
				return err
			}
			result.BaseInfo = baseInfo(db)
			if db.Marker.IsOpen() == open && db.AllowConnections == open {
				return nil
			}
			m := db.Marker
			if open {
				m.Open = new(true)
				if err := s.writeBase(ctx, db, m, new(true), false); err != nil {
					return err
				}
			} else {
				if err := s.closeBase(ctx, db, m, terminate, false); err != nil {
					return err
				}
				m.Gen++
			}
			result.BaseInfo = BaseInfo{Name: db.Name, Gen: m.Gen, Open: open, AllowConnections: open}
			result.Changed = true
			if !open {
				swept, err := s.sweep(ctx, m.Gen, false, false)
				result.Dropped, result.Skipped = swept.Dropped, swept.Skipped
				return err
			}
			return nil
		})
		return result, err
	})
}

func (s *session) closeBase(ctx context.Context, db pg.Database, m marker.Marker, terminate, adopt bool) error {
	var terminateErr error
	if terminate {
		terminateErr = pg.Terminate(ctx, s.Conn, db.Name)
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, terminateErr)
	}
	// Keep copies blocked until the session check and any generation bump are complete.
	m.Open = new(true)
	transition, cancel := cleanupContext(ctx)
	err := s.writeBase(transition, db, m, new(false), adopt)
	cancel()
	if err != nil {
		return errors.Join(err, terminateErr)
	}
	db.Marker = m
	db.AllowConnections = false
	if err := s.finishBaseClose(ctx, db, terminate, !adopt); err != nil {
		return errors.Join(err, terminateErr)
	}
	return nil
}

func (s *session) finishBaseClose(ctx context.Context, db pg.Database, terminate, bump bool) error {
	err := s.checkClosedSessions(ctx, db.Name, terminate)
	if err == nil {
		m := db.Marker
		m.Open = new(false)
		if bump {
			m.Gen++
		}
		cleanup, cancel := cleanupContext(ctx)
		defer cancel()
		err = s.writeBase(cleanup, db, m, nil, false)
	}
	if err == nil {
		return nil
	}
	cleanup, cancel := cleanupContext(ctx)
	defer cancel()
	if restoreErr := s.writeBase(cleanup, db, db.Marker, new(true), false); restoreErr != nil {
		return &config.Error{
			Code:    config.ErrorCode(databaseError(err, "")),
			Message: fmt.Sprintf("%s; could not reopen base: %s", err, restoreErr),
			Cause:   errors.Join(err, restoreErr),
		}
	}
	return err
}

func (s *session) checkClosedSessions(ctx context.Context, name string, terminate bool) error {
	count, err := pg.SessionCount(ctx, s.Conn, name)
	var terminateErr error
	if terminate {
		for attempt := range 5 {
			if err != nil || count == 0 {
				break
			}
			if attempt > 0 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(200 * time.Millisecond):
				}
			}
			terminateErr = pg.Terminate(ctx, s.Conn, name)
			count, err = pg.SessionCount(ctx, s.Conn, name)
		}
	}
	if err != nil {
		return errors.Join(err, terminateErr)
	}
	if count == 0 {
		return nil
	}
	message := "database is in use"
	if terminateErr != nil {
		message += ": " + terminateErr.Error()
	}
	return &config.Error{Code: config.DatabaseInUse, Message: message,
		Hint: "Use --terminate to disconnect database sessions.", Cause: terminateErr}
}
