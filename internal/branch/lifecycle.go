package branch

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/toshi0607/pgcheckout/internal/config"
	"github.com/toshi0607/pgcheckout/internal/pg"
)

type DSNResult struct {
	Branch   string `json:"branch"`
	Database string `json:"database"`
	URL      string `json:"url"`
}

type DropResult struct {
	Branch   string   `json:"branch"`
	Database string   `json:"database"`
	Dropped  bool     `json:"dropped"`
	Warnings []string `json:"warnings,omitempty"`
}

func (s *session) dsnResult(db pg.Database) DSNResult {
	return DSNResult{Branch: db.Marker.Branch, Database: db.Name, URL: s.DatabaseURL(db.Name)}
}

func (svc Service) DSN(ctx context.Context, branch string) (DSNResult, error) {
	name, err := branchDatabase(svc.Config.Base, branch)
	if err != nil {
		return DSNResult{}, err
	}
	return run(ctx, svc, func(s *session) (DSNResult, error) {
		if _, err := s.lookupBase(ctx); err != nil {
			return DSNResult{}, err
		}
		db, found, err := s.lookupBranch(ctx, name, branch)
		if err != nil {
			return DSNResult{}, err
		}
		if !found {
			return DSNResult{}, branchNotFound()
		}
		return s.dsnResult(db), nil
	})
}

func (svc Service) Drop(ctx context.Context, branch string, force bool) (DropResult, error) {
	name, err := branchDatabase(svc.Config.Base, branch)
	if err != nil {
		return DropResult{}, err
	}
	return run(ctx, svc, func(s *session) (DropResult, error) {
		result := DropResult{Branch: branch, Database: name}
		err := s.claim(ctx, &result.Warnings, func() error {
			if _, err := s.lookupBase(ctx); err != nil {
				return err
			}
			return s.dropBranch(ctx, name, branch, force)
		})
		result.Dropped = err == nil
		return result, err
	})
}

func (svc Service) Reset(ctx context.Context, branch string, force bool, opts EnsureOptions) (EnsureResult, error) {
	if err := ValidateTTL(opts.TTL); err != nil {
		return EnsureResult{}, err
	}
	name, err := branchDatabase(svc.Config.Base, branch)
	if err != nil {
		return EnsureResult{}, err
	}
	return run(ctx, svc, func(s *session) (EnsureResult, error) {
		start := time.Now()
		for {
			result, retry, err := s.resetOnce(ctx, name, branch, force, opts)
			if err != nil {
				return EnsureResult{}, databaseError(err, "")
			}
			if retry {
				continue
			}
			if result.Source == "" {
				// The first claim found no branch to replace.
				warnings := result.Warnings
				result, err = s.ensure(ctx, name, branch, opts)
				result.Warnings = append(warnings, result.Warnings...)
			} else {
				result.ElapsedMS = time.Since(start).Milliseconds()
			}
			result.Reset = true
			return result, err
		}
	})
}

