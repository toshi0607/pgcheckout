# todo example

English | [日本語](README.ja.md)

A small todo app for trying pgcheckout, and a script that walks through the whole workflow and checks every step.

## Run the walkthrough

Start PostgreSQL 18. The server listens on `127.0.0.1:54320` only.

```bash
docker compose -f examples/todo/compose.yaml up -d --wait
```

Run the script from anywhere in the repository.

```bash
examples/todo/demo.sh
```

The last line is `demo passed`. On a failed check, the script prints what it expected and what it got, and exits with code 1.

Stop the server when you are done.

```bash
docker compose -f examples/todo/compose.yaml down
```

## What the script does

The script builds `pgcheckout` and `todo` into a temporary directory and works in a scratch git repository there. It does not touch this repository's git state or your git configuration.

| Step | Command | What it checks |
|---|---|---|
| 1 | `todo createdb`, `todo migrate` | The development database has 3 rows |
| 2 | `pgcheckout base create`, `pgcheckout pool fill` | The pool has 2 ready copies |
| 3 | `pgcheckout hook install`, `pgcheckout ensure` | `main` gets a database, and `.env.pgcheckout` is written |
| 4 | `git checkout -b feature/priority`, a new migration | The hook rewrites `.env.pgcheckout`, and the branch has the new column and 4 rows |
| 5 | `git checkout main` | `main` has neither the new column nor the new row |
| 6 | `git checkout feature/priority` | The branch database was kept, not recreated |
| 7 | `pgcheckout base open`, a migration on the base, `pgcheckout base close` | Both branches are reported as stale, and neither is recreated |
| 8 | `pgcheckout reset main` | `main` has the base's new column and is no longer stale |
| 9 | `pgcheckout drop`, `pgcheckout pool drain`, `pgcheckout gc` | No database of this run remains |

## Use another server

Set `PGCHECKOUT_ADMIN_URL` to the maintenance database of your server. The role needs `CREATEDB`.

```bash
PGCHECKOUT_ADMIN_URL='postgres://dev@127.0.0.1:5432/postgres' examples/todo/demo.sh
```

| Variable | Meaning |
|---|---|
| `PGCHECKOUT_ADMIN_URL` | Maintenance database URL. The default is the server in `compose.yaml` |
| `DEMO_BASE` | Base name. The default is `todo_demo`, and the development database is `todo_demo_dev` |
| `KEEP=1` | Keep the databases and the scratch repository after the run |

The script refuses to start if a database whose name starts with the base name already exists. It deletes only the databases that it created.

The walkthrough passes on PostgreSQL 18 and on managed AlloyDB 18.4.

## The todo app

`todo` reads its connection URL from `--url`, then the `DATABASE_URL` environment variable, then the `DATABASE_URL=...` line in `.env.pgcheckout` in the current directory. So after `pgcheckout hook install --env-file .env.pgcheckout`, it follows `git checkout` without any other setting.

| Command | Action |
|---|---|
| `todo migrate` | Applies the `*.sql` files in `migrations` in file-name order |
| `todo add TITLE` | Adds a todo |
| `todo list` | Prints the column names, then one line per todo |
| `todo createdb NAME`, `todo dropdb NAME` | Creates or drops a database. The script uses these instead of `psql` |

## If the hook does not run in your own repository

git runs `.git/hooks/post-checkout` only when `core.hooksPath` is not set. If you set `core.hooksPath` globally, `pgcheckout hook install` still writes the hook to `.git/hooks` and adds a `note` to its result: the hook runs only if the hooks in that shared directory call the repository's local hooks. The script avoids this by running git without your global configuration.
