# Using pgtwig from an agent

pgtwig gives each git branch its own PostgreSQL database, copied from a protected base database.

## Contract

- stdout: exactly one JSON object. stderr on failure: `{"error":{"code","message","hint"}}`.
- Exit codes: 0 success, 1 runtime error, 2 usage error.
- Never prompts. Never reads stdin.
- `ensure` is idempotent: the same branch always maps to the same database.
- Required configuration: `PGTWIG_ADMIN_URL` and `PGTWIG_BASE` (or `--admin-url`, `--base`, `.pgtwig.json`).

## Typical flow

```bash
pgtwig doctor
```

Read `problems`. An empty array means the server, role and base are ready.

```bash
pgtwig ensure
```

Use the `url` field as the connection string for the current git branch. Pass a name to target another branch: `pgtwig ensure feat/x`.

```bash
pgtwig reset
```

Drops and recreates the current branch's database from the base. Add `--force` if sessions are still connected.

```bash
pgtwig drop feat/x
```

## Fields worth checking

- `created`: false means the database already existed and was left untouched.
- `stale`: true means the base has changed since this branch was created. Run `pgtwig reset` to pick up the new base; pgtwig never recreates a branch on its own.
- `source`: `existing`, `pool` or `copy`.
- `warnings`: per-database settings or grants that could not be replicated from the base.

## Error codes

| Code | Meaning | What to do |
|---|---|---|
| `config_missing` | admin URL or base not set | set `PGTWIG_ADMIN_URL` / `PGTWIG_BASE` |
| `base_not_found`, `base_not_marked` | no usable base | `pgtwig base create <name> --from <db>` |
| `base_open` | the base is being edited | wait, or run `pgtwig base close` if you opened it |
| `database_in_use` | sessions are connected | close them, or retry with `--force` / `--terminate` |
| `unmanaged_database` | a database with that name exists but pgtwig did not create it | do not drop it; report to the user |
| `unmanaged_file` | the env file or hook exists and pgtwig did not write it | move the file or choose another path |
| `detached_head`, `not_git_repo` | no branch name available | pass the branch name explicitly |

## Rules

- Do not run `DROP DATABASE` yourself on databases named `<base>__...`. Use `pgtwig drop`, `pgtwig gc` or `pgtwig pool drain`.
- Do not connect to the base database. To change it: `pgtwig base open`, apply the change, `pgtwig base close`.
- The `url` field contains the password from the admin URL. Do not log it.
