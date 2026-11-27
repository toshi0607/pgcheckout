# pgcheckout

English | [日本語](README.ja.md)

pgcheckout is a CLI that copies a development database inside a PostgreSQL-compatible server and assigns one copy to each git branch.
Copies are made ahead of time and renamed when requested, so a branch database is available in tens of milliseconds.

## Supported environments

pgcheckout works with any PostgreSQL-compatible server reachable by a connection string.
Integration tests run against PostgreSQL 18.7 and AlloyDB Omni 17.10. They also pass on managed AlloyDB 18.4.
The only privilege required on the server is `CREATEDB`.

Copy speed depends on the server version and where the data is stored.
The table below shows values measured on a 1 GB database (1–4 measurements each).

| Environment | Copy | Rename |
|---|---|---|
| PostgreSQL 18 (installed directly on macOS, APFS) | 43–106 ms | 0.12–0.22 ms |
| PostgreSQL 18 (Docker, XFS volume) | 43–68 ms | 0.54 ms |
| PostgreSQL 18 (Docker, default ext4) | 3.07 s | not measured |
| AlloyDB Omni 17 (Docker, 493 MB) | 0.95–1.02 s | 0.8–3.1 ms |
| Managed AlloyDB 18.4 (8 vCPU, 1,029 MB) | 2.1–3.4 s | 15 ms |
| Managed AlloyDB 18.4 (8 vCPU, 4,706 MB) | 3.0–4.6 s | not measured |

Even in an environment where copying is slow, if the pool holds a copy, the wait is only the rename plus the connection.

## Getting started

Install it. The binary is placed in `$(go env GOPATH)/bin`, so add that directory to your `PATH`.

```bash
go install ./cmd/pgcheckout
```

Pass the maintenance database URL and the base name through environment variables.
For the maintenance database URL, specify a database that is neither the copy source nor a branch (such as `postgres`).

```bash
export PGCHECKOUT_ADMIN_URL=postgres://dev@127.0.0.1:5432/postgres
```

```bash
export PGCHECKOUT_BASE=app
```

Copy the existing development database `app_dev` to create the base `app`.
The base is stored with connections disabled, and `app_dev` remains usable as before.

```bash
pgcheckout base create app --from app_dev
```

Prepare the database for the current git branch.
No matter how many times you run it, the same branch gets the same database.

```bash
pgcheckout ensure
```

```json
{"branch":"feat/login","database":"app__feat_login_d668f008","url":"postgres://dev@127.0.0.1:5432/app__feat_login_d668f008","created":true,"source":"pool","stale":false,"gen":1,"base_gen":1,"strategy":"file_copy_clone","elapsed_ms":2,"refill_requested":true}
```

When `refill_requested: true`, pgcheckout starts a background pool refill process after it prints the result.
If the start fails, it writes `{"warning":{"code":"refill_failed","message":"..."}}` to stderr and still exits with code 0.

To try the whole workflow on a throwaway server first, run the walkthrough in [examples/todo](examples/todo/README.md).

## Follow git branch switches

Installing a post-checkout hook writes the connection URL to a file every time you switch branches.

```bash
pgcheckout hook install --env-file .env.pgcheckout
```

`.env.pgcheckout` contains a single line, `DATABASE_URL=...`.
The hook is placed in the repository's `.git/hooks`. It does not write to a shared directory that `core.hooksPath` points to.

Two conditions must hold for the hook to run. If either is missing, `git checkout` itself still succeeds, and the connection file is not rewritten.

- `pgcheckout` is on the `PATH` of the environment that runs git. If it is not, the hook exits without printing anything.
- `PGCHECKOUT_ADMIN_URL` is set in the environment that runs git. The base name may instead be written in `.pgcheckout.json`. If the setting is missing, the hook prints the failure to stderr and exits.

The file it writes contains a connection string that includes the password. Add it to `.gitignore` and make sure it is not committed.

## Update the base

To change the contents of the base, for example with a migration, open the base, make the change, and close it.
Closing the base moves it to a new generation and discards the pool built from other generations.

```bash
pgcheckout base open
```

```bash
pgcheckout base close
```

Existing branches are not recreated. Branches whose generation differs from the base are marked `stale: true` in the output of `ensure` and `list`.
To recreate one, use `pgcheckout reset`.

## Commands

