package config

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPrecedenceAndDiscovery(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "one", "two")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, root, `{"base":"file_base","pool_size":4,"strategy":"wal_log"}`)
	for _, tc := range []struct {
		name     string
		env      map[string]string
		flags    Overrides
		base     string
		size     int
		strategy string
		adminDB  string
	}{
		{"file", nil, Overrides{}, "file_base", 4, "wal_log", ""},
		{"env", map[string]string{
			"PGCHECKOUT_BASE": "env_base", "PGCHECKOUT_POOL_SIZE": "0", "PGCHECKOUT_STRATEGY": "file_copy",
			"PGCHECKOUT_ADMIN_URL": "postgresql://localhost/env_admin",
		}, Overrides{}, "env_base", 0, "file_copy", "env_admin"},
		{"flag", map[string]string{
			"PGCHECKOUT_BASE": "env_base", "PGCHECKOUT_POOL_SIZE": "invalid", "PGCHECKOUT_STRATEGY": "invalid",
			"PGCHECKOUT_ADMIN_URL": "invalid",
		}, Overrides{Base: new("flag_base"), PoolSize: new("6"), Strategy: new("default"), AdminURL: new("postgres://localhost/flag_admin")},
			"flag_base", 6, "default", "flag_admin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(nested, tc.flags, func(name string) (string, bool) {
				if name == "DATABASE_URL" {
					t.Fatal("DATABASE_URL was consulted")
				}
				value, ok := tc.env[name]
				return value, ok
			})
			if err != nil {
				t.Fatal(err)
			}
			db := ""
			if cfg.AdminURL != nil {
				db = cfg.MaintenanceDB()
			}
			if cfg.Base != tc.base || cfg.PoolSize != tc.size || cfg.Strategy != tc.strategy || db != tc.adminDB {
				t.Errorf("unexpected config: %+v", cfg)
			}
		})
	}

	writeConfig(t, nested, `{"base":"nearest"}`)
	nearest, err := Load(nested, Overrides{}, noEnv)
	if err != nil || nearest.Base != "nearest" || nearest.PoolSize != 2 || nearest.Strategy != "auto" {
		t.Errorf("unexpected nearest config: %+v, %v", nearest, err)
	}
}

func TestDiscoveryStopsAtGitBoundary(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "repo")
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, parent, `{"base":"wrong"}`)
	if got, err := discover(nested); err != nil || got.Base != "" {
		t.Fatalf("discovery outside a Git repository = %+v, %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: elsewhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := discover(nested); err != nil || got.Base != "" {
		t.Fatalf("discovery with a Git boundary = %+v, %v", got, err)
	}
	writeConfig(t, root, `{"base":"right"}`)
	if got, err := discover(nested); err != nil || got.Base != "right" {
		t.Fatalf("discovery at the Git boundary = %+v, %v", got, err)
	}
}

func TestInvalidConfig(t *testing.T) {
	for _, file := range []string{
		`{"admin_url":"postgres://localhost/db"}`, `{"base":1}`, `{"pool_size":null}`,
		`{"pool_size":-1}`, `{"strategy":""}`, `{"BASE":"app"}`, `{} {}`,
	} {
		t.Run(file, func(t *testing.T) {
			dir := t.TempDir()
			writeConfig(t, dir, file)
			if _, err := Load(dir, Overrides{}, noEnv); ErrorCode(err) != ConfigInvalid {
				t.Errorf("Load() error = %v, want config_invalid", err)
			}
		})
	}
	for _, raw := range []string{"mysql://localhost/db", "postgresql://localhost", "postgres:/db", "postgres://localhost/%00"} {
		if _, err := ParseAdminURL(raw); ErrorCode(err) != ConfigInvalid {
			t.Errorf("ParseAdminURL(%q) error = %v, want config_invalid", raw, err)
		}
	}
}

func TestDatabaseURL(t *testing.T) {
	admin, err := ParseAdminURL("postgresql://alice:s%40cret@[::1]:5439/admin?sslmode=require&application_name=one%20two")
	if err != nil {
		t.Fatal(err)
	}
	got, err := url.Parse((Config{AdminURL: admin}).DatabaseURL("app__feat"))
	if err != nil {
		t.Fatal(err)
	}
	password, _ := got.User.Password()
	if got.Path != "/app__feat" || got.User.Username() != "alice" || password != "s@cret" ||
		got.Host != admin.Host || got.RawQuery != admin.RawQuery || got.Scheme != admin.Scheme || admin.Path != "/admin" {
		t.Errorf("database URL did not preserve the admin URL components")
	}
}

func noEnv(string) (string, bool) { return "", false }

func writeConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".pgcheckout.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
