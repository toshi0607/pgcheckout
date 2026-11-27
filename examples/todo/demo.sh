#!/usr/bin/env bash
# Walks through the pgcheckout workflow with the todo example and checks every step.
#
#   PGCHECKOUT_ADMIN_URL  maintenance database URL (default: the compose.yaml service)
#   DEMO_BASE             base name (default todo_demo); the development database is ${DEMO_BASE}_dev
#   KEEP=1                keep the databases and the scratch repository
set -euo pipefail

PGCHECKOUT_ADMIN_URL=${PGCHECKOUT_ADMIN_URL:-postgres://postgres:postgres@127.0.0.1:54320/postgres}
DEMO_BASE=${DEMO_BASE:-todo_demo}
DEV_DB=${DEMO_BASE}_dev
KEEP=${KEEP:-0}
export PGCHECKOUT_ADMIN_URL
# The base and pool size come from .pgcheckout.json, and todo reads .env.pgcheckout.
unset DATABASE_URL PGCHECKOUT_BASE PGCHECKOUT_POOL_SIZE
# Keep the scratch repository independent of the caller's git configuration
# (a global core.hooksPath would bypass .git/hooks/post-checkout).
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
TMP_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/pgcheckout-demo.XXXXXX")
BIN=$TMP_ROOT/bin
WORK=$TMP_ROOT/repo
CREATED_DEV=0
CREATED_BASE=0

heading() { printf '\n== %s\n' "$1"; }
note() { printf '   %s\n' "$1"; }
ok() { printf '   ok: %s\n' "$1"; }
indent() { sed 's/^/     /'; }

fail() {
	printf 'FAIL: %s\n' "$1" >&2
	if [ $# -ge 3 ]; then
		printf '  expected: %s\n  got:      %s\n' "$2" "$3" >&2
	fi
	exit 1
}

expect_eq() {
	if [ "$2" = "$3" ]; then
		ok "$1: $3"
	else
		fail "$1" "$2" "$3"
	fi
}

# json EXPR evaluates a Python expression over the JSON object on stdin, bound to d.
json() {
	python3 -c 'import json, sys
d = json.load(sys.stdin)
v = eval(sys.argv[1], {"d": d})
print(v if isinstance(v, str) else json.dumps(v))' "$1"
}

# check_todos LABEL COLUMNS ROWS [todo flags...] checks the columns and row count of todo list.
check_todos() {
	local label=$1 want_columns=$2 want_rows=$3 out columns rows
	shift 3
	out=$(todo "$@" list) || fail "$label: todo list failed"
	printf '%s\n' "$out" | indent
	columns=$(printf '%s\n' "$out" | head -n 1 | tr '\t' ',')
	rows=$(printf '%s\n' "$out" | awk 'END { print NR - 1 }')
	expect_eq "$label columns" "$want_columns" "$columns"
	expect_eq "$label rows" "$want_rows" "$rows"
}

# branch_db BRANCH prints the database pgcheckout assigned to BRANCH.
branch_db() {
	pgcheckout list | json "''.join(b['database'] for b in d['branches'] if b['branch'] == '$1')"
}

# retry_in_use CMD... runs a pgcheckout command, retrying while a just-closed
# session still counts as connected. Output goes to $TMP_ROOT/out.json.
retry_in_use() {
	local i
	for i in 1 2 3 4 5 6 7 8 9 10; do
		if "$@" >"$TMP_ROOT/out.json" 2>"$TMP_ROOT/err.json"; then
			return 0
		fi
		grep -q '"code":"database_in_use"' "$TMP_ROOT/err.json" || break
		sleep 0.5
	done
	cat "$TMP_ROOT/err.json" >&2
	return 1
}

# wait_for_refill waits until no background "pool fill" (started by ensure or
# reset) holds the fill lock. A fill with --size 0 creates nothing and reports
# "skipped" while another fill is running.
wait_for_refill() {
	local i out
	for i in $(seq 1 120); do
		if ! out=$(pgcheckout --base "$DEMO_BASE" pool fill --size 0 2>/dev/null); then
			return 0
		fi
		case $out in
		*'"skipped"'*) sleep 1 ;;
		*) return 0 ;;
		esac
	done
	return 1
}

