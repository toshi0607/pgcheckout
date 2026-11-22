package branch

import (
	"context"
	"strings"

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
		err = s.claim(ctx, func() error {
			_, found, err := pg.Lookup(ctx, s.Conn, name)
			if err != nil {
				return err
			}
			if found {
				return config.NewError(config.ConfigInvalid, "destination base database already exists", "")
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
			if err := s.renameManaged(ctx, copied.database, name, m); err != nil {
				return s.discardCopy(ctx, copied.database, databaseError(err, ""))
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
		err = s.claim(ctx, func() error {
			db, found, err := pg.Lookup(ctx, s.Conn, name)
			if err != nil {
				return err
			}
			if !found {
				return config.NewError(config.BaseNotFound, "base database does not exist", "")
			}
			m := db.Marker
			if m.PGTwig == 1 && (m.Kind != marker.Base || !m.ValidFor(name)) {
				return unmanaged()
			}
			if err := s.quiesce(ctx, name, terminate); err != nil {
				return err
			}
			changed := m.PGTwig == 0 || m.IsOpen() || db.AllowConnections || m.Open == nil
			if changed {
				if m.PGTwig == 0 {
					m = marker.New(marker.Base, name, 1)
				}
				m.Open = new(false)
				if err := s.writeBase(ctx, db, m, true); err != nil {
					return err
				}
			}
			result = BaseResult{BaseInfo: BaseInfo{Name: name, Gen: m.Gen}, Changed: changed}
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
		err = s.claim(ctx, func() error {
			db, err := s.lookupBase(ctx)
			if err != nil {
				return err
			}
			result.BaseInfo = baseInfo(db)
			if db.Marker.IsOpen() == open && (!open || db.AllowConnections) {
				return nil
			}
			m := db.Marker
			if !open {
				if err := s.quiesce(ctx, db.Name, terminate); err != nil {
					return err
				}
				m.Gen++
			}
			m.Open = new(open)
			if err := s.writeBase(ctx, db, m, false); err != nil {
				return err
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

func (s *session) quiesce(ctx context.Context, name string, terminate bool) error {
	if terminate {
		return databaseError(pg.Terminate(ctx, s.Conn, name), "")
	}
	count, err := pg.SessionCount(ctx, s.Conn, name)
	if err != nil {
		return err
	}
	if count > 0 {
		return config.NewError(config.DatabaseInUse, "database is in use", "Use --terminate to disconnect database sessions.")
	}
	return nil
}