| Command | Action |
|---|---|
| `ensure [branch]` | Prepares the branch database and returns its connection URL |
| `dsn [branch]` | Returns the connection URL |
| `list` | Lists the base, branches, and pool |
| `drop [branch]` | Deletes the branch. If connections remain, `--force` is required |
| `reset [branch]` | Replaces the branch with a fresh copy of the base. The old database stays until the new one is in place |
| `pool fill` / `pool status` / `pool drain` | Refills, reports the status of, and discards the pool |
| `gc` | Deletes expired branches and pools from other generations |
| `doctor` | Reports the state of the server, privileges, and base |
| `base create` / `base adopt` / `base open` / `base close` | Creates and updates the base |
| `hook install` / `hook uninstall` | The post-checkout hook |
| `mcp` | Runs as an MCP server over standard input and output |
| `help` | Returns the commands, flags, and environment variables as JSON |

If you omit the branch name, pgcheckout uses the current git branch name.

## Use as an MCP server

`pgcheckout mcp` runs as an MCP server over standard input and output.
Register it in an MCP client with the command `pgcheckout` and the argument `mcp`. Pass the connection URL and the base name through environment variables.

```json
{
  "mcpServers": {
    "pgcheckout": {
      "command": "pgcheckout",
      "args": ["mcp"],
      "env": {
        "PGCHECKOUT_ADMIN_URL": "postgres://dev@127.0.0.1:5432/postgres",
        "PGCHECKOUT_BASE": "app"
      }
    }
  }
}
```

There are eight tools: `doctor`, `list`, `pool_status`, `dsn`, `ensure`, `reset`, `drop`, and `gc`.
Base operations, `pool fill`, `pool drain`, and hook operations are not exposed as tools.

There are three differences from running them as commands.

- The `url` in a result does not include the password, because tool results remain in the conversation record. To write a connection URL that includes the password, use `env_file` of `ensure` and `reset` to write it to a file.
- `reset` and `drop` always require a branch name. `ensure` and `dsn` can omit it, in which case they use the current branch in the server's working directory.
- A failure is returned as a tool result with `isError` set. The body is the same `{"error":{"code","message","hint"}}` as for commands.

The server supports both the conventional flow that starts with `initialize` and the 2026-07-28 flow that uses `server/discover`.

## Configuration

| Environment variable | Flag | Meaning |
|---|---|---|
| `PGCHECKOUT_ADMIN_URL` | `--admin-url` | Connection string for the maintenance database |
| `PGCHECKOUT_BASE` | `--base` | Name of the base |
| `PGCHECKOUT_POOL_SIZE` | `--pool-size` | Target pool size (default 2, 0 disables) |
| `PGCHECKOUT_STRATEGY` | `--strategy` | Copy strategy (default `auto`) |

Values that are not secret can also be written in the repository's `.pgcheckout.json`.

```json
{"base": "app", "pool_size": 2}
```

`DATABASE_URL` is not read. This avoids running maintenance SQL against an application's connection URL by mistake.

## Output

Standard output receives exactly one JSON object.
Errors are written to standard error in the form `{"error":{"code":"...","message":"...","hint":"..."}}`, and the exit code is 1 (2 for a usage error).
pgcheckout never prompts for input.

## Scope of deletion

pgcheckout deletes only branches, pools, and half-copied databases that carry a pgcheckout marker in the database comment and belong to the specified base.
It does not delete the base or the maintenance database.
A database whose name follows the naming rule but has no marker is only reported by `list` and `doctor` as `unmanaged`.

## Limitations

- A branch is a separate database on the same server, and shares CPU, memory, roles, and the server version.
- Merging and diffing between branches are not supported.
- The AlloyDB Omni Docker image only goes up to PostgreSQL 17, so copying takes time proportional to the data size.
- On managed AlloyDB 18.4, `file_copy_method` accepts only `copy`, so pgcheckout copies with plain `FILE_COPY`, and `doctor` reports `file_copy`. A copy takes about 2 seconds plus a part that grows with the data size.
- On managed AlloyDB 18.4, keep the default strategy. With `--strategy wal_log`, a copy of 1 GB took 29–40 s when the template had not been read into shared buffers, for example a base that was itself made with `FILE_COPY`, and about 2 s once it had. `FILE_COPY` took about 2.5 s in both cases.
- Everyone who uses the same base must specify the same maintenance database. The exclusive locks are independent for each maintenance database.
- A base name cannot contain `__` and cannot end with `_`.
- The connection string contains the password. Handle output and files with care.

## Tests

```bash
go test ./...
```

Run the integration tests by passing the connection URL of a role that has `CREATEDB`.

```bash
PGCHECKOUT_TEST_ADMIN_URL=postgres://dev@127.0.0.1:5432/postgres go test -tags integration ./...
```

The design is in [DESIGN.md](DESIGN.md).
