//go:build unix

package cli

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/toshi0607/pgcheckout/internal/config"
)

const (
	helperEnv    = "PGCHECKOUT_CLI_TEST_HELPER"
	helperPIDEnv = "PGCHECKOUT_CLI_TEST_PID_FILE"
)

func TestMain(m *testing.M) {
	switch os.Getenv(helperEnv) {
	case "mcp":
		os.Exit(Run(context.Background(), []string{"mcp"}, os.Stdout, os.Stderr, "test"))
	case "refill":
		path := os.Getenv(helperPIDEnv)
		// Rename so the parent never reads a partially written PID.
		if err := os.WriteFile(path+".tmp", []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			os.Exit(3)
		}
		if err := os.Rename(path+".tmp", path); err != nil {
			os.Exit(3)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestMCPSurvivesClosedStdout(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable)
	cmd.Env = append(os.Environ(), helperEnv+"=mcp")
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := read.Close(); err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = write
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	const request = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}` + "\n"
	// Keep stdin open: closing it first lets the server exit before it writes the response.
	// The child can then exit on its own only after writing to the closed pipe.
	if _, err := stdin.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		t.Fatalf("mcp did not exit within 30 s: %v", err)
	}
	if status := cmd.ProcessState.Sys().(syscall.WaitStatus); status.Signaled() {
		t.Fatalf("mcp was killed by %v after its output closed; stderr: %s", status.Signal(), &stderr)
	}
}

func TestMCPRefillIsReaped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "refill.pid")
	t.Setenv(helperEnv, "refill")
	t.Setenv(helperPIDEnv, path)
	cfg := config.Config{AdminURL: &url.URL{Scheme: "postgres", Host: "127.0.0.1", Path: "/postgres"}, Base: "app"}
	if requested, warning := startMCPRefill(cfg); !requested || warning != "" {
		t.Fatalf("startMCPRefill = %t, %q, want a started refill", requested, warning)
	}
	var pid int
	waitFor(t, "refill child to write its PID", func() bool {
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			return false
		}
		if err != nil {
			t.Fatal(err)
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			t.Fatal(err)
		}
		return true
	})
	// A zombie still accepts signal 0; only a reaped process reports ESRCH.
	waitFor(t, "refill child to be reaped", func() bool {
		err := syscall.Kill(pid, 0)
		if err != nil && !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("probe refill child %d: %v", pid, err)
		}
		return errors.Is(err, syscall.ESRCH)
	})
}

func waitFor(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
