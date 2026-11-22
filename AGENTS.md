# Using pgcheckout from an agent

pgcheckout gives each git branch its own PostgreSQL database, copied from a protected base database.

## Contract

- stdout: exactly one JSON object. stderr on failure: `{"error":{"code","message","hint"}}`.
- Exit codes: 0 success, 1 runtime error, 2 usage error.
- Never prompts. Never reads stdin.
- `ensure` is idempotent: the same branch always maps to the same database.
- Required configuration: `PGCHECKOUT_ADMIN_URL` and `PGCHECKOUT_BASE` (or `--admin-url`, `--base`, `.pgcheckout.json`).

## Typical flow

```bash
pgcheckout doctor
```

Read `problems`. An empty array means the server, role and base are ready.

```bash
pgcheckout ensure
```

Use the `url` field as the connection string for the current git branch. Pass a name to target another branch: `pgcheckout ensure feat/x`.

```bash
pgcheckout reset
```

Drops and recreates the current branch's database from the base. Add `--force` if sessions are still connected.

```bash
pgcheckout drop feat/x
```

## Fields worth checking

- `created`: false means the database already existed and was left untouched.
- `stale`: true means the base has changed since this branch was created. Run `pgcheckout reset` to pick up the new base; pgcheckout never recreates a branch on its own.
- `source`: `existing`, `pool` or `copy`.
- `refill_requested`: true means pgcheckout will try to start a background pool refill after printing. A failed start writes `{"warning":{"code":"refill_failed","message":"..."}}` to stderr and exits 0.
- `warnings`: per-database settings or grants that could not be replicated from the base.

## Error codes

| Code | Meaning | What to do |
|---|---|---|
| `config_missing` | admin URL or base not set | set `PGCHECKOUT_ADMIN_URL` / `PGCHECKOUT_BASE` |
| `base_not_found`, `base_not_marked` | no usable base | `pgcheckout base create <name> --from <db>` |
| `base_open` | the base is being edited | wait, or run `pgcheckout base close` if you opened it |
| `database_in_use` | sessions are connected | close them, or retry with `--force` / `--terminate` |
| `database_exists` | the destination database already exists | choose an unused database name |
| `server_resources` | PostgreSQL lacks resources to complete the operation | check the server message and free the required resources |
| `canceled` | the operation was canceled | check the cancellation or timeout before retrying |
| `unmanaged_database` | a database with that name exists but pgcheckout did not create it | do not drop it; report to the user |
| `unmanaged_file` | the env file or hook exists and pgcheckout did not write it | move the file or choose another path |
| `detached_head`, `not_git_repo` | no branch name available | pass the branch name explicitly |

## Rules

- Do not run `DROP DATABASE` yourself on databases named `<base>__...`. Use `pgcheckout drop`, `pgcheckout gc` or `pgcheckout pool drain`.
- Do not connect to the base database. To change it: `pgcheckout base open`, apply the change, `pgcheckout base close`.
- On the command line, the `url` field contains the password from the admin URL. Do not log it. MCP tool results omit the password.

## MCP server

Start the stdio server with `pgcheckout mcp`.

| Tool | Operation |
|---|---|
| `doctor` | Check PostgreSQL, the role, and the base. |
| `list` | List the base, branches, pool, temporary copies, and unmanaged databases. |
| `pool_status` | Inspect pooled copies and readiness. |
| `dsn` | Get an existing branch database's connection URL. |
| `ensure` | Get or create a branch database idempotently. |
| `reset` | Delete and recreate a named branch database. |
| `drop` | Delete a named branch database. |
| `gc` | Delete expired and obsolete databases, with a dry-run option. |

- Tool results remove passwords from `url` and report `password_redacted`. Use `env_file` with `ensure` or `reset` to write the full URL to a managed file.
- `reset` and `drop` require an explicit `branch`. `ensure` and `dsn` default to the server's current Git branch and return the resolved name.
- Failures, including missing or invalid configuration, return `isError: true` with the CLI error JSON in a text block. The server stays available for further calls.
