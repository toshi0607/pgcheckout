# The pgcheckout state model

English | [日本語](STATE_MODEL.ja.md)

The pgcheckout code has no state type and no transition table. State lives in two places: the PostgreSQL catalog (database names, comments, whether connections are allowed) and the position within a function's sequence of steps.
This document restates the states and transitions read from the code as tables. A specification of the same content written in TLA+ is in [pgcheckout.tla](pgcheckout.tla), and the verification results are recorded at the end.

The code is unchanged. Line numbers are those at the time of writing.

## Things that hold state

There are 9 things that hold state. The TLA+ specification includes 1 to 6, whose intermediate states can be observed by multiple concurrently running processes. 7 to 9 are limited to the tables, because they are contained within a single process or are decided by atomic file replacement.

| No. | What holds state | Where the state is kept | TLA+ |
|---|---|---|---|
| 1 | Base | Catalog (name, marker, whether connections are allowed) | `base` |
| 2 | Copies (`tmp` and pool) | Catalog (name, marker) | `copies` |
| 3 | Branch | Catalog (name, marker) | `branchDb` |
| 4 | Operation steps | Position within a function | `pc` |
| 5 | Connection and context | Process memory | `conn`, `canc` |
| 6 | Advisory locks | Server session | `claim`, `fillLock` |
| 7 | Hook file | `.git/hooks/post-checkout` | Not included |
| 8 | Connection target file | The `--env-file` path | Not included |
| 9 | MCP server and refill process | Process memory | Not included |

## 1. Base

The state of the base is decided by four things: whether the database with the base name exists, whether a marker exists, the marker's `open`, and `datallowconn`. The generation is the marker's `gen`.

| State | Marker | `open` | Connections | Can be a copy source |
|---|---|---|---|---|
| absent | None | — | — | No |
| unmarked | None | — | Any | No |
| closed | Present | false | Blocked | Yes |
| open | Present | true | Allowed | No |
| closing | Present | true | Blocked | No |

`baseIsOpen` decides whether the base can be a copy source (`internal/branch/service.go:179`). If `open` is true or connections are allowed, the base is treated as open. A combination where the marker is false and connections are allowed is also treated as "open", but no pgcheckout operation reaches this combination.

| Transition | Operation that causes it | Condition | Code |
|---|---|---|---|
| absent → closed (first generation) | `base create` | Finish copying to `tmp`, blocking connections, and confirming that no connections exist, then rename | `base.go:103-135` |
| unmarked → closing (first generation) | `base adopt` | No marker exists | `base.go:174-179`, `base.go:253-260` |
| open → closing | `base close` | A marker exists and the base is not closed | `base.go:224`, `base.go:253-260` |
| closing → closed | `base close`, `base adopt` | Zero connections, not canceled, and the catalog matches what was written. `base close` increments the generation by 1 | `base.go:278-287` |
| closing → open | Failure of `base close` | Connections remain, cancellation, or loss of the connection | `base.go:292-302` |
| closing → unmarked | Failure of `base adopt` | Same as above. The comment and whether connections are allowed are restored | `base.go:295-299` |
| closed → open, closing → open | `base open` | A marker exists | `base.go:213-217` |

The first generation is the maximum generation remaining among the pools and branches with the same base name, plus 1. If none remain, it is 1 (`firstGen`, `base.go:39-61`). This prevents generations from overlapping with copies of a previous base when the base is dropped outside pgcheckout and created again.

"closing" is the state between blocking connections and finishing counting connections. Blocking connections does not disconnect existing connections, so blocking and updating the generation cannot be a single write. During this state `open` is still true, so no copy starts.

The base stays in "closing" in two cases: when the write that restores it also fails, and when the process crashes. In the former case the error contains `could not restore base database` (`base.go:303-316`). In either case, running `base open` or `base close` again leaves the state.

## 2. Copies (`tmp` and pool)

A copy is created with `CREATE DATABASE ... TEMPLATE`, and its name changes as the marker's `kind` is rewritten.

