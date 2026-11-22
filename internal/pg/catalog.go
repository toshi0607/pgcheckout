package pg

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/toshi0607/pgcheckout/internal/marker"
)

type DB interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Database struct {
	Name             string
	OID              uint32
	Owner            string
	HasACL           bool
	AllowConnections bool
	Comment          *string
	Marker           marker.Marker
}

const databaseSelect = `SELECT d.datname, d.oid, pg_get_userbyid(d.datdba), d.datacl IS NOT NULL,
	d.datallowconn, shobj_description(d.oid, 'pg_database')
	FROM pg_database AS d`

func Lookup(ctx context.Context, db DB, name string) (Database, bool, error) {
	result, err := scanDatabase(db.QueryRow(ctx, databaseSelect+" WHERE d.datname = $1", name))
	if errors.Is(err, pgx.ErrNoRows) {
		return Database{}, false, nil
	}
	if err != nil {
		return Database{}, false, fmt.Errorf("look up database: %w", err)
	}
	return result, true, nil
}

func Databases(ctx context.Context, db DB) ([]Database, error) {
	rows, err := db.Query(ctx, databaseSelect+" ORDER BY d.datname")
	if err != nil {
		return nil, fmt.Errorf("list databases: %w", err)
	}
	results, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Database, error) {
		return scanDatabase(row)
	})
	if err != nil {
		return nil, fmt.Errorf("read databases: %w", err)
	}
	return results, nil
}

func scanDatabase(row pgx.Row) (Database, error) {
	var d Database
	err := row.Scan(&d.Name, &d.OID, &d.Owner, &d.HasACL, &d.AllowConnections, &d.Comment)
	if d.Comment != nil {
		d.Marker, _ = marker.Parse(*d.Comment)
	}
	return d, err
}

func DatabaseSize(ctx context.Context, db DB, oid uint32) (int64, error) {
	var size int64
	if err := db.QueryRow(ctx, "SELECT pg_database_size($1::oid)", oid).Scan(&size); err != nil {
		return 0, fmt.Errorf("read database size: %w", err)
	}
	return size, nil
}

type Server struct {
	Version     string `json:"server_version"`
	VersionNum  int    `json:"server_version_num"`
	Role        string `json:"role"`
	IsSuperuser bool   `json:"is_superuser"`
	CanCreateDB bool   `json:"can_create_db"`
	Maintenance string `json:"maintenance_db"`
}

func ServerInfo(ctx context.Context, db DB) (Server, error) {
	var s Server
	err := db.QueryRow(ctx, `SELECT current_setting('server_version'), current_setting('server_version_num')::integer,
		current_user, rolsuper, rolsuper OR rolcreatedb, current_database()
		FROM pg_roles WHERE rolname = current_user`).Scan(&s.Version, &s.VersionNum, &s.Role, &s.IsSuperuser, &s.CanCreateDB, &s.Maintenance)
	if err != nil {
		return Server{}, fmt.Errorf("read PostgreSQL server information: %w", err)
	}
	return s, nil
}

func State(err error) string {
	if e, ok := errors.AsType[*pgconn.PgError](err); ok {
		return e.Code
	}
	return ""
}
