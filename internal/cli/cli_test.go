package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toshi0607/pgcheckout/internal/branch"
	"github.com/toshi0607/pgcheckout/internal/config"
	"github.com/toshi0607/pgcheckout/internal/gitx"
)

func TestErrorOutput(t *testing.T) {
	for _, tc := range []struct {
		format string
		err    error
		code   int
		want   string
	}{
		{"json", usage("unknown command"), 2, `{"error":{"code":"usage","message":"unknown command"}}` + "\n"},
		{"json", config.NewError(config.DetachedHead, "Git HEAD is detached", "Pass a branch name explicitly."), 1,
			`{"error":{"code":"detached_head","message":"Git HEAD is detached","hint":"Pass a branch name explicitly."}}` + "\n"},
		{"text", config.NewError(config.DetachedHead, "Git HEAD is detached", "Pass a branch name explicitly."), 1,
			"error: Git HEAD is detached\nhint: Pass a branch name explicitly.\n"},
		{"json", fmt.Errorf("secret DSN"), 1, `{"error":{"code":"internal","message":"could not complete the operation"}}` + "\n"},
	} {
		var out bytes.Buffer
		if code := writeError(&out, tc.format, fmt.Errorf("outer: %w", tc.err)); code != tc.code || out.String() != tc.want {
			t.Errorf("writeError(%q) = %d, %q, want %d, %q", tc.format, code, out.String(), tc.code, tc.want)
		}
	}
}

func TestInterspersedFlags(t *testing.T) {
	got, format, err := parse([]string{"--base", "app", "ensure", "feat/x", "--ttl", "1h", "--pool-size=0", "--no-refill", "--format", "text"})
	if err != nil {
		t.Fatal(err)
	}
	if format != "text" || got.command != "ensure" || len(got.pos) != 1 || got.pos[0] != "feat/x" || !got.noRefill ||
		got.ttl == nil || *got.ttl != time.Hour || got.overrides.Base == nil || *got.overrides.Base != "app" ||
		got.overrides.PoolSize == nil || *got.overrides.PoolSize != "0" {
		t.Errorf("unexpected parsed arguments: %+v, %s", got, format)
	}
	for _, tc := range []struct {
		args []string
		size *int
	}{
		{[]string{"pool", "fill"}, nil},
		{[]string{"pool", "--base", "app", "fill", "--size", "0"}, new(0)},
	} {
		got, _, err := parse(tc.args)
		if err != nil || (got.size == nil) != (tc.size == nil) || (tc.size != nil && *got.size != *tc.size) {
			t.Errorf("parse(%v) = %+v, %v", tc.args, got, err)
		}
	}
}

func TestVersion(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		code   int
		stdout string
		stderr string
	}{
		{[]string{"version"}, 0, "{\"version\":\"dev\"}\n", ""},
		{[]string{"version", "--format", "text"}, 0, "version\t\"dev\"\n", ""},
	} {
		var stdout, stderr bytes.Buffer
		if code := Run(t.Context(), tc.args, &stdout, &stderr, "dev"); code != tc.code || stdout.String() != tc.stdout || stderr.String() != tc.stderr {
			t.Errorf("Run(%v) = (%d, %q, %q)", tc.args, code, stdout.String(), stderr.String())
		}
	}
}

func TestEnvFlags(t *testing.T) {
	for _, cmd := range [][]string{{"ensure", "topic"}, {"reset", "topic"}, {"hook", "install"}} {
		t.Run(strings.Join(cmd, " "), func(t *testing.T) {
			for _, flags := range [][]string{
				{"--env-key", "BRANCH_URL"},
				{"--env-file", ".env", "--env-key=bad-name"},
				{"--env-file", ".env", "--env-key="},
				{"--env-file="},
				{"--env-file"},
			} {
				if _, _, err := parse(append(cmd, flags...)); config.ErrorCode(err) != config.Usage {
					t.Errorf("parse(%v) = %v, want usage", flags, err)
				}
			}
			got, format, err := parse(append(cmd, "--env-file", "--base", "--env-key", "_BRANCH_2", "--format", "text"))
			if err != nil || format != "text" || got.env.Path == nil || *got.env.Path != "--base" ||
				got.env.Key == nil || *got.env.Key != "_BRANCH_2" || got.overrides.Base != nil {
				t.Errorf("unexpected parsed arguments: %+v, %s, %v", got, format, err)
			}
		})
	}
}