| Transition | Operation that causes it | Condition | Code |
|---|---|---|---|
| absent → unmarked | `ensure`, `pool fill`, `base create` | The copy source has no connections | `copy.go:31`, `pg/strategy.go:43-58` |
| unmarked → `tmp` | Same operations | The marker write is tried up to 3 times | `copy.go:52`, `copy.go:75-99` |
| unmarked → `tmp` (left behind) | Same operations | After the client gives up on `CREATE`, another connection finds the copy and marks it. It is not deleted | `copy.go:35-48`, `copy.go:101-136` |
| `tmp` → branch | `ensure`, `reset` | Inside the lock: the base OID and generation are the same as when the copy started, the base is closed, and the branch does not exist yet | `ensure.go:117-156` |
| `tmp` → pool | `pool fill` | Inside the lock: the base OID and generation are the same, and the base is closed | `pool.go:117-143` |
| `tmp` → base | `base create` | See the base table above | `base.go:129-135` |
| `tmp` → absent | The operation that created it | It could not be published | `mutation.go:247-265` |
| `tmp` → absent | `base close`, `gc` | The generation is older than the base | `gc.go:58-59` |
| `tmp` → absent | `gc` | The generation is the same as or newer than the base, and more than 1 hour has passed since creation | `gc.go:59` |
| `tmp` → absent | `pool drain` | Unconditional | `pool.go:183-185` |
| pool → branch | `ensure`, `reset` | Inside the lock: the generation is the same as the base | `ensure.go:174-190` |
| pool → absent | `base close`, `gc` | The generation differs from the base | `gc.go:56-57` |
| pool → absent | `pool drain` | Unconditional | `pool.go:183-185` |

The rename and the marker rewrite go into one transaction, and the transaction begins by checking the catalog again (`mutation.go:123-136`). A deletion also checks the catalog immediately beforehand (`mutation.go:71`). No transition deletes a database that has no marker.

A `tmp` can be deleted by `base close`, `gc`, or `pool drain` in another process even while the operation that created it is still alive. The side whose copy was deleted learns at rename time that the database does not exist, and starts over from copying (`ensure.go:145-147`, `pool.go:135-136`).

## 3. Branch

| Transition | Operation that causes it | Condition | Code |
|---|---|---|---|
| absent → present | `ensure`, `reset` | Rename from the pool or from `tmp` | Table above |
| present → absent | `drop` | The marker's `branch` matches | `lifecycle.go:247-260` |
| present → present (replaced) | `reset` | Inside the lock: the base is closed and a replacement of the base's generation is ready. The old database becomes a `tmp` | `mutation.go:146-224` |
| present → absent | `gc` | `expires_at` has passed | `gc.go:54-55` |

No transition rewrites the generation or content of an existing branch. A branch with an old generation is only reported as `stale: true` (`ensure.go:236`).

## 4. Operation steps

An operation that spans multiple statements moves through the following positions in order. "Lock" means the advisory lock `claim`. Reads and writes inside the lock look like a single transition to other lock holders.

| Operation | Position | What it does | Lock | Code |
|---|---|---|---|---|
| `ensure` | `e_claim1` | Look for the branch. Take from the pool | Inside | `ensure.go:75-98` |
| | `create` | `CREATE DATABASE` | Outside | `copy.go:31` |
| | `mark` | Write the marker | Outside | `copy.go:52` |
| | `e_claim2` | Check again and rename | Inside | `ensure.go:117-156` |
| | `discard` | Delete the copy that was not used | Outside | `ensure.go:157-164` |
| `reset` | `r1` | Confirm that the base is closed. The rest follows `ensure`, except that an existing branch is replaced in 1 transaction and the old database is deleted in `discard` | Inside | `lifecycle.go:102-206`, `mutation.go:146-224` |
| `pool fill` | `f_lock` | Try the `fill` lock | — | `pool.go:55` |
| | `f_claim1` | Count the pool | Inside | `pool.go:78-90` |
| | `create`, `mark` | Same as `ensure` | Outside | `pool.go:99` |
| | `f_claim2` | Check again, rename, and return to `f_claim1` | Inside | `pool.go:117-143` |
| `base close` | `bc1` | Block connections | Acquire | `base.go:203-224`, `base.go:244-263` |
| | `bc2` | Count connections | Inside | `base.go:320-351` |
| | `bc3` | Move to the next generation, close, and delete copies of other generations | Release | `base.go:278-287`, `base.go:230-237` |
| | `brestore` | Return to the open state | Release | `base.go:292-317` |
| `base adopt` | `ba1` | Decide the first generation, write the marker, and block connections. Continues from `bc2` | Acquire | `base.go:155-179` |
| `base create` | `bcr1` | Confirm that the name is free and decide the first generation | Acquire | `base.go:74-94` |
| | `create`, `mark` | Same as `ensure` | Inside | `base.go:103` |
| | `bcr2` | Block connections and rename | Release | `base.go:111-135` |
| `drop`, `gc`, `pool drain`, `base open` | One | The transitions in the tables above | Inside | — |

