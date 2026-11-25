package branch

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"

	"github.com/toshi0607/pgcheckout/internal/config"
	"github.com/toshi0607/pgcheckout/internal/marker"
	"github.com/toshi0607/pgcheckout/internal/naming"
	"github.com/toshi0607/pgcheckout/internal/pg"
)

type Service struct {
	Config  config.Config
	Connect func(context.Context, string) (*pgx.Conn, error)
}

func ValidateConfig(cfg config.Config, requireBase bool) error {
	if cfg.AdminURL == nil {
		return config.NewError(config.ConfigMissing, "admin URL is required", "Set PGCHECKOUT_ADMIN_URL or pass --admin-url.")
	}
	if cfg.Base == "" {
		if !requireBase {
			return nil
		}
		return config.NewError(config.ConfigMissing, "base name is required", "Set PGCHECKOUT_BASE or pass --base.")
	}
	return naming.ValidateBase(cfg.Base)
}

type session struct {
	*pgx.Conn
	config.Config
	pg.Server
	connect func(context.Context, string) (*pgx.Conn, error)
}

func run[T any](ctx context.Context, svc Service, fn func(*session) (T, error)) (result T, err error) {
	if svc.Config.AdminURL == nil {
		return result, config.NewError(config.ConfigMissing, "admin URL is required", "Set PGCHECKOUT_ADMIN_URL or pass --admin-url.")
	}
	connect := svc.Connect
	if connect == nil {
		connect = connectMaintenance
	}
	adminURL := svc.Config.AdminURL.String()
	conn, err := connect(ctx, adminURL)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return result, &config.Error{Code: config.Canceled, Message: "operation canceled", Cause: errors.Join(ctx.Err(), err)}
		}
		message := config.RedactConnectionDetails(strings.ReplaceAll(err.Error(), adminURL, "<redacted-url>"))
		return result, &config.Error{Code: config.ConnectFailed,
			Message: "could not connect to PostgreSQL: " + message, Cause: err}
	}
	defer func() {
		lost := conn.IsClosed()
		err = databaseError(closeMaintenance(ctx, conn, err), "")
		if lost {
			err = connectionLost(err, adminURL)
		}
	}()
	server, err := prepareMaintenance(ctx, conn, svc.Config.MaintenanceDB())
	if err != nil {
		return result, err
	}
	return fn(&session{Conn: conn, Config: svc.Config, Server: server, connect: connect})
}

func connectionLost(err error, adminURL string) error {
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[*config.Error](err); ok {
		return err
	}
	message := config.RedactConnectionDetails(strings.ReplaceAll(err.Error(), adminURL, "<redacted-url>"))
	return &config.Error{Code: config.ConnectFailed,
		Message: "connection to PostgreSQL was lost during the operation: " + message,
		Hint:    "Retry; pgcheckout doctor reports anything the interrupted operation left behind.", Cause: err}
}

func prepareMaintenance(ctx context.Context, conn *pgx.Conn, database string) (pg.Server, error) {
	if err := pg.Exec(ctx, conn, "use PostgreSQL catalog schema", "SET search_path = pg_catalog"); err != nil {
		return pg.Server{}, err
	}
	server, err := pg.ServerInfo(ctx, conn)
	if err != nil {
		return pg.Server{}, err
	}
	if server.Maintenance != database {
		return pg.Server{}, config.NewError(config.ConfigInvalid, "connected database differs from the admin URL path", "")
	}
	// SQL literals below use standard escaping regardless of the role's session defaults.
	if err := pg.Exec(ctx, conn, "enable standard SQL strings", "SET standard_conforming_strings = on"); err != nil {
		return pg.Server{}, err
	}
	return server, nil
}

func closeMaintenance(ctx context.Context, conn *pgx.Conn, err error) error {
	cleanup, cancel := cleanupContext(ctx)
	defer cancel()
	// Closing a finished session cannot change a successful operation.
	if closeErr := conn.Close(cleanup); closeErr != nil && err != nil {
		return errors.Join(err, fmt.Errorf("close maintenance connection: %w", closeErr))
	}
	return err
}