func (s *session) resetOnce(ctx context.Context, name, branch string, force bool, opts EnsureOptions) (EnsureResult, bool, error) {
	var base pg.Database
	var strategy string
	var found bool
	var result EnsureResult
	var retired *pg.Database
	err := s.claim(ctx, &result.Warnings, func() error {
		var err error
		base, err = s.copyableBase(ctx)
		if err != nil {
			return err
		}
		strategy, err = pg.SelectStrategy(s.VersionNum, s.Config.Strategy)
		if err != nil {
			return err
		}
		var old pg.Database
		old, found, err = s.lookupBranch(ctx, name, branch)
		if err != nil || !found {
			return err
		}
		if name == s.Maintenance || name == s.Base {
			return unmanaged()
		}
		if !force {
			sessions, err := pg.SessionCount(ctx, s.Conn, name)
			if err != nil {
				return err
			}
			if sessions > 0 {
				return config.NewError(config.DatabaseInUse, "database is in use", dropInUseHint(false))
			}
		}
		result, retired, err = s.resetFromPool(ctx, old, base.Marker.Gen, branch, force, opts)
		return err
	})
	s.dropReplacedBranch(ctx, retired, force, &result.Warnings)
	if err != nil || !found || result.Source != "" {
		return result, false, err
	}

	copied, err := s.copyFromBase(ctx, base, base.Marker.Gen, strategy)
	if errors.Is(err, errDatabaseGone) {
		return EnsureResult{}, true, nil
	}
	if err != nil {
		return EnsureResult{}, false, err
	}
	if copied.revokeFailed {
		err := config.NewError(config.InsufficientPrivilege, "could not revoke default database privileges from PUBLIC", "")
		return EnsureResult{}, false, s.discardCopy(ctx, copied.database, err)
	}

	retry := false
	err = s.claim(ctx, &result.Warnings, func() error {
		current, err := s.copyableBase(ctx)
		if err != nil {
			return err
		}
		if current.OID != base.OID || current.Marker.Gen != base.Marker.Gen {
			retry = true
			return nil
		}
		old, found, err := s.lookupBranch(ctx, name, branch)
		if err != nil {
			return err
		}
		m := branchMarker(copied.database.Marker, branch, opts.TTL)
		if found {
			swapped, failure, err := s.swapBranch(ctx, old, copied.database, m, force)
			if failure == swapReplacement && databaseGone(err) {
				retry = true
				return nil
			}
			if err != nil {
				return err
			}
			retired = &swapped
		} else {
			if err := ctx.Err(); err != nil {
				return err
			}
			transition, cancel := cleanupContext(ctx)
			err = s.renameManaged(transition, copied.database, name, m)
			cancel()
			if databaseGone(err) {
				retry = true
				return nil
			}
		}
		if err != nil {
			return err
		}
		warnings := result.Warnings
		result = s.ensureResult(pg.Database{Name: name, Marker: m}, current.Marker.Gen, "copy")
		result.Warnings = append(warnings, copied.warnings...)
		s.checkRefill(ctx, &result, opts)
		return nil
	})
	s.dropReplacedBranch(ctx, retired, force, &result.Warnings)
	if result.Source == "" {
		err = s.discardCopy(ctx, copied.database, err)
	}
	return result, retry, err
}

func (s *session) resetFromPool(ctx context.Context, old pg.Database, gen int, branch string, force bool, opts EnsureOptions) (EnsureResult, *pg.Database, error) {
	databases, err := s.poolCandidates(ctx, gen)
	if err != nil {
		return EnsureResult{}, nil, err
	}
	for _, replacement := range databases {
		m := branchMarker(replacement.Marker, branch, opts.TTL)
		retired, failure, err := s.swapBranch(ctx, old, replacement, m, force)
		if failure == swapReplacement && (databaseGone(err) || pg.State(err) == "55006") {
			continue
		}
		if err != nil {
			return EnsureResult{}, nil, err
		}
		result := s.poolClaimResult(old.Name, m, gen)
		s.checkRefill(ctx, &result, opts)
		return result, &retired, nil
	}
	return EnsureResult{}, nil, nil
}

// dropReplacedBranch runs after the claim is released: the replaced database is already a marked tmp.
func (s *session) dropReplacedBranch(ctx context.Context, old *pg.Database, force bool, warnings *[]string) {
	if old == nil {
		return
	}
	cleanup, cancel := cleanupContextWithTimeout(ctx, time.Minute)
	defer cancel()
	err := s.withCleanupConn(cleanup, func(conn *pgx.Conn) error {
		cleaner := *s
		cleaner.Conn = conn
		return cleaner.dropManaged(cleanup, *old, force)
	})
	if err != nil && !databaseGone(err) {
		warning := fmt.Sprintf("could not drop previous branch database %q: %v; run pgcheckout gc to remove it", old.Name, err)
		*warnings = append(*warnings, config.RedactConnectionDetails(warning))
	}
}

func (s *session) dropBranch(ctx context.Context, name, branch string, force bool) error {
	db, found, err := s.lookupBranch(ctx, name, branch)
	if err != nil {
		return err
	}
	if !found {
		return branchNotFound()
	}
	err = s.dropManaged(ctx, db, force)
	if errors.Is(err, errDatabaseGone) || pg.State(err) == "3D000" {
		return branchNotFound()
	}
	return err
}

func branchNotFound() error {
	return config.NewError(config.BranchNotFound, "branch database does not exist", "")
}