`ensure` and `pool fill` release the lock during copying. During that time the base can be opened, changed, and closed. Comparing the OID and generation inside the second lock is what discards the copy in that case (`ensure.go:133`, `pool.go:122`).

`dsn`, `list`, `doctor`, and `pool status` only read the catalog and cause no transitions.

## 5. Connection and context

| State | How it is entered | Behavior afterward | Code |
|---|---|---|---|
| connected | Start of the operation | — | `service.go:56` |
| canceled | Cancellation by the caller, a deadline | Statements that use the caller's context fail. Cleanup continues in a separate context with a 5-second deadline | `service.go:189-195` |
| disconnected | Loss of the connection | Statements that use the original connection fail. Cleanup runs on a new connection | `service.go:120-135` |
| finished | Completion, crash | No cleanup after a crash | — |

Cleanup means three things: deleting the copy (`discardCopy`), the write that restores the base, and re-attaching the marker. Cancellation is checked explicitly just before statements that change state. There are 9 such places: before the rename (`ensure.go:138`, `ensure.go:176`, `pool.go:128`, `base.go:126`), before blocking connections (`base.go:111`, `base.go:249`), before returning a copy (`copy.go:69`), and before counting connections and before incrementing the generation (`base.go:272`, `base.go:279`). Writes after the check use a separate context, so cancellation does not stop them.

`CREATE DATABASE` can keep running on the server after the client gives up. If, from another connection, the original backend still appears to be running, the copy can appear later, so the error includes the name (`copy.go:106-117`).

## 6. Advisory locks

| Lock | How it is acquired | What it protects | Code |
|---|---|---|---|
| `claim` | Wait and acquire | The base marker, branch names, and deletion of the pool and `tmp` | `service.go:225-228` |
| `fill` | Try, and finish if not acquired | That only one `pool fill` runs at a time | `pool.go:55`, `pool.go:68-70` |

Both are session-level locks, and the key is built from the base name and the purpose (`service.go:198-199`). When the connection drops, the server releases the lock. For that reason, the cleanup of a process that lost its connection runs on a new connection without holding the lock. That this cleanup does not break anything when it overlaps with another process's intermediate state is ensured by checking the catalog just before the write (`mutation.go:25-54`).

## Time-based transitions

| Target | Condition | Effect | Code |
|---|---|---|---|
| Branch | `expires_at` passes | Becomes a target of `gc` deletion | `gc.go:55` |
| `tmp` | More than 1 hour has passed since creation | Becomes a target of `gc` deletion even if the generation is the same as or newer than the base | `gc.go:50`, `gc.go:59` |

Time is read from the server's `now()` (`gc.go:46`).

## 7. Hook file

| State | How to tell |
|---|---|
| absent | The file does not exist |
| unmanaged | Line 2 is not the pgcheckout heading |
| managed | Line 2 is the pgcheckout heading |

| Transition | Operation that causes it | Condition | Code |
|---|---|---|---|
| absent → managed | `hook install` | — | `gitx/hook.go:88` |
| managed → managed | `hook install` | Rewrites only when the content or permissions differ | `gitx/hook.go:72-74` |
| unmanaged → managed (leave a backup) | `hook install --force` | The backup name is free | `gitx/hook.go:82-87`, `gitx/hook.go:141-162` |
| managed → absent | `hook uninstall` | — | `gitx/hook.go:128` |

