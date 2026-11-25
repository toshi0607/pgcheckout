# pgcheckout design

English | [日本語](DESIGN.ja.md)

pgcheckout is a CLI that copies a development database inside a PostgreSQL-compatible server and assigns 1 copy to each git branch.
It targets only PostgreSQL-compatible servers reachable by connection string (a local PostgreSQL, AlloyDB Omni, managed AlloyDB, and so on).
Products that cannot copy a database inside the server, such as MySQL, are out of scope.

## Terms

| Term | Meaning |
|---|---|
| Base | The database that copies are made from. It is protected by forbidding connections |
| Branch | A copy of the base that developers and agents connect to and use |
| Pool | Databases copied from the base in advance that are not yet assigned to anyone |
| Generation | An integer that increases each time the contents of the base change. It records which state of the base a copy was made from |
| Maintenance database | The database pgcheckout connects to in order to run management SQL. It is separate from the base |
| Marker | JSON written in a database comment that shows pgcheckout manages the database |

## Copy strategy

Copies use `CREATE DATABASE ... TEMPLATE`.
The strategy is chosen from the server version, and the chosen strategy is included in the output.

| Server | Strategy | Reason |
|---|---|---|
| 18 or later | `STRATEGY=FILE_COPY` with `file_copy_method=clone` set | On a filesystem that can share blocks, copy time no longer grows with size |
| 15-17 | `STRATEGY=WAL_LOG` | `FILE_COPY` adds checkpoints before and after, and was slow in some runs on AlloyDB Omni 17 |
| 14 or earlier | not specified | There is no `STRATEGY` clause |

On a server where setting `file_copy_method` fails, the copy proceeds with `FILE_COPY` as is.
The strategy can be overridden with `PGCHECKOUT_STRATEGY`.

`CREATE DATABASE ... TEMPLATE` does not copy the following 3 things (confirmed on PostgreSQL 18.7).

- Per-database settings (`ALTER DATABASE ... SET`)
- Privileges on the database (`GRANT ... ON DATABASE`)
- The database comment

Right after copying, pgcheckout reapplies the base's settings and privileges to the destination.
Comments are not copied, so the base's marker never carries over to a branch.

## Markers and the scope of destructive operations

A marker is JSON of the following form, written in a database comment.

```json
{"pgcheckout":1,"kind":"branch","base":"app","gen":3,"branch":"feat/login","created_at":"2026-11-22T06:00:00Z","strategy":"file_copy_clone"}
```

`kind` is one of `base`, `branch`, `pool`, or `tmp`.
A branch with an expiry gets `expires_at`, and a base gets `open`, which shows whether it is open or closed.
A copy whose settings or privileges failed to reapply gets `warnings`, the number of failures.

Delete, reset, pool drain, and expired-database cleanup run only on databases that have a marker, whose `base` matches the target base, and whose `kind` is one of `branch`, `pool`, or `tmp`.
No deletion is based on a name prefix alone.
No command deletes the base or the maintenance database.
A database whose name matches pgcheckout's naming rules but has no marker is only reported as `unmanaged` by `list` and `doctor`.

## Naming rules

- Branch: `<base name>__<slug>`
- Pool: `<base name>__pool_<8 hex digits>`
- Copy in progress: `<base name>__tmp_<8 hex digits>`

The slug is the branch name lowercased, with every character outside `a-z0-9_` replaced by `_`.
If a replacement or truncation happened, the first 8 digits of the SHA-256 of the original branch name are appended after `_`.
The whole name stays within 63 bytes.
The mapping between a branch and a database is verified by the marker's `branch`, not by the name.

## Handling the base

The base is protected with `ALLOW_CONNECTIONS false`.
A database that forbids connections can still be a copy source, and the destination is created in a connectable state (confirmed on PostgreSQL 18.7).
This guarantees there are no connections, which avoids a copy failing because connections remain on the source.

| Command | Behavior |
|---|---|
| `base create <name> --from <existing DB>` | Creates the base by copying an existing database. The original database stays usable |
| `base adopt <existing DB>` | Makes an existing database the base as is. After that, no one can connect to that database |
| `base open` | Allows connections to the base. Use it when changing the base, for example with migrations |
| `base close` | Forbids connections to the base and moves it to a new generation. Discards pools of other generations |

