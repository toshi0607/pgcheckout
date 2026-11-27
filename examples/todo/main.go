// Command todo is a tiny todo list used to demonstrate pgcheckout.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const usageText = `Usage: todo [flags] <command> [args]

Commands:
  migrate              Apply every *.sql file in --dir in file-name order.
  add TITLE            Add a todo.
  list                 Print all todos as tab-separated lines with a header.
  createdb NAME        Create a database.
  dropdb NAME          Drop a database if it exists.
  databases PREFIX     Print the names of databases that start with PREFIX.

Flags (before or after the command):
  --url URL            Connection URL.
  --db NAME            Replace the database name in the connection URL.
  --dir DIR            Migrations directory for migrate (default "migrations").

The connection URL is the first of: --url, the DATABASE_URL environment
variable, a DATABASE_URL=... line in ./.env.pgcheckout.
`

const envFile = ".env.pgcheckout"

type options struct {
	url, db, dir string
	args         []string
}

func main() {
	opts, err := parseArgs(os.Args[1:])
	if errors.Is(err, errHelp) {
		fmt.Print(usageText)
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "todo: %v\n\n%s", err, usageText)
		os.Exit(1)
	}
	if err := run(context.Background(), opts, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "todo: %v\n", err)
		os.Exit(1)
	}
}

var errHelp = errors.New("help requested")

func parseArgs(args []string) (options, error) {
	opts := options{dir: "migrations"}
	for len(args) > 0 {
		arg := args[0]
		args = args[1:]
		if arg == "--" {
			opts.args = append(opts.args, args...)
			break
		}
		if arg == "-h" || arg == "--help" {
			return opts, errHelp
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			opts.args = append(opts.args, arg)
			continue
		}
		name, value, attached := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		var target *string
		switch name {
		case "url":
			target = &opts.url
		case "db":
			target = &opts.db
		case "dir":
			target = &opts.dir
		default:
			return opts, fmt.Errorf("unknown flag %q", "--"+name)
		}
		if !attached {
			if len(args) == 0 {
				return opts, fmt.Errorf("flag --%s requires a value", name)
			}
			value, args = args[0], args[1:]
		}
		*target = value
	}
	if len(opts.args) == 0 {
		return opts, errors.New("a command is required")
	}
	want := map[string]int{"migrate": 0, "add": 1, "list": 0, "createdb": 1, "dropdb": 1, "databases": 1}
	n, ok := want[opts.args[0]]
	switch {
	case !ok:
		return opts, fmt.Errorf("unknown command %q", opts.args[0])
	case len(opts.args)-1 != n:
		return opts, fmt.Errorf("%s takes %d argument(s)", opts.args[0], n)
	case n == 1 && opts.args[1] == "":
		return opts, fmt.Errorf("%s needs a non-empty argument", opts.args[0])
	}
	return opts, nil
}

func run(ctx context.Context, opts options, out io.Writer) error {
	raw, err := connectionURL(opts)
	if err != nil {
		return err
	}
	cfg, err := pgx.ParseConfig(raw)
	if err != nil {
		return errors.New("invalid connection URL")
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return connectError(cfg.Database, err)
	}
	defer conn.Close(context.WithoutCancel(ctx))

	cmd, args := opts.args[0], opts.args[1:]
	switch cmd {
	case "migrate":
		return migrate(ctx, conn, opts.dir, out)
	case "add":
		if _, err := conn.Exec(ctx, "INSERT INTO todos (title) VALUES ($1)", args[0]); err != nil {
			return fmt.Errorf("add: %w", err)
		}
		fmt.Fprintln(out, "added")
		return nil
	case "list":
		return list(ctx, conn, out)
	case "createdb":
		if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{args[0]}.Sanitize()); err != nil {
			return fmt.Errorf("createdb: %w", err)
		}
		fmt.Fprintf(out, "created database %s\n", args[0])
		return nil
	case "dropdb":
		if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{args[0]}.Sanitize()); err != nil {
			return fmt.Errorf("dropdb: %w", err)
		}
		fmt.Fprintf(out, "dropped database %s\n", args[0])
		return nil
	case "databases":
		return databases(ctx, conn, args[0], out)
	}
	return fmt.Errorf("unknown command %q", cmd)
}

// connectionURL picks the URL and applies --db. Errors never include the URL.
func connectionURL(opts options) (string, error) {
	raw := opts.url
	if raw == "" {
		raw = os.Getenv("DATABASE_URL")
	}
	if raw == "" {
		var err error
		raw, err = readEnvFile(envFile)
		if err != nil {
			return "", err
		}
	}
	if raw == "" {
		return "", errors.New("no connection URL: pass --url, set DATABASE_URL, or create " + envFile)
	}
	if opts.db == "" {
		return raw, nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return "", errors.New("--db needs a postgres:// or postgresql:// connection URL")
	}
	u.Path = "/" + opts.db
	u.RawPath = "/" + url.PathEscape(opts.db)
	return u.String(), nil
}

func readEnvFile(path string) (string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if value, ok := strings.CutPrefix(line, "DATABASE_URL="); ok {
			return value, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return "", nil
}

// connectError reports why a connection failed without the user name or password.
func connectError(database string, err error) error {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		return fmt.Errorf("connect to database %q: %s (SQLSTATE %s)", database, pgErr.Message, pgErr.Code)
	}
	if connErr, ok := errors.AsType[*pgconn.ConnectError](err); ok {
		if inner := connErr.Unwrap(); inner != nil {
			err = inner
		}
	}
	return fmt.Errorf("connect to database %q: %v", database, err)
}

func migrate(ctx context.Context, conn *pgx.Conn, dir string, out io.Writer) error {
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if len(files) == 0 {
		return fmt.Errorf("migrate: no *.sql files in %s", dir)
	}
	slices.Sort(files)
	if _, err := conn.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (version text PRIMARY KEY)"); err != nil {
		return fmt.Errorf("migrate: create schema_migrations: %w", err)
	}
	for _, file := range files {
		version := filepath.Base(file)
		var applied bool
		if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)", version).Scan(&applied); err != nil {
			return fmt.Errorf("migrate: check %s: %w", version, err)
		}
		if applied {
			continue
		}
		sql, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, string(sql)); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", version)
			return err
		})
		if err != nil {
			return fmt.Errorf("migrate: apply %s: %w", version, err)
		}
		fmt.Fprintf(out, "applied %s\n", version)
	}
	return nil
}

func list(ctx context.Context, conn *pgx.Conn, out io.Writer) error {
	rows, err := conn.Query(ctx, "SELECT * FROM todos ORDER BY id")
	if err != nil {
		return fmt.Errorf("list: %w", err)
	}
	defer rows.Close()
	var columns []string
	for _, field := range rows.FieldDescriptions() {
		columns = append(columns, field.Name)
	}
	fmt.Fprintln(out, strings.Join(columns, "\t"))
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return fmt.Errorf("list: %w", err)
		}
		cells := make([]string, len(values))
		for i, value := range values {
			if value != nil {
				cells[i] = fmt.Sprint(value)
			}
		}
		fmt.Fprintln(out, strings.Join(cells, "\t"))
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list: %w", err)
	}
	return nil
}

func databases(ctx context.Context, conn *pgx.Conn, prefix string, out io.Writer) error {
	rows, err := conn.Query(ctx, "SELECT datname FROM pg_database WHERE starts_with(datname, $1) ORDER BY datname", prefix)
	if err != nil {
		return fmt.Errorf("databases: %w", err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("databases: %w", err)
	}
	for _, name := range names {
		fmt.Fprintln(out, name)
	}
	return nil
}