An unmanaged file is neither rewritten nor deleted without `--force` (`gitx/hook.go:69-71`, `gitx/hook.go:118-120`). `hook uninstall` does not restore the backup to its original name; it reports the backup's path.

## 8. Connection target file

| Transition | Condition | Code |
|---|---|---|
| absent → managed | — | `gitx/env.go:90` |
| managed → managed | Rewrites only when the content differs | `gitx/env.go:86-88` |
| unmanaged → (unchanged) | If line 1 is not the heading, returns `unmanaged_file` | `gitx/env.go:83-85` |

A write goes to a temporary file in the same directory and is then renamed into place (`gitx/file.go:40-58`). If this write fails after the branch database has been prepared, the database remains and the error's `hint` describes that state (`gitx/env.go:36-54`).

## 9. MCP server and refill process

| State | How it is entered | Tool calls | Code |
|---|---|---|---|
| accepting | Startup | Runs them | `mcpserver/server.go:36-49` |
| shutting down | End of standard input, end of the transport | Does not start new calls. Cancels running calls and waits | `mcpserver/server.go:51-54`, `mcpserver/server.go:68-76` |
| finished | Zero running calls | — | `mcpserver/server.go:54` |

The refill process is started as a separate, detached process when the result of `ensure` is `RefillNeeded` (`cli/run.go:213-233`). After it starts, it behaves as `pool fill` in the tables so far. The MCP server waits for the child processes it started to exit and reaps them (`cli/run.go:139-152`).

## Invariants

The properties checked in the TLA+ specification are listed below. The names are the names of the definitions in the specification.

| Name | Meaning |
|---|---|
| `MutexOK` | Only a process whose connection is alive and which is inside a section that spans multiple statements holds `claim` |
| `BaseProtected` | A closed base has no connections from outside pgcheckout |
| `SealedBase` | The content of a closed base is the same as its content when it reached that generation |
| `MarkerAgrees` | A base whose marker indicates closed also has connections blocked |
| `Fresh` | The content of pool copies and branches is the same as the content of the base when it reached the marker's generation |
| `FreshCurrent` | The content of pool copies and branches with the same generation as a closed base is the same as the base's content. Used in the configurations where the base is recreated externally |
| `NoFutureGen` | The generation of pool copies and branches does not exceed the generation of the base |
| `PoolBound` | The pool at the current generation does not exceed the target count |
| `UnmarkedTransient` | Without faults, an unmarked copy exists only just before the operation that created it writes the marker |
| `NoSilentLeak` | When all operations have finished, any `tmp` and unmarked copy that remain are ones whose names appeared in an error, or ones owned by a crashed process |
| `ClosingReported` | If the base is "closing" when all operations have finished, either the failure to restore it has been reported or the process has crashed |
| `SealedImmutable` | Once a generation is decided, its content does not change. A base whose content changed is always closed with a new generation |
| `BaseKept` | pgcheckout does not delete the base |
| `GenMonotonic` | The base's generation does not decrease |
| `BranchStable` | The generation and content of an existing branch are not rewritten, except when `reset` replaces it with a copy of the current base |
| `BranchKept` | A branch disappears only through `drop` or `gc` |

## What the model omits

- The contents of a database are represented by a single integer (the content version).
- Re-applying settings and privileges (`pg/settings.go`, `pg/acl.go`) is not represented. Most failures there become only warnings, but if revoking privileges from PUBLIC fails, the copy is discarded and the operation fails (`ensure.go:110-113`, `base.go:107-110`, `pool.go:108-115`). This path has the same shape as the path that fails after writing the marker and discards the copy, so it is not modeled as a separate transition.
- Connections to branches and pool copies are not represented. This excludes the case where connections remain and the deletion returns `database_in_use`, and the retry with forced deletion.
- Databases whose marker points to a different base, whose marker is corrupted, or which match the naming rule but have no marker are not represented. No transition in the code changes them.
- Collisions in the random part of a name are not represented.
- Cancellation and connection loss are represented as happening between statements. Only for `CREATE DATABASE`, the outcomes when it happens during execution (not created, created and marked, appears later) are separate transitions.
- Time is represented only as the truth value of "more than 1 hour has passed" and "the deadline has passed".
- The guarantee of termination (liveness) is not checked. What is checked is safety, and that there is no state in which processes wait for each other and cannot proceed.

