package pg

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/toshi0607/pgcheckout/internal/marker"
	"github.com/toshi0607/pgcheckout/internal/naming"
)

type commentRecorder struct {
	DB
	sql string
}

func (db *commentRecorder) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	db.sql = sql
	return pgconn.CommandTag{}, nil
}

func TestHostileBranchSQL(t *testing.T) {
	for _, branch := range []string{
		"single'quote", `double"quote`, "semi;colon", `back\slash`, "new\nline",
		strings.Repeat("a", 200), strings.Repeat("a", 48) + "界",
	} {
		name, err := naming.BranchDB("app", branch)
		if err != nil || !utf8.ValidString(name) || len(name) > 63 ||
			strings.ContainsAny(name, "'\";\\\n") {
			t.Fatalf("unsafe database name for %q: %q, %v", branch, name, err)
		}
		m := marker.New(marker.Branch, "app", 1)
		m.Branch = branch
		body, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		var db commentRecorder
		if err := Comment(t.Context(), &db, name, m); err != nil {
			t.Fatal(err)
		}
		want := `COMMENT ON DATABASE "` + name + `" IS '` + strings.ReplaceAll(string(body), "'", "''") + "'"
		if db.sql != want {
			t.Errorf("Comment SQL = %q, want %q", db.sql, want)
		}
	}
	if got := Identifier("a'\";\\\nb"); got != "\"a'\"\";\\\nb\"" {
		t.Errorf("unexpected quoted identifier: %q", got)
	}
}
