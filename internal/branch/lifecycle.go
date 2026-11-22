package branch

import (
	"context"
	"errors"

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
		var warnings []string
		err := s.claim(ctx, &warnings, func() error {
			if _, err := s.copyableBase(ctx); err != nil {
				return err
			}
			if _, err := pg.SelectStrategy(s.VersionNum, s.Config.Strategy); err != nil {
				return err
			}
			err := s.dropBranch(ctx, name, branch, force)
			if config.ErrorCode(err) == config.BranchNotFound {
				return nil
			}
			return err
		})
		if err != nil {
			return EnsureResult{}, err
		}
		result, err := s.ensure(ctx, name, branch, opts)
		result.Warnings = append(warnings, result.Warnings...)
		result.Reset = true
		return result, err
	})
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