## Verification results

With TLC 2.19 (tla2tools.jar v1.7.4), all reachable states of the following configurations were explored. Use [check.sh](check.sh) to run it.

| Configuration | Processes | Branches | Operation count | Generation limit | Faults | States reached | Result |
|---|---|---|---|---|---|---|---|
| `normal` | 3 | 2 | 4 | 3 | None | 78,318,795 | No violation |
| `lifecycle` | 3 | 1 | 4 | 2 | None | 1,627,984 | No violation |
| `cancel` | 2 | 1 | 3 | 2 | 2 cancellations | 460,329 | No violation |
| `loss` | 2 | 1 | 3 | 2 | 2 connection losses | 814,404 | No violation |
| `crash` | 2 | 1 | 3 | 2 | 2 crashes | 256,735 | No violation |
| `cancel-lifecycle` | 2 | 1 | 3 | 2 | 2 cancellations | 106,136 | No violation |
| `loss-lifecycle` | 2 | 1 | 3 | 2 | 2 connection losses | 195,015 | No violation |
| `crash-lifecycle` | 2 | 1 | 3 | 2 | 2 crashes | 70,724 | No violation |
| `allfaults` | 2 | 1 | 2 | 2 | 2 in total across the 3 kinds | 145,896 | No violation |
| `union` | 2 | 1 | 3 | 2 | None | 106,392 | No violation |
| `union-faults` | 2 | 1 | 3 | 2 | 2 in total across the 3 kinds | 4,051,893 | No violation |
| `external` | 2 | 1 | 4 | 3 | None | 2,522,533 | No violation |
| `external-faults` | 2 | 1 | 4 | 3 | 1 in total across the 3 kinds | 33,309,473 | No violation |
| `reentry` | 2 | 1 | 4 | 3 | None | 29,139,569 | No violation |

`normal` and the configurations with faults start from a state where a closed base exists, and use `ensure`, `reset`, `drop`, `pool fill`, `base open`, `base close`, `gc`, and `pool drain`. The configurations with `lifecycle` in the name start from the absent and unmarked states, and add `base create` and `base adopt`. `union` and `union-faults` start from all three initial states and use all 10 kinds of operations. `external` and `external-faults` start from a state where a closed base exists, and add up to 2 drops and recreations of the base outside pgcheckout in total (which appear as an unmarked database). The operations used are `ensure`, `pool fill`, `base open`, `base close`, `base adopt`, and `base create`, and the properties checked are `MutexOK`, `BaseProtected`, `MarkerAgrees`, `Fresh`, `FreshCurrent`, `NoFutureGen`, and `PoolBound`. `reentry` adds, to the operations of `external`, a marked base that is renamed away and back and a base restored from a dump taken while it was open, with up to 3 external operations in total. It checks the same properties except `NoFutureGen`, which a returning base violates by design. In every configuration, the pool target is 1 and the content version is 0 or 1 (up to 2 for `normal`).

What was verified is safety within these bounds, and that there is no state in which processes wait for each other and cannot proceed. Nothing is established for a number of processes or operations beyond these bounds.

### Confirming that the checks are not vacuous

Conditions in the specification were removed one at a time, and TLC was confirmed to report a violation.