Only `base close` increases the generation.
`base create` and `base adopt` start from the maximum generation among the remaining pools and branches with the same base name, and any base that was renamed away, plus 1. If none remain, they start from 1. `base close` moves the base to the maximum of its own generation and those same generations, plus 1. In the usual case this is the current generation plus 1. A copy is current only when its generation equals the base's. Any other generation, older or newer, is stale. So when the base is deleted outside pgcheckout and created again, or comes back with an old marker after a rename or a restore from a dump, copies of the other base never share its generation.
While the base is open, `ensure` does not create new copies and returns the `base_open` error. It still returns branches that already exist, even when the base is open.
When the database that is already the base is open, `base adopt` returns the `base_open` error. Only `base close` closes it.
Forbidding connections does not cut connections that are already established. So right after forbidding connections, pgcheckout counts connections again. If any remain, it returns the base to the open state and returns `database_in_use`. It increases the generation only after confirming no connections remain.
A base name cannot contain `__` and cannot end with `_`. This avoids overlapping with the naming rules of databases that belong to another base.
When `base adopt` fails, it restores the target database's comment and connection permission. No marker remains after a failure.
`base create` forbids connections while the copy is still `tmp`, confirms no connections remain, and finally renames it to make it the base. If it fails partway, it deletes the `tmp`, so no base remains. The exception is when the response to `CREATE DATABASE` is lost (see below).
`base adopt`, `base close`, and `base create` return the `database_in_use` error when other connections remain on the target. Only with `--terminate` do they cut the remaining connections.

## The ensure flow

`ensure [branch name]` makes sure a branch database exists and returns its connection target.
If the branch name is omitted, it uses git's current branch name.

1. Read the base's marker and take an advisory lock on the maintenance database.
2. Look for a database with the name determined by the branch name. If it exists and the marker's `branch` matches, return it without changing anything (`created: false`). If its generation differs from the base's, add `stale: true`. It does not recreate the database. If a database with the same name has no marker, or its marker points to a different branch, return the `unmanaged_database` error.
3. If none exists, pick 1 pool whose generation equals the base's, and do the rename and the marker rewrite in 1 transaction (`source: "pool"`).
4. If the pool is empty, outside the lock copy to a `tmp` name and reapply settings and privileges, retake the lock and redo step 2, then do the rename and the marker rewrite (`source: "copy"`).
5. After printing the result, if the pool is below the target count, start a refill as a separate process detached from itself. The refill process does not inherit standard input and output.

The rename and the marker rewrite are in 1 transaction, so a database with the final name and no marker is never left behind.
`CREATE DATABASE` cannot run inside a transaction, so if the process dies right after copying, a `tmp` without a marker can remain. `doctor` reports it.
Marker writes are tried up to 3 times. If the client gives up on `CREATE DATABASE` first, for example because of an interruption, pgcheckout checks over another connection whether the database exists and, if it does, writes the marker. It does not delete a marked `tmp` on the spot, and the error includes its name. `gc` deletes it 1 hour after creation, and `pool drain` deletes it immediately. If one is left during `base create`, it can be deleted after the base is created again. If the marker still could not be written, the error includes the database name.

If 2 `ensure` calls for the same branch run at the same time, the one that takes the lock later finds the existing database in step 1 and discards its own copy.

Advisory locks are independent for each database you connect to. Everyone who uses the same base must specify the same maintenance database.

## Commands

| Command | Behavior |
|---|---|
| `ensure [branch]` | See above |
| `list` | Lists the base, branches, pools, and `unmanaged` databases |
| `dsn [branch]` | The branch's connection string |
| `drop [branch]` | Deletes the branch. It fails if connections remain, and `--force` cuts them and deletes |
| `reset [branch]` | Replaces the branch with a fresh copy of the base. It takes a pool copy or makes a copy first, then, in 1 transaction, renames the old database to a `tmp` name and the new one to the branch name, and deletes the old database afterwards. If that deletion fails, `gc` removes the leftover. If any step before that transaction fails, the branch stays as it was |
| `pool fill` / `pool status` / `pool drain` | Fill, status, and discard of the pool |
| `gc` | Deletes expired branches and pools of other generations. Has `--dry-run` |
| `doctor` | Reports the server version, privileges, the selected strategy, the base state, and `unmanaged` databases |
| `base ...` | See above |
| `hook install` / `hook uninstall` | Installs or removes the git post-checkout hook |
| `help` | Returns the list of commands, flags, and environment variables as JSON |

An expiry is set only when stated explicitly, as in `ensure --ttl 72h`.

## Configuration

Configuration is passed through environment variables and flags. If the repository has `.pgcheckout.json`, non-secret values (`base`, `pool_size`, `strategy`) are read from it.

| Environment variable | Meaning |
|---|---|
| `PGCHECKOUT_ADMIN_URL` | Connection string for the maintenance database. If unset, it does not fall back to `DATABASE_URL` and returns an error |
| `PGCHECKOUT_BASE` | The base name |
| `PGCHECKOUT_POOL_SIZE` | Target pool size. Default is 2, and 0 disables the pool |
| `PGCHECKOUT_STRATEGY` | Strategy override (`clone`, `file_copy`, `wal_log`, `default`) |

`DATABASE_URL` is not read by default because using an application connection target as the maintenance one by mistake could run management SQL against the base or a database close to production.

