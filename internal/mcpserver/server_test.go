package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/toshi0607/pgcheckout/internal/branch"
	"github.com/toshi0607/pgcheckout/internal/config"
	"github.com/toshi0607/pgcheckout/internal/gitx"
)

func startClient(t *testing.T, server Server) *mcp.ClientSession {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- server.run(ctx, serverTransport) }()
	t.Cleanup(func() {
		cancel()
		waitServer(t, done)
	})
	client := connectClient(t, clientTransport)
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error("close client:", err)
		}
	})
	return client
}

func connectClient(t *testing.T, transport mcp.Transport) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "pgcheckout-test", Version: "test"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatal("connect MCP client:", err)
	}
	return session
}

func waitServer(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Error("run MCP server:", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("MCP server did not stop")
	}
}

func callTool(t *testing.T, client *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s RPC: %v", name, err)
	}
	return result
}

func textContent(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if len(result.Content) != 1 {
		t.Fatalf("expected one content block, got %d", len(result.Content))
	}
	content, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content has type %T, want text", result.Content[0])
	}
	return content.Text
}

func expectToolError(t *testing.T, result *mcp.CallToolResult, code string) *config.Error {
	t.Helper()
	if !result.IsError || result.StructuredContent != nil {
		t.Fatal("expected a tool error with text only")
	}
	var payload config.ErrorResponse
	if err := json.Unmarshal([]byte(textContent(t, result)), &payload); err != nil {
		t.Fatal("decode tool error:", err)
	}
	if payload.Error == nil || payload.Error.Code != code {
		t.Fatalf("tool error = %+v, want code %q", payload.Error, code)
	}
	return payload.Error
}

func TestToolList(t *testing.T) {
	client := startClient(t, Server{Version: "test-version"})
	info := client.InitializeResult()
	if info.ServerInfo.Name != "pgcheckout" || info.ServerInfo.Title != "pgcheckout" || info.ServerInfo.Version != "test-version" ||
		!strings.Contains(info.Instructions, "env_file") {
		t.Fatalf("unexpected MCP server identity or instructions: %+v", info)
	}
	result, err := client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{"doctor", "list", "pool_status", "dsn", "ensure", "reset", "drop", "gc"}
	if len(result.Tools) != len(names) || result.NextCursor != "" {
		t.Fatalf("expected one page with %d tools, got %d tools and cursor %q", len(names), len(result.Tools), result.NextCursor)
	}
	for i, tool := range result.Tools {
		if tool.Name != names[i] || tool.Title == "" || tool.Description == "" {
			t.Errorf("tool %d = %q with title %q and description %q", i, tool.Name, tool.Title, tool.Description)
		}
		input, inputOK := tool.InputSchema.(map[string]any)
		output, outputOK := tool.OutputSchema.(map[string]any)
		if !inputOK || input["type"] != "object" || !outputOK || output["type"] != "object" {
			t.Errorf("%s has invalid input or output schema", tool.Name)
		}
		required, _ := input["required"].([]any)
		if slices.Contains(required, any("branch")) != (tool.Name == "reset" || tool.Name == "drop") {
			t.Errorf("%s has unexpected required fields %v", tool.Name, required)
		}
		hints := tool.Annotations
		if hints == nil || hints.OpenWorldHint == nil || *hints.OpenWorldHint ||
			hints.ReadOnlyHint != (i < 4) || hints.IdempotentHint != (tool.Name == "ensure") {
			t.Errorf("%s has unexpected annotations %+v", tool.Name, hints)
		}
		if i >= 4 && (hints == nil || hints.DestructiveHint == nil || *hints.DestructiveHint != (i > 4)) {
			t.Errorf("%s has unexpected destructive hint", tool.Name)
		}
	}
}

