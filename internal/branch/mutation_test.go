package branch

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/toshi0607/pgtwig/internal/config"
	"github.com/toshi0607/pgtwig/internal/marker"
	"github.com/toshi0607/pgtwig/internal/pg"
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
	*dest[5].(*string) = db.comment
	return nil
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
