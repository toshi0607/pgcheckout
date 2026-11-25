package branch

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/toshi0607/pgcheckout/internal/config"
	"github.com/toshi0607/pgcheckout/internal/marker"
	"github.com/toshi0607/pgcheckout/internal/naming"
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
	terminate := force && s.VersionNum < 130000
	sql := "DROP DATABASE " + pg.Identifier(expected.Name)
	if force && !terminate {
		sql += " WITH (FORCE)"
	}

	const maxAttempts = 5
	for attempt := 1; ; attempt++ {
		checkErr := s.checkMutation(ctx, s.Conn, expected, false, kind)
		var err error
		if checkErr == nil && terminate {
			err = pg.Terminate(ctx, s.Conn, expected.Name)
			if err == nil {
				checkErr = s.checkMutation(ctx, s.Conn, expected, false, kind)
			}
		}
		if checkErr != nil {
			if attempt > 1 && errors.Is(checkErr, errDatabaseGone) {
				return nil
			}
			return checkErr
		}

		if err == nil {
			err = pg.Exec(ctx, s.Conn, "drop database", sql)
		}
		if !force || attempt == maxAttempts || !retryableForcedDrop(err) {
			err = databaseError(err, dropInUseHint(force))
			if force {
				if problem, ok := errors.AsType[*config.Error](err); ok && problem.Code == config.InsufficientPrivilege {
					problem.Hint = "A session owned by another role is connected and this role may not terminate it. Retry, or disconnect that session."
				}
			}
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func dropInUseHint(force bool) string {
	if force {
		return "Sessions kept reconnecting; retry, or stop the clients first."
	}
	return "Use --force to disconnect database sessions."
}

func retryableForcedDrop(err error) bool {
	switch pg.State(err) {
	case "42501", "55006":
		return true
	default:
		return false
	}
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
		return pg.Comment(ctx, tx, target, m)
	})
}

type swapFailure uint8

const (
	swapOtherFailure swapFailure = iota
	swapOldRename
	swapReplacement
)

func (s *session) swapBranch(ctx context.Context, old, replacement pg.Database, m marker.Marker, force bool) (pg.Database, swapFailure, error) {
	if old.Name == s.Maintenance || old.Name == s.Base || m.Kind != marker.Branch || !m.ValidFor(s.Base) {
		return pg.Database{}, swapOtherFailure, unmanaged()
	}
	name, err := naming.TmpDB(s.Base)
	if err != nil {
		return pg.Database{}, swapOtherFailure, err
	}
	// Generation 0 is older than every base generation, so gc and base close drop a leftover at once.
	retired := pg.Database{Name: name, OID: old.OID, Marker: marker.New(marker.Tmp, s.Base, 0)}
	const maxAttempts = 5
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return pg.Database{}, swapOtherFailure, err
		}
		failure := swapOtherFailure
		var err error
		retry := false
		if force {
			if err := s.checkMutation(ctx, s.Conn, old, false, marker.Branch); err != nil {
				return pg.Database{}, failure, err
			}
			err = pg.Terminate(ctx, s.Conn, old.Name)
			// A session that left between the scan and the signal also fails here; the next attempt rescans.
			retry = err != nil
		}
		if err == nil {
			if err := ctx.Err(); err != nil {
				return pg.Database{}, failure, err
			}
			// Each RENAME can wait about five seconds for sessions to leave.
			transition, cancel := cleanupContextWithTimeout(ctx, 15*time.Second)
			err = pgx.BeginFunc(transition, s.Conn, func(tx pgx.Tx) error {
				if err := s.checkMutation(transition, tx, old, false, marker.Branch); err != nil {
					return err
				}
				if err := s.checkMutation(transition, tx, replacement, false, marker.Pool, marker.Tmp); err != nil {
					failure = swapReplacement
					return err
				}
				if err := pg.Rename(transition, tx, old.Name, retired.Name); err != nil {
					failure = swapOldRename
					return err
				}
				if err := pg.Comment(transition, tx, retired.Name, retired.Marker); err != nil {
					return err
				}
				if err := pg.Rename(transition, tx, replacement.Name, old.Name); err != nil {
					failure = swapReplacement
					return err
				}
				return pg.Comment(transition, tx, old.Name, m)
			})
			cancel()
			if err == nil {
				return retired, swapOtherFailure, nil
			}
			retry = failure == swapOldRename && pg.State(err) == "55006"
		}
		if !force || attempt == maxAttempts || !retry {
			hint := ""
			if failure == swapOldRename {
				hint = dropInUseHint(force)
			}
			err = databaseError(err, hint)
			if force {
				if problem, ok := errors.AsType[*config.Error](err); ok && problem.Code == config.InsufficientPrivilege {
					problem.Hint = "A session owned by another role is connected and this role may not terminate it. Retry, or disconnect that session."
				}
			}
			return pg.Database{}, failure, err
		}
		select {
		case <-ctx.Done():
			return pg.Database{}, failure, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func (s *session) writeBase(ctx context.Context, base pg.Database, m marker.Marker, allow *bool, adopt bool) error {
	return s.mutateBase(ctx, s.Conn, base, allow, adopt, func(tx pgx.Tx) error {
		return pg.Comment(ctx, tx, base.Name, m)
	})
}

func (s *session) mutateBase(ctx context.Context, conn *pgx.Conn, base pg.Database, allow *bool, adopt bool, writeComment func(pgx.Tx) error) error {
	err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if err := s.checkMutation(ctx, tx, base, adopt, marker.Base); err != nil {
			return err
		}
		if allow != nil {
			if err := pg.AllowConnections(ctx, tx, base.Name, *allow); err != nil {
				return err
			}
		}
		return writeComment(tx)
	})
	return databaseError(err, "")
}

func (s *session) discardCopy(ctx context.Context, d pg.Database, cause error) error {
	cleanup, cancel := cleanupContext(ctx)
	defer cancel()
	err := s.withCleanupConn(cleanup, func(conn *pgx.Conn) error {
		cleaner := *s
		cleaner.Conn = conn
		return cleaner.dropManaged(cleanup, d, false)
	})
	if err != nil && !errors.Is(err, errDatabaseGone) && pg.State(err) != "3D000" {
		cleanupErr := fmt.Errorf("clean up temporary database %q: %w", d.Name, err)
		if problem, ok := errors.AsType[*config.Error](databaseError(cause, "")); ok {
			return &config.Error{Code: problem.Code, Hint: problem.Hint,
				Message: config.RedactConnectionDetails(problem.Message + "; " + cleanupErr.Error()),
				Cause:   errors.Join(cause, cleanupErr)}
		}
		return errors.Join(cause, cleanupErr)
	}
	return cause
}
