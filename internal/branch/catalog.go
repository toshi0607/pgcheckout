package branch

import (
	"context"
	"errors"
	"time"

	"github.com/toshi0607/pgtwig/internal/config"
	"github.com/toshi0607/pgtwig/internal/marker"
	"github.com/toshi0607/pgtwig/internal/naming"
	"github.com/toshi0607/pgtwig/internal/pg"
)

type BranchInfo struct {
	Branch    string    `json:"branch"`
	Database  string    `json:"database"`
	Gen       int       `json:"gen"`
	Stale     bool      `json:"stale"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at,omitzero"`
	SizeBytes *int64    `json:"size_bytes,omitempty"`
}

type ListResult struct {
	Base      BaseInfo       `json:"base"`
	Branches  []BranchInfo   `json:"branches"`
	Pool      []PoolDatabase `json:"pool"`
	Tmp       []PoolDatabase `json:"tmp"`
	Unmanaged []string       `json:"unmanaged"`
}

func (svc Service) List(ctx context.Context) (ListResult, error) {
	return run(ctx, svc, func(s *session) (ListResult, error) {
		base, err := s.lookupBase(ctx)
		if err != nil {
			return ListResult{}, err
		}
		dbs, unmanaged, err := s.catalog(ctx)
		if err != nil {
			return ListResult{}, err
		}
		result := ListResult{
			Base: baseInfo(base), Branches: make([]BranchInfo, 0), Pool: make([]PoolDatabase, 0),
			Tmp: make([]PoolDatabase, 0), Unmanaged: unmanaged,
		}
		for _, d := range dbs {
			m := d.Marker
			switch m.Kind {
			case marker.Branch:
				info := BranchInfo{Branch: m.Branch, Database: d.Name, Gen: m.Gen, Stale: m.Gen < base.Marker.Gen,
					CreatedAt: m.CreatedAt, ExpiresAt: m.ExpiresAt}
				if size, err := pg.DatabaseSize(ctx, s.Conn, d.OID); err == nil {
					info.SizeBytes = &size
				}
				result.Branches = append(result.Branches, info)
			case marker.Pool:
				result.Pool = append(result.Pool, poolDatabase(d))
			case marker.Tmp:
				result.Tmp = append(result.Tmp, poolDatabase(d))
			}
		}
		return result, nil
	})
}

type DoctorResult struct {
	pg.Server
	Strategy   string         `json:"strategy"`
	Base       *BaseInfo      `json:"base"`
	BaseReason string         `json:"base_reason,omitempty"`
	Pool       PoolCounts     `json:"pool"`
	Unmanaged  []string       `json:"unmanaged"`
	Problems   []config.Error `json:"problems"`
}

func (svc Service) Doctor(ctx context.Context) (DoctorResult, error) {
	return run(ctx, svc, func(s *session) (DoctorResult, error) {
		result := DoctorResult{
			Server: s.Server, Pool: PoolCounts{Target: s.Config.PoolSize}, Problems: make([]config.Error, 0),
		}
		if !s.CanCreateDB {
			result.Problems = append(result.Problems, *config.NewError(config.InsufficientPrivilege,
				"current role lacks CREATEDB", "Use a role with CREATEDB."))
		}

		strategy, err := pg.SelectStrategy(s.VersionNum, s.Config.Strategy)
		if err != nil {
			problem, ok := errors.AsType[*config.Error](err)
			if !ok {
				return DoctorResult{}, err
			}
			result.Problems = append(result.Problems, *problem)
		}
		result.Strategy = strategy

		base, err := s.lookupBase(ctx)
		if err != nil {
			problem, ok := errors.AsType[*config.Error](err)
			if !ok {
				return DoctorResult{}, err
			}
			result.BaseReason = problem.Message
			result.Problems = append(result.Problems, *problem)
		} else {
			info := baseInfo(base)
			result.Base = &info
			if info.Open {
				result.Problems = append(result.Problems, *config.NewError(config.BaseOpen, "base database is open", "Use pgtwig base close."))
			}
		}
		if s.Base == s.Maintenance {
			result.Problems = append(result.Problems, *maintenanceError())
		}

		dbs, unmanaged, err := s.catalog(ctx)
		if err != nil {
			return DoctorResult{}, err
		}
		result.Unmanaged = unmanaged
		if result.Base != nil {
			result.Pool = countPool(dbs, base.Marker.Gen, s.Config.PoolSize)
		}
		if len(unmanaged) > 0 {
			result.Problems = append(result.Problems, *config.NewError(config.UnmanagedDatabase,
				"reserved database names exist without a matching pgtwig marker", "Check the unmanaged database list."))
		}
		return result, nil
	})
}

func (s *session) managedDatabases(ctx context.Context) ([]pg.Database, error) {
	managed, _, err := s.catalog(ctx)
	return managed, err
}

func (s *session) catalog(ctx context.Context) ([]pg.Database, []string, error) {
	managed, unmanaged := make([]pg.Database, 0), make([]string, 0)
	if s.Base == "" {
		return managed, unmanaged, nil
	}
	dbs, err := pg.Databases(ctx, s.Conn)
	if err != nil {
		return nil, nil, err
	}
	for _, d := range dbs {
		valid := d.Marker.ValidFor(s.Base) && ((d.Name == s.Base) == (d.Marker.Kind == marker.Base))
		switch {
		case valid && d.Name != s.Maintenance:
			managed = append(managed, d)
		case !valid && naming.IsReservedName(s.Base, d.Name):
			unmanaged = append(unmanaged, d.Name)
		}
	}
	return managed, unmanaged, nil
}
