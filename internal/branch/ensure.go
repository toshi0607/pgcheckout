package branch

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"time"

	"github.com/toshi0607/pgtwig/internal/config"
	"github.com/toshi0607/pgtwig/internal/marker"
	"github.com/toshi0607/pgtwig/internal/naming"
	"github.com/toshi0607/pgtwig/internal/pg"
)

type EnsureOptions struct {
	TTL      *time.Duration
	NoRefill bool
}

type EnsureResult struct {
	DSNResult
	Created       bool      `json:"created"`
	Source        string    `json:"source"`
	Stale         bool      `json:"stale"`
	Gen           int       `json:"gen"`
	BaseGen       int       `json:"base_gen"`
	Strategy      string    `json:"strategy"`
	ElapsedMS     int64     `json:"elapsed_ms"`
	ExpiresAt     time.Time `json:"expires_at,omitzero"`
	Warnings      []string  `json:"warnings,omitempty"`
	RefillStarted bool      `json:"refill_started"`
	RefillNeeded  bool      `json:"-"`
	Reset         bool      `json:"reset,omitempty"`
}

func (svc Service) Ensure(ctx context.Context, branch string, opts EnsureOptions) (EnsureResult, error) {
	name, err := branchDatabase(svc.Config.Base, branch)
	if err != nil {
		return EnsureResult{}, err
	}
	return run(ctx, svc, func(s *session) (EnsureResult, error) {
		return s.ensure(ctx, name, branch, opts)
	})
}

func (s *session) ensure(ctx context.Context, name, branch string, opts EnsureOptions) (EnsureResult, error) {
	start := time.Now()
	for {
		result, retry, err := s.ensureOnce(ctx, name, branch, opts)
		if err != nil {
			return EnsureResult{}, databaseError(err, "")
		}
		if !retry {
			result.ElapsedMS = time.Since(start).Milliseconds()
			return result, nil
		}
	}
}

func (s *session) ensureOnce(ctx context.Context, name, branch string, opts EnsureOptions) (EnsureResult, bool, error) {
	var base pg.Database
	var result EnsureResult
	var strategy string
	err := s.claim(ctx, func() error {
		var err error
		base, err = s.copyableBase(ctx)
		if err != nil {
			return err
		}
		strategy, err = pg.SelectStrategy(s.VersionNum, s.Config.Strategy)
		if err != nil {
			return err
		}
		result, err = s.claimExistingOrPool(ctx, base.Marker.Gen, name, branch, opts)
		return err
	})
	if err != nil || result.Source != "" {
		return result, false, err
	}

	copied, err := s.copyFromBase(ctx, base, base.Marker.Gen, strategy)
	if errors.Is(err, errDatabaseGone) {
		return EnsureResult{}, true, nil
	}
	if err != nil {
		return EnsureResult{}, false, err
	}
	published := false
	retry := false
	err = s.claim(ctx, func() error {
		current, err := s.copyableBase(ctx)
		if err != nil {
			return err
		}
		db, found, err := s.lookupBranch(ctx, name, branch)
		if err != nil {
			return err
		}
		if found {
			result = s.ensureResult(db, current.Marker.Gen, "existing")
			return s.checkRefill(ctx, &result, opts)
		}
		if base.OID != current.OID || base.Marker.Gen != current.Marker.Gen {
			retry = true
			return nil
		}
		m := branchMarker(copied.database.Marker, branch, opts.TTL)
		if err := s.renameManaged(ctx, copied.database, name, m); err != nil {
			if databaseGone(err) {
				retry = true
				return nil
			}
			return err
		}
		published = true
		result = s.ensureResult(pg.Database{Name: name, Marker: m}, current.Marker.Gen, "copy")
		result.Warnings = copied.warnings
		return s.checkRefill(ctx, &result, opts)
	})
	if !published {
		err = s.discardCopy(ctx, copied.database, err)
		if result.Source == "existing" {
			result.Warnings = append(copied.warnings, result.Warnings...)
		}
	}
	return result, retry, err
}

func (s *session) claimExistingOrPool(ctx context.Context, gen int, name, branch string, opts EnsureOptions) (EnsureResult, error) {
	db, found, err := s.lookupBranch(ctx, name, branch)
	if err != nil {
		return EnsureResult{}, err
	}
	if found {
		result := s.ensureResult(db, gen, "existing")
		return result, s.checkRefill(ctx, &result, opts)
	}
	if s.Config.PoolSize == 0 {
		return EnsureResult{}, nil
	}
	databases, err := s.managedDatabases(ctx)
	if err != nil {
		return EnsureResult{}, err
	}
	slices.SortFunc(databases, func(a, b pg.Database) int {
		if order := a.Marker.CreatedAt.Compare(b.Marker.CreatedAt); order != 0 {
			return order
		}
		return cmp.Compare(a.Name, b.Name)
	})
	for _, d := range databases {
		if d.Marker.Kind != marker.Pool || d.Marker.Gen != gen {
			continue
		}
		m := branchMarker(d.Marker, branch, opts.TTL)
		err := s.renameManaged(ctx, d, name, m)
		if databaseGone(err) || pg.State(err) == "55006" {
			continue
		}
		if err != nil {
			return EnsureResult{}, err
		}
		result := s.ensureResult(pg.Database{Name: name, Marker: m}, gen, "pool")
		return result, s.checkRefill(ctx, &result, opts)
	}
	return EnsureResult{}, nil
}

func branchMarker(m marker.Marker, branch string, ttl *time.Duration) marker.Marker {
	now := time.Now().UTC()
	m.Kind, m.Branch, m.CreatedAt = marker.Branch, branch, now
	m.ExpiresAt = time.Time{}
	if ttl != nil {
		m.ExpiresAt = now.Add(*ttl)
	}
	return m
}

func (s *session) ensureResult(db pg.Database, gen int, source string) EnsureResult {
	return EnsureResult{
		DSNResult: s.dsnResult(db), Source: source, Created: source != "existing",
		Gen: db.Marker.Gen, BaseGen: gen, Stale: db.Marker.Gen < gen,
		Strategy: db.Marker.Strategy, ExpiresAt: db.Marker.ExpiresAt,
	}
}

func (s *session) checkRefill(ctx context.Context, result *EnsureResult, opts EnsureOptions) error {
	if opts.NoRefill || s.Config.PoolSize == 0 {
		return nil
	}
	dbs, err := s.managedDatabases(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		result.Warnings = append(result.Warnings, "could not check whether the pool needs a refill")
		return nil
	}
	result.RefillNeeded = countPool(dbs, result.BaseGen, s.Config.PoolSize).Ready < s.Config.PoolSize
	return nil
}

func (s *session) lookupBranch(ctx context.Context, name, branch string) (pg.Database, bool, error) {
	db, found, err := pg.Lookup(ctx, s.Conn, name)
	if err != nil || !found {
		return db, found, err
	}
	m := db.Marker
	if !m.ValidFor(s.Base) || m.Kind != marker.Branch || m.Branch != branch {
		return pg.Database{}, false, unmanaged()
	}
	return db, true, nil
}

func branchDatabase(base, branch string) (string, error) {
	if base == "" {
		return "", config.NewError(config.ConfigMissing, "base name is required", "Set PGTWIG_BASE or pass --base.")
	}
	return naming.BranchDB(base, branch)
}
