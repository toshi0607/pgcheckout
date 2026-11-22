//go:build integration

package testpg

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/toshi0607/pgcheckout/internal/naming"
	"github.com/toshi0607/pgcheckout/internal/pg"
)

func CleanupDatabases(ctx context.Context, admin *pgx.Conn, base, source string) error {
	dbs, err := pg.Databases(ctx, admin)
	if err != nil {
		return fmt.Errorf("list test databases for cleanup: %w", err)
	}
	var cleanupErr error
	for _, db := range dbs {
		if db.Name != source && db.Name != base && !naming.IsReservedName(base, db.Name) {
			continue
		}
		if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+pg.Identifier(db.Name)); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("clean up test database %s: %w", db.Name, err))
		}
	}
	return cleanupErr
}