func (s *session) withCleanupConn(ctx context.Context, fn func(*pgx.Conn) error) (err error) {
	if !s.Conn.IsClosed() {
		return fn(s.Conn)
	}
	adminURL := s.AdminURL.String()
	conn, err := s.connect(ctx, adminURL)
	if err != nil {
		message := config.RedactConnectionDetails(strings.ReplaceAll(err.Error(), adminURL, "<redacted-url>"))
		return &config.Error{Code: config.ConnectFailed, Message: "reconnect to PostgreSQL for cleanup: " + message, Cause: err}
	}
	defer func() { err = closeMaintenance(ctx, conn, err) }()
	if _, err := prepareMaintenance(ctx, conn, s.Maintenance); err != nil {
		return err
	}
	return fn(conn)
}

func connectMaintenance(ctx context.Context, url string) (*pgx.Conn, error) {
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.BuildContextWatcherHandler = func(conn *pgconn.PgConn) ctxwatch.Handler {
		return &pgconn.CancelRequestContextWatcherHandler{Conn: conn, DeadlineDelay: 5 * time.Second}
	}
	return pgx.ConnectConfig(ctx, cfg)
}

func (s *session) lookupBase(ctx context.Context) (pg.Database, error) {
	if s.Base == "" {
		return pg.Database{}, config.NewError(config.ConfigMissing, "base name is required", "Set PGCHECKOUT_BASE or pass --base.")
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
		return pg.Database{}, config.NewError(config.BaseNotMarked, "base database has no base marker", "Use pgcheckout base adopt.")
	default:
		return db, nil
	}
}

func (s *session) copyableBase(ctx context.Context) (pg.Database, error) {
	if s.Base != "" && s.Base == s.Maintenance {
		return pg.Database{}, maintenanceError()
	}
	base, err := s.lookupBase(ctx)
	if err == nil && baseIsOpen(base) {
		return pg.Database{}, baseOpenError()
	}
	return base, err
}

func baseIsOpen(base pg.Database) bool { return base.Marker.IsOpen() || base.AllowConnections }

func baseOpenError() *config.Error {
	return config.NewError(config.BaseOpen, "base database is open", "Use pgcheckout base close.")
}

func maintenanceError() *config.Error {
	return config.NewError(config.ConfigInvalid, "maintenance database cannot be used as a base or copy source", "Use a different database in --admin-url.")
}

func cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return cleanupContextWithTimeout(ctx, 5*time.Second)
}

func cleanupContextWithTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

func (s *session) lock(ctx context.Context, purpose string, try bool, warnings *[]string, fn func() error) (acquired bool, err error) {
	sum := sha256.Sum256([]byte("pgcheckout:" + s.Base + ":" + purpose))
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
			unlockErr = fmt.Errorf("release %s lock: %w", purpose, unlockErr)
		} else if !unlocked {
			unlockErr = fmt.Errorf("%s lock was not held", purpose)
		}
		err = recordCleanup(err, unlockErr, warnings)
	}()
	return true, fn()
}

func (s *session) claim(ctx context.Context, warnings *[]string, fn func() error) error {
	_, err := s.lock(ctx, "claim", false, warnings, fn)
	return err
}

func recordCleanup(err, cleanupErr error, warnings *[]string) error {
	if err == nil && cleanupErr != nil {
		*warnings = append(*warnings, config.RedactConnectionDetails(cleanupErr.Error()))
		return nil
	}
	return errors.Join(err, cleanupErr)
}

func databaseError(err error, hint string) error {
	if _, ok := errors.AsType[*config.Error](err); ok {
		return err
	}
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	if !ok {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return &config.Error{Code: config.Canceled, Message: "operation canceled", Cause: err}
		}
		return err
	}
	code := config.Internal
	switch pgErr.Code {
	case "55006":
		return &config.Error{Code: config.DatabaseInUse, Message: "database is in use", Hint: hint, Cause: err}
	case "42501":
		return &config.Error{Code: config.InsufficientPrivilege, Message: "PostgreSQL denied the database operation", Cause: err}
	case "42P04":
		code = config.DatabaseExists
	case "57014":
		code = config.Canceled
	default:
		if strings.HasPrefix(pgErr.Code, "53") {
			code = config.ServerResources
		}
	}
	return &config.Error{Code: code,
		Message: fmt.Sprintf("%s (SQLSTATE %s)", config.RedactConnectionDetails(pgErr.Message), pgErr.Code), Cause: err}
}