func TestToolErrors(t *testing.T) {
	const password = "fixture-secret"
	const adminURL = "postgres://user:" + password + "@localhost/postgres"
	server := Server{cwd: t.TempDir(), connect: func(_ context.Context, raw string) (*pgx.Conn, error) {
		return nil, errors.New("connection failed for " + raw + " with credential " + password)
	}}
	t.Setenv("PGCHECKOUT_ADMIN_URL", "")
	t.Setenv("PGCHECKOUT_POOL_SIZE", "0")
	t.Setenv("PGCHECKOUT_STRATEGY", "auto")
	client := startClient(t, server)
	expectToolError(t, callTool(t, client, "doctor", nil), config.ConfigMissing)
	expectToolError(t, callTool(t, client, "ensure", nil), config.ConfigMissing)
	for _, tool := range []string{"reset", "drop"} {
		expectToolError(t, callTool(t, client, tool, nil), config.Usage)
		expectToolError(t, callTool(t, client, tool, map[string]any{"branch": ""}), config.Usage)
	}
	expectToolError(t, callTool(t, client, "ensure", map[string]any{"branch": ""}), config.Usage)

	_, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "unknown"})
	if rpcErr, ok := errors.AsType[*jsonrpc.Error](err); !ok || rpcErr.Code != jsonrpc.CodeInvalidParams {
		t.Errorf("unknown tool RPC error = %v, want invalid params", err)
	}
	t.Setenv("PGCHECKOUT_ADMIN_URL", "invalid")
	expectToolError(t, callTool(t, client, "doctor", nil), config.ConfigInvalid)
	t.Setenv("PGCHECKOUT_ADMIN_URL", adminURL)
	t.Setenv("PGCHECKOUT_BASE", "app")
	problem := expectToolError(t, callTool(t, client, "doctor", nil), config.ConnectFailed)
	if strings.Contains(problem.Message, password) || strings.Contains(problem.Message, adminURL) {
		t.Error("connection diagnostic exposed credentials")
	}
	expectToolError(t, callTool(t, client, "dsn", nil), config.NotGitRepo)
	if _, err := client.ListTools(t.Context(), nil); err != nil {
		t.Fatal("server stopped after tool errors:", err)
	}
}

