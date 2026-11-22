package branch

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/toshi0607/pgtwig/internal/config"
	"github.com/toshi0607/pgtwig/internal/marker"
	"github.com/toshi0607/pgtwig/internal/naming"
	"github.com/toshi0607/pgtwig/internal/pg"
)

type Service struct {
	Config  config.Config
	Connect func(context.Context, string) (*pgx.Conn, error)
}

type session struct {
	*pgx.Conn
	config.Config
	pg.Server
}

func run[T any](ctx context.Context, svc Service, fn func(*session) (T, error)) (result T, err error) {
	if svc.Config.AdminURL == nil {
		return result, config.NewError(config.ConfigMissing, "admin URL is required", "Set PGTWIG_ADMIN_URL or pass --admin-url.")
	}
	connect := svc.Connect
	if connect == nil {
		connect = pgx.Connect
	}
	conn, err := connect(ctx, svc.Config.AdminURL.String())
	if err != nil {
		return result, &config.Error{Code: config.ConnectFailed, Message: "could not connect to PostgreSQL", Cause: err}
	}
	defer func() {
		cleanup, cancel := cleanupContext(ctx)
		defer cancel()
		if closeErr := conn.Close(cleanup); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close maintenance connection: %w", closeErr))
		}
	}()
	if err := pg.Exec(ctx, conn, "use PostgreSQL catalog schema", "SET search_path = pg_catalog"); err != nil {
		return result, err
	}
	server, err := pg.ServerInfo(ctx, conn)
	if err != nil {
		return result, err
	}
	if server.Maintenance != svc.Config.MaintenanceDB() {
		return result, config.NewError(config.ConfigInvalid, "connected database differs from the admin URL path", "")
	}
	// SQL literals below use standard escaping regardless of the role's session defaults.
	if err := pg.Exec(ctx, conn, "enable standard SQL strings", "SET standard_conforming_strings = on"); err != nil {
		return result, err
	}
	result, err = fn(&session{Conn: conn, Config: svc.Config, Server: server})
	return result, databaseError(err, "")
}

func (s *session) lookupBase(ctx context.Context) (pg.Database, error) {
	if s.Base == "" {
		return pg.Database{}, config.NewError(config.ConfigMissing, "base name is required", "Set PGTWIG_BASE or pass --base.")
	}
	if err := naming.ValidateBase(s.Base); err != nil {
		return pg.Database{}, err
	}
	db, found, err := pg.Lookup(ctx, s.Conn, s.Base)
	switch {
	case err != nil:
		return pg.Database{}, err
	case !found:
		return pg.Database{}, config.NewError(config.BaseNotFound, "base database does not exist", "")
	case !db.Marker.ValidFor(s.Base) || db.Marker.Kind != marker.Base:
		return pg.Database{}, config.NewError(config.BaseNotMarked, "base database has no base marker", "Use pgtwig base adopt.")
	default:
		return db, nil
	}
}

func (s *session) copyableBase(ctx context.Context) (pg.Database, error) {
	if s.Base != "" && s.Base == s.Maintenance {
		return pg.Database{}, maintenanceError()
	}
	base, err := s.lookupBase(ctx)
	if err == nil && base.Marker.IsOpen() {
		return pg.Database{}, config.NewError(config.BaseOpen, "base database is open", "Use pgtwig base close.")
	}
	return base, err
}

func maintenanceError() *config.Error {
	return config.NewError(config.ConfigInvalid, "maintenance database cannot be used as a base or copy source", "Use a different database in --admin-url.")
}

func cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

func (s *session) lock(ctx context.Context, purpose string, try bool, fn func() error) (acquired bool, err error) {
	sum := sha256.Sum256([]byte("pgtwig:" + s.Base + ":" + purpose))
	key := int64(binary.BigEndian.Uint64(sum[:8]))
	if try {
		if err := s.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&acquired); err != nil {
			return false, fmt.Errorf("try %s lock: %w", purpose, err)
		}
		if !acquired {
			return false, nil
		}
	} else if err := pg.Exec(ctx, s.Conn, "take "+purpose+" lock", "SELECT pg_advisory_lock($1)", key); err != nil {
		return false, err
	}
	defer func() {
		cleanup, cancel := cleanupContext(ctx)
		defer cancel()
		var unlocked bool
		unlockErr := s.QueryRow(cleanup, "SELECT pg_advisory_unlock($1)", key).Scan(&unlocked)
		if unlockErr != nil {
			err = errors.Join(err, fmt.Errorf("release %s lock: %w", purpose, unlockErr))
		} else if !unlocked {
			err = errors.Join(err, fmt.Errorf("%s lock was not held", purpose))
		}
	}()
	return true, fn()
}

func (s *session) claim(ctx context.Context, fn func() error) error {
	_, err := s.lock(ctx, "claim", false, fn)
	return err
}

func databaseError(err error, hint string) error {
	if _, ok := errors.AsType[*config.Error](err); ok {
		return err
	}
	switch pg.State(err) {
	case "55006":
		return &config.Error{Code: config.DatabaseInUse, Message: "database is in use", Hint: hint, Cause: err}
	case "42501":
		return &config.Error{Code: config.InsufficientPrivilege, Message: "PostgreSQL denied the database operation", Cause: err}
	default:
		return err
	}
}
