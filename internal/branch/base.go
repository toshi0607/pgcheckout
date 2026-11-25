package branch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/toshi0607/pgcheckout/internal/config"
	"github.com/toshi0607/pgcheckout/internal/marker"
	"github.com/toshi0607/pgcheckout/internal/naming"
	"github.com/toshi0607/pgcheckout/internal/pg"
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

func (s *session) highestStampedGen(ctx context.Context) (int, error) {
	dbs, err := s.managedDatabases(ctx)
	if err != nil {
		return 0, err
	}
	var highest int
	for _, db := range dbs {
		if db.Marker.Kind == marker.Pool || db.Marker.Kind == marker.Branch {
			highest = max(highest, db.Marker.Gen)
		}
	}
	dbs, err = pg.Databases(ctx, s.Conn)
	if err != nil {
		return 0, err
	}
	for _, db := range dbs {
		if db.Name != s.Base && db.Marker.Kind == marker.Base && db.Marker.ValidFor(s.Base) {
			highest = max(highest, db.Marker.Gen)
		}
	}
	return highest, nil
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
			highest, err := s.highestStampedGen(ctx)
			if err != nil {
				return err
			}
			gen := highest + 1
			if terminate {
				if err := pg.Terminate(ctx, s.Conn, from); err != nil {
					return databaseError(err, "")
				}
			}
			copied, err := s.copyFromBase(ctx, source, gen, strategy)
			if err != nil {
				return databaseError(err, "Use --terminate to disconnect source database sessions.")
			}
			if copied.revokeFailed {
				err := config.NewError(config.InsufficientPrivilege, "could not revoke default database privileges from PUBLIC", "")
				return s.discardCopy(ctx, copied.database, err)
			}
			if err := ctx.Err(); err != nil {
				return s.discardCopy(ctx, copied.database, err)
			}
			transition, cancel := cleanupContext(ctx)
			err = s.checkMutation(transition, s.Conn, copied.database, false, marker.Tmp)
			if err == nil {
				err = pg.AllowConnections(transition, s.Conn, copied.database.Name, false)
			}
			if err == nil {
				err = s.checkClosedSessions(transition, copied.database.Name, terminate)
			}
			cancel()
			if err != nil {
				return s.discardCopy(ctx, copied.database, databaseError(err, ""))
			}
			if err := ctx.Err(); err != nil {
				return s.discardCopy(ctx, copied.database, err)
			}
			m := marker.New(marker.Base, name, gen)
			transition, cancel = cleanupContext(ctx)
			err = s.renameManaged(transition, copied.database, name, m)
			cancel()
			if err != nil {
				return s.discardCopy(ctx, copied.database, databaseError(err, ""))
			}
			result = BaseResult{
				BaseInfo: BaseInfo{Name: name, Gen: gen}, Changed: true,
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
			if m.PGCheckout == 1 {
				if m.Kind != marker.Base || !m.ValidFor(name) {
					return unmanaged()
				}
				if baseIsOpen(db) {
					return baseOpenError()
				}
				result.BaseInfo = baseInfo(db)
				return nil
			}
			highest, err := s.highestStampedGen(ctx)
			if err != nil {
				return err
			}
			m = marker.New(marker.Base, name, highest+1)
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
				highest, err := s.highestStampedGen(ctx)
				if err != nil {
					return err
				}
				m.Gen = max(m.Gen, highest) + 1
				if err := s.closeBase(ctx, db, m, terminate, false); err != nil {
					return err
				}
			}
			result.BaseInfo = BaseInfo{Name: db.Name, Gen: m.Gen, Open: open, AllowConnections: open}
			result.Changed = true
			if !open {
				swept, err := s.sweep(ctx, m.Gen, false, false)
				result.Dropped, result.Skipped = swept.Dropped, swept.Skipped
				if err != nil {
					message := config.RedactConnectionDetails(err.Error())
					result.Warnings = append(result.Warnings, "stale pool sweep failed: "+message+"; run pgcheckout gc")
				}
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
	closing := db
	if adopt {
		closing.Marker = m
	}
	closing.Marker.Open = new(true)
	closing.AllowConnections = false
	transition, cancel := cleanupContext(ctx)
	err := s.writeBase(transition, db, closing.Marker, new(false), adopt)
	cancel()
	if err != nil {
		return errors.Join(err, terminateErr)
	}
	if err := s.finishBaseClose(ctx, closing, db, m.Gen, terminate, adopt); err != nil {
		return errors.Join(err, terminateErr)
	}
	return nil
}

func (s *session) finishBaseClose(ctx context.Context, closing, original pg.Database, gen int, terminate, adopt bool) error {
	err := ctx.Err()
	if err == nil {
		cleanup, cancel := cleanupContext(ctx)
		err = s.checkClosedSessions(cleanup, closing.Name, terminate)
		cancel()
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		m := closing.Marker
		m.Open = new(false)
		m.Gen = gen
		cleanup, cancel := cleanupContext(ctx)
		err = s.writeBase(cleanup, closing, m, nil, false)
		cancel()
	}
	if err == nil {
		return nil
	}
	cleanup, cancel := cleanupContext(ctx)
	defer cancel()
	restoreErr := s.withCleanupConn(cleanup, func(conn *pgx.Conn) error {
		allow := !adopt || original.AllowConnections
		return s.mutateBase(cleanup, conn, closing, &allow, false, func(tx pgx.Tx) error {
			if adopt {
				return pg.SetComment(cleanup, tx, closing.Name, original.Comment)
			}
			return pg.Comment(cleanup, tx, closing.Name, closing.Marker)
		})
	})
	if restoreErr != nil {
		mapped := databaseError(err, "")
		message := fmt.Sprintf("%s; could not restore base database %q: %s", err, closing.Name, restoreErr)
		hint := ""
		if problem, ok := errors.AsType[*config.Error](mapped); ok {
			hint = problem.Hint
		}
		return &config.Error{
			Code:    config.ErrorCode(mapped),
			Message: config.RedactConnectionDetails(message),
			Hint:    hint,
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
