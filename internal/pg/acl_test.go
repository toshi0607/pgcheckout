package pg

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
)

type failingACLDB struct {
	DB
	rows pgx.Rows
	err  error
}

func (db failingACLDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return db.rows, db.err
}

type failingACLRows struct{ pgx.Rows }

func (failingACLRows) Close()     {}
func (failingACLRows) Next() bool { return false }
func (failingACLRows) Err() error { return errors.New("ACL read failed") }

func TestCopyACLReadFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		db   failingACLDB
	}{
		{"query", failingACLDB{err: errors.New("ACL query failed")}},
		{"rows", failingACLDB{rows: failingACLRows{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			warnings, revokeFailed := CopyACL(t.Context(), tc.db, Database{HasACL: true}, "target", "owner")
			if !revokeFailed || !slices.Equal(warnings, []string{"could not read source database privileges"}) {
				t.Fatalf("CopyACL = %v, %t, want a warning and revokeFailed=true", warnings, revokeFailed)
			}
		})
	}
}
