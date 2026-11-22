package cli

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/toshi0607/pgtwig/internal/config"
)

func TestErrorOutput(t *testing.T) {
	for _, tc := range []struct {
		format string
		err    error
		code   int
		want   string
	}{
		{"json", usage("not implemented"), 2, `{"error":{"code":"usage","message":"not implemented"}}` + "\n"},
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

func TestVersionAndHook(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		code   int
		stdout string
		stderr string
	}{
		{[]string{"version"}, 0, "{\"version\":\"dev\"}\n", ""},
		{[]string{"version", "--format", "text"}, 0, "version\t\"dev\"\n", ""},
		{[]string{"hook", "install"}, 2, "", "{\"error\":{\"code\":\"usage\",\"message\":\"not implemented\"}}\n"},
	} {
		var stdout, stderr bytes.Buffer
		if code := Run(t.Context(), tc.args, &stdout, &stderr, "dev"); code != tc.code || stdout.String() != tc.stdout || stderr.String() != tc.stderr {
			t.Errorf("Run(%v) = (%d, %q, %q)", tc.args, code, stdout.String(), stderr.String())
		}
	}
}
