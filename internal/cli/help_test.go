package cli

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/toshi0607/pgcheckout/internal/config"
)

func TestHelp(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}, {"ensure", "--help"}, {"pool", "fill", "--help"}, {"base", "--help"}, {"mcp", "--help"}} {
		var stdout, stderr bytes.Buffer
		if code := Run(t.Context(), args, &stdout, &stderr, "dev"); code != 0 || stderr.Len() != 0 {
			t.Fatalf("Run(%v) = %d, stderr %q", args, code, &stderr)
		}
		var got helpResult
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Usage != "pgcheckout [global flags] <command> ..." || got.ExitCodes["2"] != "usage error" ||
			len(got.GlobalFlags) == 0 || len(got.Env) == 0 {
			t.Fatalf("incomplete help: %s", &stdout)
		}
		if len(args) == 1 {
			if len(got.Commands) < 2 {
				t.Fatalf("global help has %d commands", len(got.Commands))
			}
		} else if len(got.Commands) != 1 || got.Commands[0].Name != strings.Join(args[:len(args)-1], " ") {
			t.Fatalf("wrong scope for %v: %+v", args, got.Commands)
		}
		if args[0] == "ensure" && !slices.ContainsFunc(got.Commands[0].Flags, func(f helpFlag) bool {
			return f.Name == "--ttl" && f.Value == "duration" && f.Summary != ""
		}) {
			t.Error("ensure help is missing --ttl <duration>")
		}
	}
}

func TestMCPFormat(t *testing.T) {
	var stdout, stderr bytes.Buffer
	args := []string{"--format", "bogus", "mcp"}
	if code := Run(t.Context(), args, &stdout, &stderr, "dev"); code != 2 || stdout.Len() != 0 ||
		!strings.Contains(stderr.String(), `"code":"usage","message":"--format must be json or text"`) {
		t.Fatalf("Run(%v) = (%d, %q, %q)", args, code, &stdout, &stderr)
	}

	stderr.Reset()
	args = []string{"--format", "text", "mcp", "--help"}
	if code := Run(t.Context(), args, &stdout, &stderr, "dev"); code != 0 || stderr.Len() != 0 ||
		!strings.HasPrefix(stdout.String(), "Usage: pgcheckout [global flags] mcp\n") {
		t.Fatalf("Run(%v) = (%d, %q, %q)", args, code, &stdout, &stderr)
	}
}

func TestTextHelpAndMissingCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	args := []string{"ensure", "--help", "--format", "text"}
	if code := Run(t.Context(), args, &stdout, &stderr, "dev"); code != 0 || stderr.Len() != 0 ||
		!strings.HasPrefix(stdout.String(), "Usage: pgcheckout [global flags] ensure [branch]") ||
		!strings.Contains(stdout.String(), "--ttl <duration>") {
		t.Fatalf("Run(%v) = (%d, %q, %q)", args, code, &stdout, &stderr)
	}
	stdout.Reset()
	if code := Run(t.Context(), nil, &stdout, &stderr, "dev"); code != 2 || stdout.Len() != 0 ||
		!strings.Contains(stderr.String(), `"hint":"Run pgcheckout help."`) {
		t.Fatalf("Run without a command = (%d, %q, %q)", code, &stdout, &stderr)
	}
}

func TestHelpCoversDispatcher(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run(t.Context(), []string{"help"}, &stdout, &stderr, "dev"); code != 0 {
		t.Fatalf("help exited %d: %s", code, &stderr)
	}
	var got helpResult
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// Read accepted commands from bind so a new parser case also requires a help entry.
	source, err := parser.ParseFile(token.NewFileSet(), "args.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range source.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "bind" {
			checkHelpCases(t, got.Commands, fn.Body, "")
			return
		}
	}
	t.Fatal("command binding function not found")
}

func checkHelpCases(t *testing.T, commands []helpCommand, node ast.Node, prefix string) {
	t.Helper()
	ast.Inspect(node, func(n ast.Node) bool {
		clause, ok := n.(*ast.CaseClause)
		if !ok {
			return true
		}
		for _, label := range clause.List {
			name, err := strconv.Unquote(label.(*ast.BasicLit).Value)
			if err != nil {
				t.Fatal(err)
			}
			name = prefix + name
			if !slices.ContainsFunc(commands, func(c helpCommand) bool { return c.Name == name }) {
				t.Errorf("help is missing command %q", name)
			}
			for _, stmt := range clause.Body {
				if _, ok := stmt.(*ast.SwitchStmt); ok {
					checkHelpCases(t, commands, stmt, name+" ")
				}
			}
		}
		return false
	})
}

func TestTTLRequiresPositiveDuration(t *testing.T) {
	for _, command := range []string{"ensure", "reset"} {
		for _, ttl := range []string{"0", "-1s"} {
			if _, _, err := parse([]string{command, "--ttl", ttl}); config.ErrorCode(err) != config.Usage {
				t.Errorf("parse(%s --ttl %s) error = %v, want usage", command, ttl, err)
			}
		}
	}
}

func TestRefillStartWarning(t *testing.T) {
	var stderr bytes.Buffer
	writeRefillWarning(&stderr, "json")
	const want = `{"warning":{"code":"refill_failed","message":"could not start detached pool refill"}}` + "\n"
	if stderr.String() != want {
		t.Errorf("stderr = %q, want %q", &stderr, want)
	}
}
