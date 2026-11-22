package branch

import (
	"context"
	"fmt"
	"time"

	"github.com/toshi0607/pgcheckout/internal/config"
	"github.com/toshi0607/pgcheckout/internal/marker"
	"github.com/toshi0607/pgcheckout/internal/pg"
)

type Skipped struct {
	Database string `json:"database"`
	Reason   string `json:"reason"`
}

type GCResult struct {
	Dropped  []string  `json:"dropped"`
	Skipped  []Skipped `json:"skipped"`
	DryRun   bool      `json:"dry_run"`
	Warnings []string  `json:"warnings,omitempty"`
}

func (svc Service) GC(ctx context.Context, dryRun bool) (GCResult, error) {
	return run(ctx, svc, func(s *session) (result GCResult, err error) {
		err = s.claim(ctx, &result.Warnings, func() error {
			base, err := s.lookupBase(ctx)
			if err != nil {
				return err
			}
			result, err = s.sweep(ctx, base.Marker.Gen, true, dryRun)
			return err
		})
		return result, err
	})
}

func (s *session) sweep(ctx context.Context, gen int, gc, dryRun bool) (GCResult, error) {
	dbs, err := s.managedDatabases(ctx)
	if err != nil {
		return GCResult{}, err
	}
	var now time.Time
	if gc {
		if err := s.QueryRow(ctx, "SELECT now()").Scan(&now); err != nil {
			return GCResult{}, fmt.Errorf("read PostgreSQL server time: %w", err)
		}
	}
	tmpCutoff := now.Add(-time.Hour)
	return s.dropCandidates(ctx, dbs, dryRun, func(d pg.Database) bool {
		m := d.Marker
		switch m.Kind {
		case marker.Branch:
			return gc && !m.ExpiresAt.IsZero() && m.ExpiresAt.Before(now)
		case marker.Pool:
			return m.Gen < gen
		case marker.Tmp:
			return m.Gen < gen || (gc && m.Gen == gen && !m.CreatedAt.IsZero() && m.CreatedAt.Before(tmpCutoff))
		default:
			return false
		}
	})
}

func (s *session) dropCandidates(ctx context.Context, dbs []pg.Database, dryRun bool, eligible func(pg.Database) bool) (GCResult, error) {
	result := GCResult{Dropped: make([]string, 0), Skipped: make([]Skipped, 0), DryRun: dryRun}
	for _, d := range dbs {
		if !eligible(d) {
			continue
		}
		if !dryRun {
			err := s.dropManaged(ctx, d, false)
			switch {
			case err == nil:
			case databaseGone(err):
				continue
			case config.ErrorCode(err) == config.DatabaseInUse:
				result.Skipped = append(result.Skipped, Skipped{Database: d.Name, Reason: config.DatabaseInUse})
				continue
			default:
				return result, err
			}
		}
		result.Dropped = append(result.Dropped, d.Name)
	}
	return result, nil
}
