package branch

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/toshi0607/pgcheckout/internal/config"
	"github.com/toshi0607/pgcheckout/internal/marker"
	"github.com/toshi0607/pgcheckout/internal/naming"
	"github.com/toshi0607/pgcheckout/internal/pg"
)

type copyResult struct {
	database     pg.Database
	elapsed      int64
	warnings     []string
	revokeFailed bool
}

func (s *session) copyFromBase(ctx context.Context, source pg.Database, gen int, strategy string) (copyResult, error) {
	start := time.Now()
	name, err := naming.TmpDB(s.Base)
	if err != nil {
		return copyResult{}, err
	}
	strategy, err = pg.CreateCopy(ctx, s.Conn, name, source.Name, strategy)
	m := marker.New(marker.Tmp, s.Base, gen)
	m.Strategy = strategy
	if err != nil {
		if _, serverError := errors.AsType[*pgconn.PgError](err); !serverError {
			switch left, recoveryErr := s.recoverCopyMarker(ctx, name, m); {
			case recoveryErr != nil:
				return copyResult{}, unmarkedCopyError(name, err, recoveryErr)
			case left:
				mapped := databaseError(err, "")
				status := fmt.Sprintf("temporary database %q was left behind and is marked for cleanup", name)
				return copyResult{}, &config.Error{
					Code:    config.ErrorCode(mapped),
					Message: config.RedactConnectionDetails(mapped.Error() + "; " + status),
					Hint:    "pgcheckout gc drops it one hour after it was created; pgcheckout pool drain drops it now together with the pool. Both need the base to exist.",
					Cause:   err,
				}
			}
		}
		return copyResult{}, err
	}
	if err := s.markNewCopy(ctx, name, m); err != nil {
		return copyResult{}, unmarkedCopyError(name, err, nil)
	}
	d := pg.Database{Name: name, Marker: m}
	actual, found, err := pg.Lookup(ctx, s.Conn, name)
	if err != nil {
		return copyResult{}, s.discardCopy(ctx, d, err)
	}
	if !found {
		return copyResult{}, errDatabaseGone
	}
	if actual.Marker != m {
		return copyResult{}, s.discardCopy(ctx, d, unmanaged())
	}
	warnings := pg.CopySettings(ctx, s.Conn, source, name)
	aclWarnings, revokeFailed := pg.CopyACL(ctx, s.Conn, source, name, actual.Owner)
	warnings = append(warnings, aclWarnings...)
	if err := ctx.Err(); err != nil {
		return copyResult{}, s.discardCopy(ctx, actual, fmt.Errorf("copy database: %w", err))
	}
	return copyResult{database: actual, elapsed: time.Since(start).Milliseconds(), warnings: warnings, revokeFailed: revokeFailed}, nil
}

func (s *session) markNewCopy(ctx context.Context, name string, m marker.Marker) error {
	var err error
	for attempt := range 3 {
		if attempt > 0 {
			time.Sleep(200 * time.Millisecond)
		}
		cleanup, cancel := cleanupContext(ctx)
		if attempt == 0 {
			err = pg.Comment(cleanup, s.Conn, name, m)
		} else {
			err = s.withCleanupConn(cleanup, func(conn *pgx.Conn) error {
				found, err := markUncommentedCopy(cleanup, conn, name, m)
				if err == nil && !found {
					return errors.New("temporary database disappeared before it could be marked")
				}
				return err
			})
		}
		cancel()
		if err == nil {
			return nil
		}
	}
	return err
}

func (s *session) recoverCopyMarker(ctx context.Context, name string, m marker.Marker) (bool, error) {
	cleanup, cancel := cleanupContext(ctx)
	defer cancel()
	var left bool
	err := s.withCleanupConn(cleanup, func(conn *pgx.Conn) error {
		var originalBackendActive bool
		if conn != s.Conn {
			// A lost client connection can leave CREATE running on its original backend.
			err := conn.QueryRow(cleanup, "SELECT EXISTS (SELECT FROM pg_stat_activity WHERE pid = $1)", s.Conn.PgConn().PID()).Scan(&originalBackendActive)
			if err != nil {
				return fmt.Errorf("check original copy session: %w", err)
			}
		}
		found, err := markUncommentedCopy(cleanup, conn, name, m)
		if err == nil && !found && originalBackendActive {
			return errors.New("the original copy session is still active")
		}
		left = found && err == nil
		return err
	})
	return left, err
}

func markUncommentedCopy(ctx context.Context, conn *pgx.Conn, name string, m marker.Marker) (bool, error) {
	db, found, err := pg.Lookup(ctx, conn, name)
	if err != nil || !found {
		return found, err
	}
	if db.Marker == m {
		return true, nil
	}
	if db.Comment != nil {
		return true, unmanaged()
	}
	return true, pg.Comment(ctx, conn, name, m)
}

func unmarkedCopyError(name string, cause, recoveryErr error) error {
	mapped := databaseError(cause, "")
	status := fmt.Sprintf("temporary database %q is unmarked and must be dropped manually", name)
	if recoveryErr != nil {
		status = fmt.Sprintf("could not recover the marker for temporary database %q, which may be unmarked: %s", name, recoveryErr)
	}
	return &config.Error{
		Code:    config.ErrorCode(mapped),
		Message: config.RedactConnectionDetails(mapped.Error() + "; " + status),
		Hint:    fmt.Sprintf("Drop database %s manually; pgcheckout will not drop an unmarked database.", name),
		Cause:   errors.Join(cause, recoveryErr),
	}
}

func databaseGone(err error) bool {
	return errors.Is(err, errDatabaseGone) || pg.State(err) == "3D000"
}
