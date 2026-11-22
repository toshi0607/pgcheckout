//go:build integration

package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/toshi0607/pgcheckout/internal/branch"
	"github.com/toshi0607/pgcheckout/internal/config"
	"github.com/toshi0607/pgcheckout/internal/naming"
	"github.com/toshi0607/pgcheckout/internal/pg"
	"github.com/toshi0607/pgcheckout/internal/testpg"
)

func mcpFixture(t *testing.T) (*mcp.ClientSession, *pgx.Conn, config.Config, string) {
	t.Helper()
	raw := os.Getenv("PGCHECKOUT_TEST_ADMIN_URL")
	if raw == "" {
		t.Skip("PGCHECKOUT_TEST_ADMIN_URL is not set")
	}
	t.Parallel()
	adminURL, err := config.ParseAdminURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	random, err := naming.TmpDB("pt")
	if err != nil {
		t.Fatal(err)
	}
	base := "pgcheckout_mcp_" + strings.TrimPrefix(random, "pt__tmp_")
	source := base + "_source"
	admin, err := pgx.Connect(t.Context(), raw)
	if err != nil {
		t.Fatal("could not connect to the PostgreSQL test database")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := testpg.CleanupDatabases(ctx, admin, base, source); err != nil {
			t.Error(err)
		}
		if err := admin.Close(ctx); err != nil {
			t.Error("close PostgreSQL test connection:", err)
		}
	})
	if _, err := admin.Exec(t.Context(), "CREATE DATABASE "+pg.Identifier(source)+" TEMPLATE template0"); err != nil {
		t.Fatal("create test source:", err)
	}
	cfg := config.Config{AdminURL: adminURL, Base: base, PoolSize: 0, Strategy: "auto"}
	svc := branch.Service{Config: cfg}
	if _, err := svc.BaseCreate(t.Context(), base, source, false); err != nil {
		t.Fatal("create test base:", err)
	}
	cwd := t.TempDir()
	client := startClient(t, Server{
		Version: "test", cwd: cwd, Overrides: config.Overrides{
			AdminURL: &raw, Base: &base, PoolSize: new("0"), Strategy: new("auto"),
		},
	})
	return client, admin, cfg, cwd
}

func successfulTool(t *testing.T, client *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	result := callTool(t, client, name, args)
	if result.IsError {
		t.Fatalf("%s tool error: %s", name, textContent(t, result))
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("%s structuredContent has type %T, want an object", name, result.StructuredContent)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(textContent(t, result)), &decoded); err != nil {
		t.Fatalf("%s text content: %v", name, err)
	}
	if !reflect.DeepEqual(structured, decoded) {
		t.Fatalf("%s text and structured content differ", name)
	}
	return structured
}

func expectEmptyArrays(t *testing.T, result map[string]any, fields ...string) {
	t.Helper()
	for _, field := range fields {
		if !reflect.DeepEqual(result[field], []any{}) {
			t.Errorf("%s = %v, want an empty array", field, result[field])
		}
	}
}

func TestMCPAllTools(t *testing.T) {
	client, _, cfg, cwd := mcpFixture(t)
	expectEmptyArrays(t, successfulTool(t, client, "doctor", nil), "problems", "unmanaged")
	expectEmptyArrays(t, successfulTool(t, client, "list", nil), "branches", "pool", "tmp", "unmanaged")
	expectEmptyArrays(t, successfulTool(t, client, "pool_status", nil), "databases")
	args := map[string]any{"branch": "topic"}
	first := successfulTool(t, client, "ensure", args)
	second := successfulTool(t, client, "ensure", args)
	db, err := naming.BranchDB(cfg.Base, "topic")
	if err != nil {
		t.Fatal(err)
	}
	if first["created"] != true || second["created"] != false || first["database"] != db || second["database"] != db {
		t.Fatal("ensure did not return the same database on the second call")
	}

	dsn := successfulTool(t, client, "dsn", args)
	withEnv := successfulTool(t, client, "ensure", map[string]any{"branch": "topic", "env_file": "branch.env"})
	path := filepath.Join(cwd, "branch.env")
	env, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("read env file:", err)
	}
	if !strings.Contains(string(env), "DATABASE_URL="+cfg.DatabaseURL(db)+"\n") ||
		withEnv["env_file"] != path || withEnv["env_file_changed"] != true {
		t.Fatal("ensure did not write and report the full URL in the managed env file")
	}

	reset := successfulTool(t, client, "reset", map[string]any{"branch": "topic", "force": true})
	if reset["created"] != true || reset["reset"] != true || reset["database"] != db {
		t.Fatal("reset did not recreate the database")
	}
	_, wantRedacted := cfg.AdminURL.User.Password()
	for key := range cfg.AdminURL.Query() {
		wantRedacted = wantRedacted || passwordParameter(key)
	}
	wantURL, _, err := redactURL(cfg.DatabaseURL(db))
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range []map[string]any{first, second, dsn, withEnv, reset} {
		if result["branch"] != "topic" || result["url"] != wantURL || result["password_redacted"] != wantRedacted {
			t.Fatal("unexpected branch name, connection URL, or redaction flag")
		}
	}

	if result := successfulTool(t, client, "drop", args); result["dropped"] != true {
		t.Fatal("drop did not remove the branch")
	}
	for _, dryRun := range []bool{true, false} {
		gc := successfulTool(t, client, "gc", map[string]any{"dry_run": dryRun})
		if gc["dry_run"] != dryRun {
			t.Errorf("gc dry_run = %v, want %t", gc["dry_run"], dryRun)
		}
		expectEmptyArrays(t, gc, "dropped", "skipped")
	}
}

func TestMCPToolErrors(t *testing.T) {
	client, admin, cfg, _ := mcpFixture(t)
	args := map[string]any{"branch": "topic"}
	expectToolError(t, callTool(t, client, "dsn", args), config.BranchNotFound)
	name, err := naming.BranchDB(cfg.Base, "topic")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(t.Context(), "CREATE DATABASE "+pg.Identifier(name)+" TEMPLATE template0"); err != nil {
		t.Fatal("create unmanaged test database:", err)
	}
	expectToolError(t, callTool(t, client, "drop", args), config.UnmanagedDatabase)
}