| Removed condition | Corresponding code | Violated property |
|---|---|---|
| The OID and generation comparison inside the second lock of `ensure` | `ensure.go:133` | `Fresh` |
| The check, inside the second lock of `ensure`, of whether the base is open | `ensure.go:130` | `Fresh` |
| The OID and generation comparison inside the second lock of `pool fill` | `pool.go:122` | `Fresh` |
| The recount of connections in `base close` | `base.go:275` | `BaseProtected` |
| The catalog check just before the final write of `base close` | `mutation.go:234` | `MarkerAgrees` |
| The generation update in `base close` | `base.go:284-284` | `SealedImmutable` |
| Mutual exclusion of the `fill` lock | `pool.go:55` | `PoolBound` |
| Deleting a copy that was not used | `mutation.go:247-265` | `NoSilentLeak` |
| Including the name of a left-behind `tmp` in the error | `copy.go:39-47` | `NoSilentLeak` |
| Releasing the lock when the connection drops | Server behavior | `MutexOK` |
| The catalog check just before the write that restores the base, which runs without holding the lock | `base.go:296`, `mutation.go:234` | `MarkerAgrees` |
| Deleting a copy that was not used (configurations with crashes) | `mutation.go:247-265` | `NoSilentLeak` |
| The first-generation calculation of `base create` and `base adopt` (always set to 1) | `base.go:39-61` | `Fresh` (configuration `external`) |
| The next-generation calculation of `base close` (always the base's generation plus 1) | `base.go:219-223` | `Fresh` (configuration `reentry`) |
| The check, inside the first lock of `ensure`, of whether the base is open | `ensure.go:89-91` | No violation |

The last row produced no violation. Even with the first check removed, the check inside the second lock discards the same copy. The first check exists to avoid creating a copy that would be discarded, and does not affect safety.

There are 14 states confirmed to be reachable: allocation from the pool, allocation from a copy, retry because the generation changed, the case where connections remain and the base cannot be closed, a left-behind `tmp`, completion of `base create`, generation 3, failure of the restoring write, the case where a copy appears later, the case where the marker could not be written, an unmarked copy left by a crash, a restoring write that runs while another process holds the lock, a branch with the same generation as the recreated base, and a branch with a generation older than the recreated base.

## What the verification revealed

### Recreating the base outside pgcheckout caused a pool copy with old content to be assigned as new (fixed)

Before the fix, if the base was dropped with `DROP DATABASE` and recreated with `base create` or `base adopt`, the generation returned to 1. Pool copies and branches made from the previous base kept marker generation 1. Because the marker refers to the base by name, they could not be distinguished from copies of the recreated base.

In the model, with the specification where the generation always starts from 1, the following sequence was found.

1. `ensure` creates a branch at generation 1.
2. The base is dropped externally and recreated with different content.
3. `base adopt` makes the recreated base generation 1.
4. The branch's generation is 1, the same as the base, but its content differs from the base.

It was also reproduced on PostgreSQL 18.7. Running `base create`, `pool fill`, `ensure b1`, `DROP DATABASE app`, `base create` with different content, and `ensure b2` in that order, `ensure b2` returned `source: "pool"` and `stale: false`, and its content was the content from before the recreation. `list` showed both branches as `stale: false`.

In the fix, `base create` and `base adopt` start from a generation equal to the maximum generation among the remaining pool copies and branches, plus 1 (`base.go:39-61`). The marker format and the places that compare generations (`ensure.go:175`, `ensure.go:236`) are unchanged. After the fix, the same sequence gives the following result. The recreated base becomes generation 2, `ensure b2` returns the new content with `source: "copy"` and `stale: false`, and `list` shows `b1` as `stale: true`. The pool copies of the previous base are counted as old in `pool status` and become targets of `gc` deletion.

In the model, the configurations `external` and `external-faults` were confirmed not to violate `Fresh`, `FreshCurrent`, and `NoFutureGen`. If the first generation is set back to 1, `external` violates `Fresh`.

When the base is replaced during a copy, the OID comparison already discarded the copy before the fix (`ensure.go:133`, `pool.go:122`). A `tmp` or an unmarked copy does not become a pool copy or a branch unless the process that created it passes this comparison, so they are not included in the first-generation calculation.

A `tmp` made from the previous base can remain with a marker whose generation is greater than or equal to that of the recreated base. This happens when the creating process crashes before discarding the copy through the OID comparison. This `tmp` is never assigned. `gc` also deletes a `tmp` whose generation is the same as or newer than the base 1 hour after creation (`gc.go:58-59`). Previously, only `pool drain` deleted a `tmp` whose generation was greater than the base.

### A base that came back with its marker could share a generation with other copies (fixed)

A marked base can leave and come back outside pgcheckout in 2 ways. It can be renamed away and later renamed back, keeping its marker and OID. Or a dump taken while the base was open can be restored, which creates a new database with the old marker: open, with the generation at the time of the dump. A closed base cannot be dumped, because `pg_dump` cannot connect to it. Both were confirmed on PostgreSQL 18.7.

Before the fix, `base close` always wrote the base's generation plus 1, and only a lower generation counted as stale. A base that returned with an old generation could therefore reach, on a later `base close`, a generation that copies of another base already carried. Those copies were then assigned, or shown, as current.

The fix has 3 rules.

1. `base create`, `base adopt` and `base close` take the maximum over the generations recorded on pool copies, branches, and any base marker for this base that sits on a database with another name (`base.go:39-60`).
2. `base close` writes that maximum, or the base's own generation if it is greater, plus 1 (`base.go:219-223`).
3. A copy is current only when its generation equals the base's. Any other generation is stale: it is shown as `stale`, never assigned, and deleted by `gc` if it is a pool copy (`ensure.go:236`, `catalog.go:51`, `gc.go:57`, `pool.go:203-207`).

In the model, the configuration `reentry` was confirmed not to violate `Fresh` and `FreshCurrent`. If `base close` is set back to the base's generation plus 1, `reentry` violates `Fresh`. If a branch whose generation is greater than the base's is treated as current, as it was before, `reentry` violates `FreshCurrent`. Within the bounds of `reentry`, removing the renamed-away base from the maximum in rule 1 produced no violation. That part is covered by the integration test `TestParkedBaseKeepsGenerationsApart`, not by the model.

1 case is outside both the fix and the model. If the base is renamed away and back while a copy that started from it is still running, and another database held the base's name when `CREATE DATABASE` read its template, the copy has the other database's content and still passes the OID comparison. `CREATE DATABASE` names its template, so pgcheckout cannot tell which database the copy read. The model excludes this sequence explicitly in `ExternalUnpark`.

### `reset` could fail after deleting the branch (fixed)

Before the fix, `reset` deleted the branch inside the lock, released the lock, and recreated it by the same steps as `ensure`. If `base open` came in during that time, the recreation failed with `base_open`, and the branch stayed absent. Cancellation, connection loss, and a failed copy led to the same state. The model showed this sequence, and the integration test `TestResetKeepsBranchWhenCopyFails` reproduces it on a real server with a failed copy.

After the fix, `reset` obtains the replacement first and replaces the branch in 1 transaction (`lifecycle.go:70-245`, `mutation.go:146-224`).

1. Inside the first lock, it confirms that the base is closed. If the branch does not exist, the rest is the same as `ensure`. Without `--force`, it stops with `database_in_use` when a session is connected to the branch.
2. It takes a pool copy of the base's generation. If there is none, it releases the lock, makes a copy, and inside the second lock compares the base OID and generation again, as `ensure` does.
3. In 1 transaction, it checks the catalog rows of both databases, renames the old database to a `tmp` name and writes a `tmp` marker with generation 0 on it, renames the replacement to the branch name, and writes the branch marker.
4. After releasing the lock, it deletes the old database. If that fails, the result carries a warning with the database name. Generation 0 is older than every base generation, so `gc` and `base close` delete it without waiting 1 hour.

A failure before the transaction commits leaves the branch as it was and consumes no pool copy. `ALTER DATABASE ... RENAME` fails while a session is connected, after waiting about 5 seconds, so `--force` disconnects the sessions and retries up to 5 times. 2 renames and 2 marker writes committing or rolling back together in 1 transaction was confirmed on PostgreSQL 18.7.

In the model, the property `BranchKept` (a branch disappears only through `drop` or `gc`) holds in every configuration that checks action properties, including those with cancellation, connection loss, and crashes. If `reset` is set back to deleting the branch first, `union` violates `BranchKept`.
