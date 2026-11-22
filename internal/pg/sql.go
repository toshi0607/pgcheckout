package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/toshi0607/pgcheckout/internal/marker"
)

func Identifier(name string) string { return pgx.Identifier{name}.Sanitize() }

func Exec(ctx context.Context, db DB, operation, sql string, args ...any) error {
	if _, err := db.Exec(ctx, sql, args...); err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return nil
}

func quoteLiteral(value string) (string, error) {
	if strings.IndexByte(value, 0) >= 0 {
		return "", fmt.Errorf("SQL literal contains NUL")
	}
	return "'" + strings.ReplaceAll(value, "'", "''") + "'", nil
}

func Comment(ctx context.Context, db DB, name string, m marker.Marker) error {
	encoded, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode database marker: %w", err)
	}
	literal, err := quoteLiteral(string(encoded))
	if err != nil {
		return fmt.Errorf("quote database marker: %w", err)
	}
	return Exec(ctx, db, "write database marker", "COMMENT ON DATABASE "+Identifier(name)+" IS "+literal)
}

func AllowConnections(ctx context.Context, db DB, name string, allow bool) error {
	return Exec(ctx, db, "change database connection permission",
		"ALTER DATABASE "+Identifier(name)+" WITH ALLOW_CONNECTIONS "+strconv.FormatBool(allow))
}

func Rename(ctx context.Context, db DB, name, target string) error {
	return Exec(ctx, db, "rename database", "ALTER DATABASE "+Identifier(name)+" RENAME TO "+Identifier(target))
}

func SessionCount(ctx context.Context, db DB, name string) (int, error) {
	var count int
	err := db.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
		WHERE datname = $1 AND pid <> pg_backend_pid() AND backend_type = 'client backend'`, name).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count database sessions: %w", err)
	}
	return count, nil
}

func Terminate(ctx context.Context, db DB, name string) error {
	rows, err := db.Query(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity
		WHERE datname = $1 AND pid <> pg_backend_pid() AND backend_type = 'client backend'`, name)
	if err != nil {
		return fmt.Errorf("terminate database sessions: %w", err)
	}
	terminated, err := pgx.CollectRows(rows, pgx.RowTo[bool])
	if err != nil {
		return fmt.Errorf("terminate database sessions: %w", err)
	}
	for _, ok := range terminated {
		if !ok {
			return fmt.Errorf("could not terminate a database session")
		}
	}
	return nil
}
