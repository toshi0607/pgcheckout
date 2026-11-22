package branch

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/toshi0607/pgcheckout/internal/config"
	"github.com/toshi0607/pgcheckout/internal/marker"
	"github.com/toshi0607/pgcheckout/internal/pg"
)

var errDatabaseGone = errors.New("database disappeared")

func unmanaged() error {
	return config.NewError(config.UnmanagedDatabase, "database does not have the expected pgcheckout marker", "")
}

// checkMutation revalidates the catalog row immediately before destructive SQL.
func (s *session) checkMutation(ctx context.Context, db pg.DB, expected pg.Database, adopt bool, kinds ...string) error {
	name := expected.Name
	baseChange := slices.Contains(kinds, marker.Base)
	if name == s.Maintenance || (name == s.Base) != baseChange {
		return unmanaged()
	}

	actual, found, err := pg.Lookup(ctx, db, name)
	switch {
	case err != nil:
		return err
	case !found:
		return errDatabaseGone
	case expected.OID != 0 && actual.OID != expected.OID:
		return unmanaged()
	case baseChange && actual.AllowConnections != expected.AllowConnections:
		return unmanaged()
	case adopt && name == s.Base && actual.Marker.PGCheckout == 0 && expected.Marker.PGCheckout == 0:
		return nil
	}
	m := actual.Marker
	if !m.ValidFor(s.Base) || !slices.Contains(kinds, m.Kind) ||
		m.Kind != expected.Marker.Kind || m.Branch != expected.Marker.Branch || m.Gen != expected.Marker.Gen ||
		m.IsOpen() != expected.Marker.IsOpen() || m.Strategy != expected.Marker.Strategy ||
		m.Warnings != expected.Marker.Warnings ||
		!m.CreatedAt.Equal(expected.Marker.CreatedAt) || !m.ExpiresAt.Equal(expected.Marker.ExpiresAt) {
		return unmanaged()
	}
	return nil
}

func (s *session) dropManaged(ctx context.Context, expected pg.Database, force bool) error {
	kind := expected.Marker.Kind
	switch kind {
	case marker.Branch, marker.Pool, marker.Tmp:
	default:
		return unmanaged()
	}
	if err := s.checkMutation(ctx, s.Conn, expected, false, kind); err != nil {
		return err
	}
	sql := "DROP DATABASE " + pg.Identifier(expected.Name)
	if force && s.VersionNum >= 130000 {
		sql += " WITH (FORCE)"
	} else if force {
		if err := pg.Terminate(ctx, s.Conn, expected.Name); err != nil {
			return databaseError(err, "")
		}
		if err := s.checkMutation(ctx, s.Conn, expected, false, kind); err != nil {
			return err
		}
	}
	return databaseError(pg.Exec(ctx, s.Conn, "drop database", sql), "Use --force to disconnect database sessions.")
}

func (s *session) renameManaged(ctx context.Context, from pg.Database, target string, m marker.Marker) error {
	if target == s.Maintenance || (target == s.Base) != (m.Kind == marker.Base) || !m.ValidFor(s.Base) {
		return unmanaged()
	}
	return pgx.BeginFunc(ctx, s.Conn, func(tx pgx.Tx) error {
		if err := s.checkMutation(ctx, tx, from, false, marker.Pool, marker.Tmp); err != nil {
			return err
		}
		if err := pg.Rename(ctx, tx, from.Name, target); err != nil {
			return err
		}
		if err := pg.Comment(ctx, tx, target, m); err != nil {
			return err
		}
		if m.Kind == marker.Base {
			return pg.AllowConnections(ctx, tx, target, false)
		}
		return nil
	})
}

func (s *session) writeBase(ctx context.Context, base pg.Database, m marker.Marker, allow *bool, adopt bool) error {
	err := pgx.BeginFunc(ctx, s.Conn, func(tx pgx.Tx) error {
		if err := s.checkMutation(ctx, tx, base, adopt, marker.Base); err != nil {
			return err
		}
		if allow != nil {
			if err := pg.AllowConnections(ctx, tx, base.Name, *allow); err != nil {
				return err
			}
		}
		return pg.Comment(ctx, tx, base.Name, m)
	})
	return databaseError(err, "")
}

func (s *session) discardCopy(ctx context.Context, d pg.Database, cause error) error {
	cleanup, cancel := cleanupContext(ctx)
	defer cancel()
	err := s.dropManaged(cleanup, d, false)
	if err != nil && !errors.Is(err, errDatabaseGone) && pg.State(err) != "3D000" {
		cleanupErr := fmt.Errorf("clean up temporary database %q: %w", d.Name, err)
		if problem, ok := errors.AsType[*config.Error](cause); ok {
			return &config.Error{Code: problem.Code, Hint: problem.Hint,
				Message: config.RedactConnectionDetails(problem.Message + "; " + cleanupErr.Error()),
				Cause:   errors.Join(cause, cleanupErr)}
		}
		return errors.Join(cause, cleanupErr)
	}
	return cause
}