func TestMalformedToolArguments(t *testing.T) {
	client := startClient(t, Server{cwd: t.TempDir()})
	for _, tc := range []struct {
		name, tool, args, diagnostic string
	}{
		{"wrong type", "drop", `{"force":"true"}`, `has type "string", want "boolean"`},
		{"unknown property", "doctor", `{"extra":true}`, `unexpected additional properties ["extra"]`},
		{"null property", "reset", `{"branch":null}`, `has type "null", want "string"`},
		{"null arguments", "ensure", `null`, `has type "null", want "object"`},
		{"non-object", "ensure", `["bad"]`, `has type "array", want "object"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: tc.tool, Arguments: json.RawMessage(tc.args)})
			if err != nil {
				t.Fatal(err)
			}
			problem := expectToolError(t, result, config.Usage)
			if !strings.HasPrefix(problem.Message, "invalid arguments: ") ||
				!strings.Contains(problem.Message, tc.diagnostic) || problem.Hint != "Check the tool's input schema." {
				t.Errorf("unexpected tool error: %+v", problem)
			}
		})
	}
}

func TestEnsureSettingsErrors(t *testing.T) {
	client := startClient(t, Server{cwd: t.TempDir(), Overrides: config.Overrides{
		AdminURL: new("postgres://user@localhost/postgres"), Base: new("app"), PoolSize: new("0"), Strategy: new("auto"),
	}, connect: func(context.Context, string) (*pgx.Conn, error) {
		return nil, errors.New("unexpected connection attempt")
	}})
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"bad ttl", map[string]any{"ttl": "bad"}, `ttl must be a duration such as "72h"`},
		{"zero ttl", map[string]any{"ttl": "0s"}, "ttl must be greater than zero"},
		{"negative ttl", map[string]any{"ttl": "-1s"}, "ttl must be greater than zero"},
		{"missing env_file", map[string]any{"env_key": "KEY"}, "env_key requires env_file"},
		{"empty env_file", map[string]any{"env_file": ""}, "env_file must not be empty"},
		{"invalid env_key", map[string]any{"env_file": "branch.env", "env_key": "bad-name"}, "env_key must match [A-Za-z_][A-Za-z0-9_]*"},
	} {
		tc.args["branch"] = "topic"
		for _, tool := range []string{"ensure", "reset"} {
			t.Run(tool+"/"+tc.name, func(t *testing.T) {
				problem := expectToolError(t, callTool(t, client, tool, tc.args), config.Usage)
				if problem.Message != tc.want {
					t.Errorf("message = %q, want %q", problem.Message, tc.want)
				}
			})
		}
	}
}

type branchServiceStub func(tool, name string) branch.DSNResult

func (s branchServiceStub) DSN(_ context.Context, name string) (branch.DSNResult, error) {
	return s("dsn", name), nil
}

func (s branchServiceStub) Ensure(_ context.Context, name string, _ branch.EnsureOptions) (branch.EnsureResult, error) {
	return branch.EnsureResult{DSNResult: s("ensure", name)}, nil
}

func TestDefaultBranch(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	for _, args := range [][]string{{"init", "--template="}, {"symbolic-ref", "HEAD", "refs/heads/feat/x"}} {
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = cwd
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	for _, tool := range []string{"ensure", "dsn"} {
		t.Run(tool, func(t *testing.T) {
			client := startClient(t, Server{cwd: cwd, Overrides: config.Overrides{
				AdminURL: new("postgres://user@localhost/postgres"), Base: new("app"), PoolSize: new("0"), Strategy: new("auto"),
			}, branches: branchServiceStub(func(operation, name string) branch.DSNResult {
				if operation != tool || name != "feat/x" {
					t.Errorf("service operation %s(%q), want %s(%q)", operation, name, tool, "feat/x")
				}
				return branch.DSNResult{Branch: name, Database: "app__feat_x", URL: "postgres://user@localhost/app__feat_x"}
			})})
			result := callTool(t, client, tool, nil)
			if result.IsError {
				t.Fatal(textContent(t, result))
			}
			fields, ok := result.StructuredContent.(map[string]any)
			if !ok || fields["branch"] != "feat/x" {
				t.Fatalf("unexpected structured result: %v", result.StructuredContent)
			}
		})
	}
}

func TestRedactURL(t *testing.T) {
	for _, tc := range []struct {
		name, raw, want string
		redacted        bool
	}{
		{"password", "postgres://user:secret@host:5432/db?z=1&a=2", "postgres://user@host:5432/db?z=1&a=2", true},
		{"no password", "postgres://user@host/db?sslmode=disable", "postgres://user@host/db?sslmode=disable", false},
		{"escaped credentials", "postgresql://me%40work:p%40ss%3A%24%2F@[::1]:5432/db%2Fname?host=%2Ftmp", "postgresql://me%40work@[::1]:5432/db%2Fname?host=%2Ftmp", true},
		{"empty password", "postgres://user:@host/db", "postgres://user@host/db", true},
		{"query password", "postgres://user@host/db?sslmode=require&%70assword=a%26b&sslpassword=p&connect_timeout=5", "postgres://user@host/db?sslmode=require&connect_timeout=5", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actual, redacted, err := redactURL(tc.raw)
			if err != nil || actual != tc.want || redacted != tc.redacted {
				t.Errorf("redactURL = (%q, %t, %v), want (%q, %t, nil)", actual, redacted, err, tc.want, tc.redacted)
			}
		})
	}
}

func TestFinishEnsure(t *testing.T) {
	cwd := t.TempDir()
	admin, err := config.ParseAdminURL("postgres://user:special%40password@host/postgres")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{AdminURL: admin}
	env := gitx.EnvOptions{Path: new("branch.env")}
	wantFile := filepath.Join(cwd, "branch.env")
	fullURL := cfg.DatabaseURL("app__topic")
	refills := 0
	call := toolCall{cwd: cwd, svc: branch.Service{Config: cfg}, refill: func(config.Config) (bool, string) {
		refills++
		data, err := os.ReadFile(wantFile)
		if err != nil || !strings.Contains(string(data), "DATABASE_URL="+fullURL+"\n") {
			t.Error("expected the full branch URL in the env file before requesting refill")
		}
		return true, "could not start detached pool refill"
	}}
	result := branch.EnsureResult{
		DSNResult: branch.DSNResult{Branch: "topic", Database: "app__topic", URL: fullURL},
		Created:   true, RefillNeeded: true,
	}
	out, err := call.finishEnsure(result, env)
	if err != nil {
		t.Fatal(err)
	}
	if refills != 1 || !out.RefillRequested || len(out.Warnings) != 1 || !out.PasswordRedacted ||
		out.URL != "postgres://user@host/app__topic" || out.EnvFile != wantFile || out.EnvFileChanged == nil || !*out.EnvFileChanged {
		t.Fatal("unexpected ensure response")
	}
	if err := os.WriteFile(wantFile, []byte("unmanaged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, created := range []bool{true, false} {
		result.Created = created
		_, err = call.finishEnsure(result, env)
		problem, ok := errors.AsType[*config.Error](err)
		status := "already exists"
		if created {
			status = "was created"
		}
		if !ok || problem.Code != config.UnmanagedFile || !strings.Contains(problem.Hint, status) {
			t.Fatalf("expected an unmanaged-file error with a %q hint, got %v", status, err)
		}
	}
	if refills != 1 {
		t.Error("requested a refill after the env file write failed")
	}
}

func TestHandlerCancellation(t *testing.T) {
	for _, mode := range []string{"shutdown", "EOF", "request"} {
		t.Run(mode, func(t *testing.T) {
			started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			defer close(release)

			server := Server{cwd: t.TempDir(), Overrides: config.Overrides{
				AdminURL: new("postgres://user@localhost/postgres"), PoolSize: new("0"), Strategy: new("auto"),
			}, connect: func(ctx context.Context, _ string) (*pgx.Conn, error) {
				close(started)
				<-ctx.Done()
				close(canceled)
				<-release
				return nil, ctx.Err()
			}}

			ctx, cancelServer := context.WithCancel(t.Context())
			defer cancelServer()
			done := make(chan error, 1)
			var clientTransport mcp.Transport
			stop := cancelServer
			if mode != "request" {
				stdin, clientOutput := io.Pipe()
				clientInput, stdout := io.Pipe()
				defer func() {
					if err := errors.Join(clientInput.Close(), clientOutput.Close()); err != nil {
						t.Error("close client pipes:", err)
					}
				}()
				clientTransport = &mcp.IOTransport{Reader: clientInput, Writer: clientOutput}
				if mode == "EOF" {
					stop = func() {
						if err := clientOutput.Close(); err != nil {
							t.Error("close server stdin:", err)
						}
					}
				}
				go func() { done <- server.RunStdio(ctx, stdin, stdout) }()
			} else {
				serverEndpoint, clientEndpoint := mcp.NewInMemoryTransports()
				clientTransport = clientEndpoint
				go func() { done <- server.run(ctx, serverEndpoint) }()
			}
			client := connectClient(t, clientTransport)
			callCtx, cancelCall := context.WithCancel(t.Context())
			defer cancelCall()
			callSucceeded := make(chan bool, 1)
			go func() {
				result, err := client.CallTool(callCtx, &mcp.CallToolParams{Name: "doctor"})
				callSucceeded <- err == nil && !result.IsError
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("handler did not start")
			}
			if mode == "request" {
				cancelCall()
			} else {
				stop()
			}
			select {
			case <-canceled:
			case <-time.After(5 * time.Second):
				t.Fatal("handler was not canceled")
			}
			select {
			case err := <-done:
				t.Fatalf("server returned before its handler finished: %v", err)
			case <-time.After(25 * time.Millisecond):
			}
			// Unblock cleanup before awaiting shutdown or making the next request.
			release <- struct{}{}
			if mode == "request" {
				if _, err := client.ListTools(t.Context(), nil); err != nil {
					t.Fatal("request cancellation stopped the server:", err)
				}
				if err := client.Close(); err != nil {
					t.Error("close client:", err)
				}
			}
			waitServer(t, done)
			cancelCall()
			select {
			case succeeded := <-callSucceeded:
				if succeeded {
					t.Error("canceled tool call succeeded")
				}
			case <-time.After(5 * time.Second):
				t.Error("canceled tool call did not return")
			}
		})
	}
}

func TestToolOutputSchema(t *testing.T) {
	// Doctor can report a nil base, and required empty slices must remain arrays.
	out := branch.DoctorResult{Unmanaged: nonNil([]string(nil)), Problems: nonNil([]config.Error(nil))}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	if err := resolveSchema(inferSchema[branch.DoctorResult]()).Validate(value); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"problems", "unmanaged"} {
		if !reflect.DeepEqual(value[field], []any{}) {
			t.Errorf("%s = %v, want an empty array", field, value[field])
		}
	}
}