# cleanup_databases removes only what this run created. Best effort.
cleanup_databases() {
	local name left i
	set +e
	cd "$WORK" 2>/dev/null || cd /
	if [ "$CREATED_BASE" = 1 ]; then
		wait_for_refill || note "a background pool refill is still running"
		for name in $(pgcheckout --base "$DEMO_BASE" list 2>/dev/null | json "'\n'.join(b['branch'] for b in d['branches'])" 2>/dev/null); do
			note "\$ pgcheckout drop $name --force"
			pgcheckout --base "$DEMO_BASE" drop "$name" --force >/dev/null
		done
		note "\$ pgcheckout pool drain && pgcheckout gc"
		for i in 1 2 3 4 5 6 7 8 9 10; do
			pgcheckout --base "$DEMO_BASE" pool drain >/dev/null
			pgcheckout --base "$DEMO_BASE" gc >/dev/null
			left=$(pgcheckout --base "$DEMO_BASE" list 2>/dev/null |
				json "len(d['branches']) + len(d['pool']) + len(d['tmp'])" 2>/dev/null)
			[ "$left" = 0 ] && break
			sleep 1
			wait_for_refill
		done
		note "\$ todo --url \"\$PGCHECKOUT_ADMIN_URL\" dropdb $DEMO_BASE"
		todo --url "$PGCHECKOUT_ADMIN_URL" dropdb "$DEMO_BASE" >/dev/null
		CREATED_BASE=0
	fi
	if [ "$CREATED_DEV" = 1 ]; then
		note "\$ todo --url \"\$PGCHECKOUT_ADMIN_URL\" dropdb $DEV_DB"
		todo --url "$PGCHECKOUT_ADMIN_URL" dropdb "$DEV_DB" >/dev/null
		CREATED_DEV=0
	fi
	set -e
}

remove_tmp() {
	cd /
	rm -rf "$TMP_ROOT"
}

on_exit() {
	local status=$?
	set +e
	if [ "$KEEP" = 1 ]; then
		if [ "$status" -ne 0 ]; then
			printf 'KEEP=1: kept the databases and %s\n' "$TMP_ROOT" >&2
		fi
		return
	fi
	if [ "$CREATED_BASE" = 1 ] || [ "$CREATED_DEV" = 1 ]; then
		printf '\n== cleaning up after a failure\n' >&2
		cleanup_databases >&2
	fi
	remove_tmp
}

# Setup: build the binaries, create the scratch repository, check the server.
trap remove_tmp EXIT
heading "Setup"
for tool in go git python3; do
	command -v "$tool" >/dev/null || fail "$tool is required"
done
mkdir -p "$BIN"
(cd "$ROOT" && go build -o "$BIN/pgcheckout" ./cmd/pgcheckout && go build -o "$BIN/todo" ./examples/todo)
export PATH="$BIN:$PATH"
ok "built pgcheckout and todo into a temporary directory"

mkdir "$WORK"
cp -R "$ROOT/examples/todo/migrations" "$WORK/migrations"
cp "$ROOT/examples/todo/.gitignore" "$WORK/.gitignore"
printf '{"base": "%s", "pool_size": 2}\n' "$DEMO_BASE" >"$WORK/.pgcheckout.json"
cd "$WORK"
git init -q -b main
git config user.name "pgcheckout demo"
git config user.email "demo@example.invalid"
git add -A
git commit -q -m "todo app"
ok "scratch repository at $WORK"

doctor_json=$(pgcheckout doctor) || fail "pgcheckout doctor failed; check PGCHECKOUT_ADMIN_URL and the server"
note "server $(printf '%s' "$doctor_json" | json 'd["server_version"]'), maintenance database $(printf '%s' "$doctor_json" | json 'd["maintenance_db"]')"
expect_eq "role can create databases" true "$(printf '%s' "$doctor_json" | json 'd["can_create_db"]')"
existing=$(todo --url "$PGCHECKOUT_ADMIN_URL" databases "$DEMO_BASE") || fail "could not list databases on the server"
if [ -n "$existing" ]; then
	fail "databases starting with $DEMO_BASE already exist; drop them or set DEMO_BASE" "none" "$(printf '%s' "$existing" | tr '\n' ' ')"
fi
ok "no database starting with $DEMO_BASE exists"
trap on_exit EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Step 1: the development database.
heading "1. Create the development database $DEV_DB and migrate it"
note "\$ todo --url \"\$PGCHECKOUT_ADMIN_URL\" createdb $DEV_DB"
CREATED_DEV=1
todo --url "$PGCHECKOUT_ADMIN_URL" createdb "$DEV_DB" | indent
note "\$ todo --url \"\$PGCHECKOUT_ADMIN_URL\" --db $DEV_DB migrate"
todo --url "$PGCHECKOUT_ADMIN_URL" --db "$DEV_DB" migrate | indent
check_todos "$DEV_DB" "id,title,done" 3 --url "$PGCHECKOUT_ADMIN_URL" --db "$DEV_DB"

# Step 2: the base and the pool.
heading "2. Copy $DEV_DB into the base $DEMO_BASE and fill the pool"
note "\$ pgcheckout base create $DEMO_BASE --from $DEV_DB"
CREATED_BASE=1
retry_in_use pgcheckout base create "$DEMO_BASE" --from "$DEV_DB" || fail "pgcheckout base create failed"
note "base generation $(json 'd["gen"]' <"$TMP_ROOT/out.json"), strategy $(json 'd.get("strategy", "")' <"$TMP_ROOT/out.json")"
note "\$ pgcheckout pool fill"
fill_json=$(pgcheckout pool fill)
note "created $(printf '%s' "$fill_json" | json 'd["created"]') copies"
note "\$ pgcheckout pool status"
pool_json=$(pgcheckout pool status)
expect_eq "ready pool copies" 2 "$(printf '%s' "$pool_json" | json 'd["ready"]')"