func TestHookCommands(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("PGCHECKOUT_ADMIN_URL", "invalid")
	t.Setenv("PGCHECKOUT_BASE", "")
	t.Chdir(t.TempDir())
	cmd := exec.CommandContext(t.Context(), "git", "init", "--template=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cwd, ".git", "hooks", "post-checkout")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"hook", "install", "--env-file", ".env", "--env-key", "_BRANCH_URL"},
			fmt.Sprintf("{\"hook\":%q,\"installed\":true,\"changed\":true}\n", path)},
		{[]string{"hook", "uninstall", "--format", "text"}, fmt.Sprintf("hook\t%q\nremoved\ttrue\n", path)},
	} {
		var stdout, stderr bytes.Buffer
		if code := Run(t.Context(), tc.args, &stdout, &stderr, "dev"); code != 0 || stdout.String() != tc.want || stderr.Len() != 0 {
			t.Errorf("Run(%v) = (%d, %q, %q), want (0, %q, empty stderr)", tc.args, code, stdout.String(), stderr.String(), tc.want)
		}
	}
}

func TestPrepareEnsureResult(t *testing.T) {
	ensured := branch.EnsureResult{DSNResult: branch.DSNResult{Database: "app__topic", URL: "postgres://localhost/app__topic"}}
	for _, tc := range []struct {
		name    string
		args    arguments
		changed *bool
	}{
		{"no env file", arguments{}, nil},
		{"new env file for existing database", arguments{env: gitx.EnvOptions{Path: new(".env")}}, new(true)},
		{"unchanged env file", arguments{env: gitx.EnvOptions{Path: new(".env")}}, new(false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.changed != nil && !*tc.changed {
				if _, err := tc.args.env.Write(dir, ensured.URL); err != nil {
					t.Fatal(err)
				}
			}
			result, refill, err := tc.args.prepareEnsureResult(dir, config.Config{}, ensured)
			if err != nil || refill != nil {
				t.Fatalf("prepareEnsureResult: %v, refill = %v", err, refill)
			}
			var out bytes.Buffer
			if err := writeResult(&out, "json", result); err != nil {
				t.Fatal(err)
			}
			var got struct {
				URL     string `json:"url"`
				EnvFile string `json:"env_file"`
				Changed *bool  `json:"env_file_changed"`
			}
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.URL != ensured.URL ||
				(tc.changed == nil && (got.EnvFile != "" || got.Changed != nil)) ||
				(tc.changed != nil && (got.EnvFile != filepath.Join(dir, ".env") || got.Changed == nil || *got.Changed != *tc.changed)) {
				t.Errorf("unexpected JSON result: %s", &out)
			}
		})
	}
}

func TestEnsureEnvFailureIncludesDatabaseAndSkipsRefill(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("KEEP=yes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := arguments{env: gitx.EnvOptions{Path: &path}}
	ensured := branch.EnsureResult{
		DSNResult: branch.DSNResult{Database: "app__topic", URL: "postgres://localhost/app__topic"},
		Created:   true, RefillNeeded: true,
	}
	cfg := config.Config{}
	var err error
	cfg.AdminURL, err = config.ParseAdminURL("postgres://localhost/postgres")
	if err != nil {
		t.Fatal(err)
	}
	result, refill, err := a.prepareEnsureResult(dir, cfg, ensured)
	if result != nil || refill != nil || config.ErrorCode(err) != config.UnmanagedFile {
		t.Fatalf("prepareEnsureResult = %+v, %v, %v", result, refill, err)
	}
	var stderr bytes.Buffer
	if code := writeError(&stderr, "text", err); code != 1 ||
		!strings.Contains(stderr.String(), `The branch database "app__topic" was created.`) ||
		!strings.Contains(stderr.String(), path) {
		t.Errorf("writeError = %d, %q, want database name and path", code, &stderr)
	}
}
