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

type PoolCounts struct {
	Target int `json:"target"`
	Ready  int `json:"ready"`
	Stale  int `json:"stale"`
}

type PoolStatus struct {
	PoolCounts
	Databases []PoolDatabase `json:"databases"`
}

type PoolDatabase struct {
	Name      string    `json:"name"`
	Gen       int       `json:"gen"`
	CreatedAt time.Time `json:"created_at"`
}

type FillResult struct {
	Created   int      `json:"created"`
	Discarded int      `json:"discarded"`
	PoolSize  int      `json:"pool_size"`
	Target    int      `json:"target"`
	Strategy  string   `json:"strategy"`
	ElapsedMS int64    `json:"elapsed_ms"`
	Skipped   string   `json:"skipped,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
}

type DrainResult struct {
	Dropped  []string  `json:"dropped"`
	Skipped  []Skipped `json:"skipped,omitempty"`
	Warnings []string  `json:"warnings,omitempty"`
}

func (svc Service) PoolFill(ctx context.Context, size int) (FillResult, error) {
	if size < 0 {
		return FillResult{}, config.NewError(config.Usage, "pool size must be non-negative", "")
	}
	return run(ctx, svc, func(s *session) (FillResult, error) {
		start := time.Now()
		result := FillResult{Target: size}
		acquired, err := s.lock(ctx, "fill", true, &result.Warnings, func() error {
			strategy, err := pg.SelectStrategy(s.VersionNum, s.Config.Strategy)
			if err != nil {
				return err
			}
			result.Strategy = strategy
			for {
				done, err := s.fillOne(ctx, &result, strategy)
				if done || err != nil {
					return err
				}
			}
		})
		if err == nil && !acquired {
			result.Skipped = "another fill is running"
		}
		result.ElapsedMS = time.Since(start).Milliseconds()
		return result, databaseError(err, "")
	})
}

func (s *session) fillOne(ctx context.Context, result *FillResult, strategy string) (bool, error) {
	var base pg.Database
	err := s.claim(ctx, &result.Warnings, func() error {
		var err error
		base, err = s.copyableBase(ctx)
		if err != nil {
			return err
		}
		dbs, err := s.managedDatabases(ctx)
		if err != nil {
			return err
		}
		result.PoolSize = countPool(dbs, base.Marker.Gen, result.Target).Ready
		return nil
	})
	if err != nil || result.PoolSize >= result.Target {
		return true, err
	}

	name, err := naming.PoolDB(s.Base)
	if err != nil {
		return false, err
	}
	copied, err := s.copyFromBase(ctx, base, base.Marker.Gen, strategy)
	if errors.Is(err, errDatabaseGone) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	result.Strategy = copied.database.Marker.Strategy
	result.Warnings = append(result.Warnings, copied.warnings...)
	if copied.revokeFailed {
		if err := s.discardCopy(ctx, copied.database, nil); err != nil {
			return true, err
		}
		result.Discarded++
		// Another copy of this base would repeat the failed revocation.
		return true, nil
	}
	published := false
	err = s.claim(ctx, &result.Warnings, func() error {
		current, err := s.copyableBase(ctx)
		if err != nil {
			return err
		}
		if current.OID != base.OID || current.Marker.Gen != base.Marker.Gen {
			return nil
		}
		m := copied.database.Marker
		m.Kind = marker.Pool
		m.Warnings = len(copied.warnings)
		if err := s.renameManaged(ctx, copied.database, name, m); err != nil {
			if databaseGone(err) {
				return nil
			}
			return err
		}
		published = true
		result.Created++
		return nil
	})
	if !published {
		err = s.discardCopy(ctx, copied.database, err)
	}
	return false, err
}

func (svc Service) PoolStatus(ctx context.Context) (PoolStatus, error) {
	return run(ctx, svc, func(s *session) (PoolStatus, error) {
		base, err := s.lookupBase(ctx)
		if err != nil {
			return PoolStatus{}, err
		}
		dbs, err := s.managedDatabases(ctx)
		if err != nil {
			return PoolStatus{}, err
		}
		result := PoolStatus{
			PoolCounts: countPool(dbs, base.Marker.Gen, s.Config.PoolSize),
			Databases:  make([]PoolDatabase, 0),
		}
		for _, d := range dbs {
			if d.Marker.Kind == marker.Pool {
				result.Databases = append(result.Databases, poolDatabase(d))
			}
		}
		return result, nil
	})
}

func (svc Service) PoolDrain(ctx context.Context) (DrainResult, error) {
	return run(ctx, svc, func(s *session) (result DrainResult, err error) {
		err = s.claim(ctx, &result.Warnings, func() error {
			if _, err := s.lookupBase(ctx); err != nil {
				return err
			}
			dbs, err := s.managedDatabases(ctx)
			if err != nil {
				return err
			}
			swept, err := s.dropCandidates(ctx, dbs, false, func(d pg.Database) bool {
				return d.Marker.Kind == marker.Pool || d.Marker.Kind == marker.Tmp
			})
			result = DrainResult{Dropped: swept.Dropped, Skipped: swept.Skipped}
			return err
		})
		return result, err
	})
}

func poolDatabase(d pg.Database) PoolDatabase {
	return PoolDatabase{Name: d.Name, Gen: d.Marker.Gen, CreatedAt: d.Marker.CreatedAt}
}

func countPool(dbs []pg.Database, gen, target int) PoolCounts {
	result := PoolCounts{Target: target}
	for _, d := range dbs {
		if d.Marker.Kind != marker.Pool {
			continue
		}
		switch {
		case d.Marker.Gen == gen:
			result.Ready++
		case d.Marker.Gen < gen:
			result.Stale++
		}
	}
	return result
}