A branch's connection string is built by replacing only the database name in the maintenance connection string.

## Input and output

- Standard output gets exactly 1 JSON object by default. `--format text` switches to human-readable output.
- Errors go to standard error as `{"error":{"code":"...","message":"...","hint":"..."}}`. `code` is chosen from a fixed vocabulary.
- Exit codes: 0 for success, 1 for a runtime failure, 2 for a usage error.
- It never prompts for input.
- Only when starting a refill fails does it write `{"warning":{"code":"refill_failed","message":"..."}}` to standard error. The exit code stays 0.

## Retrying forced deletion

When `drop --force` fails with insufficient privilege (42501) or object in use (55006), it retries every 300 ms, up to 5 times in total.
AlloyDB Omni briefly opens internal statistics connections to each database. While one of those connections exists, an ordinary role cannot cut connections, and forced deletion fails with insufficient privilege.
The marker check runs just before the delete on every attempt.

## Structure

```
cmd/pgcheckout/        entry point
internal/cli/      argument parsing, output, exit codes
internal/config/   environment variables, flags, .pgcheckout.json
internal/naming/   slugs and database names
internal/marker/   marker encoding and decoding
internal/pg/       connections, copy strategies, SQL
internal/branch/   the ensure, drop, reset, list, gc, pool and base procedures
internal/gitx/     git invocations, the post-checkout hook, the connection file
internal/mcpserver/ the MCP server and its tools
internal/testpg/   integration test cleanup (built only for integration tests)
```

Dependencies are limited to `github.com/jackc/pgx/v5` and the official MCP Go SDK (`github.com/modelcontextprotocol/go-sdk`, with `github.com/google/jsonschema-go` for schema inference). Argument parsing uses the standard library's `flag`.

## Tests

- Unit tests: names, markers, strategy selection, and configuration parsing. They use no database.
- Integration tests: built with the build tag `integration` and run against the server that `PGCHECKOUT_TEST_ADMIN_URL` points to. They are skipped if it is unset. They run on both PostgreSQL 18 and AlloyDB Omni 17.
- Integration tests run with a role that has only `CREATEDB`.

## Measurements (2026-11-22, Apple Silicon Mac, 1 GB database)

| Environment | Copy | Rename |
|---|---|---|
| PostgreSQL 18.7 (macOS, APFS, `clone`) | 43-106 ms | 0.12-0.22 ms |
| PostgreSQL 18.7 (Docker, ext4, `clone`) | 3.07 s | not measured |
| PostgreSQL 18.7 (Docker, XFS, `clone`) | 43-68 ms | 0.54 ms |
| AlloyDB Omni 17.10 (Docker, ext4, `WAL_LOG`, 493 MB) | 0.95-1.02 s | 0.8-3.1 ms |

Each is 1 to 4 measurements, and the variance was not examined.

## MCP server

`pgcheckout mcp` runs as an MCP server over standard input and output. Agents can call tools instead of assembling commands.

It uses the official Go SDK (`github.com/modelcontextprotocol/go-sdk`).
The 2026-07-28 version of MCP changed the connection procedure, and older clients that use `initialize` and newer clients that use `server/discover` now coexist. The SDK serves both, so following the procedure is left to the SDK.

There are 8 tools. Base operations and `pool drain` are excluded because they affect other users.

| Tool | Description | Nature |
|---|---|---|
| `doctor` | Returns the state of the server, role, and base | read-only |
| `list` | Returns the list of the base, branches, and pools | read-only |
| `pool_status` | Returns the pool state | read-only |
| `dsn` | Returns the connection target of a branch database | read-only |
| `ensure` | Prepares a branch database | idempotent |
| `reset` | Recreates a branch database | destructive |
| `drop` | Deletes a branch database | destructive |
| `gc` | Deletes expired databases | destructive |

There are 3 differences from the CLI.

- Results do not include the connection target's password, because tool results stay in the conversation record. `url` is returned with the password removed, and `password_redacted` shows that it was removed. When a connection target that includes the password is needed, write it to a file with `env_file` on `ensure` and `reset`.
- `reset` and `drop` always take a branch name. The client decides the server's working directory, so it does not necessarily match the repository the agent is working in. `ensure` and `dsn` can omit the branch name and include the resolved branch name in the result.
- Failures are returned as tool results with `isError` set. The body is the same `{"error":{"code","message","hint"}}` as the CLI. Even when configuration is missing, the server does not exit and returns a tool failure.

Configuration is read on every tool call, by the same procedure as the CLI.
Standard output carries only MCP messages.
When standard input closes, the server cancels running tools, waits for cleanup to finish, and then exits.

## Non-goals

- Merging and diffing between branches
- Separate processes or separate versions per branch
- Other products such as MySQL
- Exposing base operations, `pool drain`, and `hook` as MCP tools