# Step 3: the hook and the database for main.
heading "3. Install the post-checkout hook and get a database for main"
note "\$ pgcheckout hook install --env-file .env.pgcheckout"
pgcheckout hook install --env-file .env.pgcheckout >/dev/null
[ -x .git/hooks/post-checkout ] || fail "the post-checkout hook was not installed"
note "\$ pgcheckout ensure --env-file .env.pgcheckout"
ensure_json=$(pgcheckout ensure --env-file .env.pgcheckout)
expect_eq "ensure branch" main "$(printf '%s' "$ensure_json" | json 'd["branch"]')"
note "database $(printf '%s' "$ensure_json" | json 'd["database"]'), source $(printf '%s' "$ensure_json" | json 'd["source"]')"
[ -f .env.pgcheckout ] || fail ".env.pgcheckout was not written"
ok ".env.pgcheckout written"
note "\$ todo list"
check_todos "main" "id,title,done" 3

# Step 4: a feature branch with its own migration.
heading "4. Create feature/priority and add a column there"
cp .env.pgcheckout "$TMP_ROOT/env.main"
note "\$ git checkout -b feature/priority"
git checkout -q -b feature/priority
if cmp -s .env.pgcheckout "$TMP_ROOT/env.main"; then
	fail "the post-checkout hook did not rewrite .env.pgcheckout" "a URL for another database" "the same content as on main"
fi
ok ".env.pgcheckout now points to $(branch_db feature/priority)"
printf 'ALTER TABLE todos ADD COLUMN priority integer NOT NULL DEFAULT 0;\n' >migrations/003_add_priority.sql
git add migrations
git commit -q -m "add priority"
note "\$ todo migrate"
todo migrate | indent
note "\$ todo add \"only on the feature branch\""
todo add "only on the feature branch" | indent
check_todos "feature/priority" "id,title,done,priority" 4

# Step 5: back to main.
heading "5. Switch back to main"
note "\$ git checkout main"
git checkout -q main
check_todos "main" "id,title,done" 3

# Step 6: the feature branch database is kept.
heading "6. Switch to feature/priority again"
note "\$ git checkout feature/priority"
git checkout -q feature/priority
check_todos "feature/priority" "id,title,done,priority" 4

# Step 7: update the base.
heading "7. Apply a new migration to the base"
note "\$ git checkout main"
git checkout -q main
printf 'ALTER TABLE todos ADD COLUMN notes text;\n' >migrations/003_add_notes.sql
git add migrations
git commit -q -m "add notes"
wait_for_refill || fail "a background pool refill did not finish"
note "\$ pgcheckout base open"
pgcheckout base open >/dev/null
note "\$ todo --url \"\$PGCHECKOUT_ADMIN_URL\" --db $DEMO_BASE migrate"
todo --url "$PGCHECKOUT_ADMIN_URL" --db "$DEMO_BASE" migrate | indent
note "\$ pgcheckout base close"
retry_in_use pgcheckout base close || fail "pgcheckout base close failed"
note "base generation $(json 'd["gen"]' <"$TMP_ROOT/out.json")"
list_json=$(pgcheckout list)
expect_eq "stale branches" '["feature/priority", "main"]' \
	"$(printf '%s' "$list_json" | json 'sorted(b["branch"] for b in d["branches"] if b["stale"])')"
check_todos "main" "id,title,done" 3

# Step 8: reset main onto the new base.
heading "8. Reset main to the new base"
note "\$ pgcheckout reset main --env-file .env.pgcheckout"
retry_in_use pgcheckout reset main --env-file .env.pgcheckout || fail "pgcheckout reset main failed"
note "database $(json 'd["database"]' <"$TMP_ROOT/out.json"), source $(json 'd["source"]' <"$TMP_ROOT/out.json")"
check_todos "main" "id,title,done,notes" 3
list_json=$(pgcheckout list)
expect_eq "fresh branches" '["main"]' \
	"$(printf '%s' "$list_json" | json 'sorted(b["branch"] for b in d["branches"] if not b["stale"])')"
expect_eq "stale branches" '["feature/priority"]' \
	"$(printf '%s' "$list_json" | json 'sorted(b["branch"] for b in d["branches"] if b["stale"])')"

# Step 9: cleanup.
heading "9. Clean up"
if [ "$KEEP" = 1 ]; then
	note "KEEP=1: kept the databases and the scratch repository at $WORK"
else
	cleanup_databases
	remaining=$(todo --url "$PGCHECKOUT_ADMIN_URL" databases "$DEMO_BASE") || fail "could not list databases on the server"
	expect_eq "databases starting with $DEMO_BASE" "" "$(printf '%s' "$remaining" | tr '\n' ' ')"
fi

printf 'demo passed\n'
