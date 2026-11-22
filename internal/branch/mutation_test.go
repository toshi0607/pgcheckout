package branch

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/toshi0607/pgcheckout/internal/config"
	"github.com/toshi0607/pgcheckout/internal/marker"
	"github.com/toshi0607/pgcheckout/internal/pg"
)

type mutationDB struct {
	pg.DB
	database pg.Database
	comment  string
}

func (db mutationDB) QueryRow(context.Context, string, ...any) pgx.Row { return db }

func (db mutationDB) Scan(dest ...any) error {
	*dest[0].(*string) = db.database.Name
	*dest[1].(*uint32) = db.database.OID
	*dest[2].(*string) = db.database.Owner
	*dest[3].(*bool) = db.database.HasACL
	*dest[4].(*bool) = db.database.AllowConnections
	*dest[5].(**string) = new(db.comment)
	return nil
}

func TestDropInUseHint(t *testing.T) {
	if got := dropInUseHint(false); got != "Use --force to disconnect database sessions." {
		t.Errorf("unforced drop hint = %q", got)
	}
	if got := dropInUseHint(true); got != "Sessions kept reconnecting; retry, or stop the clients first." {
		t.Errorf("forced drop hint = %q", got)
	}
}

func TestCheckMutationComparesWarnings(t *testing.T) {
	s := session{Config: config.Config{Base: "app"}, Server: pg.Server{Maintenance: "postgres"}}
	m := marker.New(marker.Pool, "app", 1)
	m.Warnings = 2
	expected := pg.Database{Name: "app__pool_12345678", OID: 1, Marker: m}
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	db := mutationDB{database: expected, comment: string(body)}
	if err := s.checkMutation(t.Context(), db, expected, false, marker.Pool); err != nil {
		t.Fatalf("unchanged marker rejected: %v", err)
	}
	expected.Marker.Warnings = 3
	if err := s.checkMutation(t.Context(), db, expected, false, marker.Pool); config.ErrorCode(err) != config.UnmanagedDatabase {
		t.Fatalf("changed marker error = %v, want unmanaged_database", err)
	}
}

func TestRetryableForcedDrop(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"insufficient privilege", fmt.Errorf("drop database: %w", &pgconn.PgError{Code: "42501"}), true},
		{"database in use", fmt.Errorf("drop database: %w", &pgconn.PgError{Code: "55006"}), true},
		{"database missing", &pgconn.PgError{Code: "3D000"}, false},
		{"query canceled", &pgconn.PgError{Code: "57014"}, false},
		{"context canceled", context.Canceled, false},
		{"success", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := retryableForcedDrop(tc.err); got != tc.want {
				t.Errorf("retryableForcedDrop(%v) = %t, want %t", tc.err, got, tc.want)
			}
		})
	}
}
