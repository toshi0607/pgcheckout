----------------------------- MODULE pgcheckout -----------------------------
(***************************************************************************)
(* Explicit model of the state transitions that pgcheckout performs on one *)
(* base and its branch, pool and temporary databases.                      *)
(*                                                                         *)
(* One action is one SQL statement, one transaction, or one section that   *)
(* runs entirely under the claim advisory lock without another process     *)
(* being able to observe an intermediate state.  STATE_MODEL.md maps every *)
(* action to the Go source.                                                *)
(***************************************************************************)
EXTENDS Integers, FiniteSets, TLC

CONSTANTS
    Procs,       \* concurrently running pgcheckout processes
    NoProc,      \* model value: nobody holds a lock
    Branches,    \* git branch names
    Ops,         \* operations a process may start
    Faults,      \* subset of {"cancel", "loss", "crash"}
    External,    \* TRUE: the base may be dropped and recreated outside pgcheckout
    InitBases,   \* subset of {"absent", "unmarked", "closed"}
    MaxGen, MaxContent, MaxCopies, MaxOps, MaxFaults, MaxExternal,
    PoolTarget   \* pool size, shared by ensure and pool fill

VARIABLES
    base,        \* the database named <base>
    sessions,    \* 1 when a client outside pgcheckout is connected to the base
    nextOid,
    sealed,      \* ghost: content of the base when each generation was sealed
    copies,      \* databases named <base>__tmp_* and <base>__pool_*
    nextId,
    branchDb,    \* databases named <base>__<branch slug>
    late,        \* CREATE DATABASE statements still running after the client gave up
    claim, fillLock,
    pc, op, arg, flag, seen, cid, conn, canc, res, dnext, rdrop,
    opsLeft, faultsLeft, extLeft,
    reported,    \* ghost: copies named in an error message
    excused,     \* ghost: copies owned by a crashed process
    stuck,       \* ghost: a failed base restore was reported, or its owner crashed
    resetLost    \* ghost: a reset dropped the branch and did not recreate it

vars == <<base, sessions, nextOid, sealed, copies, nextId, branchDb, late,
          claim, fillLock,
          pc, op, arg, flag, seen, cid, conn, canc, res, dnext, rdrop,
          opsLeft, faultsLeft, extLeft, reported, excused, stuck, resetLost>>

AbsentBase == [st |-> "absent", open |-> FALSE, allow |-> FALSE, gen |-> 0, content |-> 0, oid |-> 0]
NoBranch   == [ex |-> FALSE, gen |-> 0, content |-> 0, exp |-> "none"]
NoSeen     == [gen |-> 0, oid |-> 0]
AnyBranch  == CHOOSE b \in Branches : TRUE

Marked   == base.st = "marked"
Openish  == base.open \/ base.allow          \* baseIsOpen, service.go:179
Closed   == Marked /\ ~Openish
Closing  == Marked /\ base.open /\ ~base.allow

ReadyPool == {c \in copies : c.kind = "pool" /\ c.gen = base.gen}

(* firstGen, base.go: a new base starts one above every generation still stamped on a pool or branch database. *)
MarkedGens == {c.gen : c \in {x \in copies : x.kind = "pool"}}
              \cup {branchDb[b].gen : b \in {x \in Branches : branchDb[x].ex}}
FirstGen == IF MarkedGens = {} THEN 1
            ELSE 1 + CHOOSE g \in MarkedGens : \A h \in MarkedGens : h <= g
Running(p) == pc[p] \notin {"idle", "done"}
Quiescent == \A p \in Procs : ~Running(p)
Bad(p) == canc[p] \/ ~conn[p]                \* SQL on the session connection with the caller's context fails

OwnCopy(p) == {c \in copies : c.id = cid[p] /\ c.kind = "tmp"}
NewBranch(c, ttl) == [ex |-> TRUE, gen |-> c.gen, content |-> c.content,
                      exp |-> IF ttl THEN "live" ELSE "none"]

First(o) == CASE o = "ensure"     -> "e_claim1"
              [] o = "reset"      -> "r1"
              [] o = "drop"       -> "drop"
              [] o = "fill"       -> "f_lock"
              [] o = "baseopen"   -> "bo"
              [] o = "baseclose"  -> "bc1"
              [] o = "adopt"      -> "ba1"
              [] o = "basecreate" -> "bcr1"
              [] o = "gc"         -> "gc"
              [] o = "drain"      -> "drain"

---------------------------------------------------------------------------
Init ==
    /\ \E st \in InitBases :
         /\ base = CASE st = "absent"   -> AbsentBase
                     [] st = "unmarked" -> [AbsentBase EXCEPT !.st = "unmarked", !.allow = TRUE, !.oid = 1]
                     [] st = "closed"   -> [AbsentBase EXCEPT !.st = "marked", !.gen = 1, !.oid = 1]
         /\ sealed = [g \in 1..(MaxGen + 1) |-> IF g = 1 /\ st = "closed" THEN 0 ELSE -1]
    /\ sessions = 0 /\ nextOid = 2
    /\ copies = {} /\ nextId = 1 /\ late = {}
    /\ branchDb = [b \in Branches |-> NoBranch]
    /\ claim = NoProc /\ fillLock = NoProc
    /\ pc = [p \in Procs |-> "idle"]
    /\ op = [p \in Procs |-> "none"]
    /\ arg = [p \in Procs |-> AnyBranch]
    /\ flag = [p \in Procs |-> FALSE]
    /\ seen = [p \in Procs |-> NoSeen]
    /\ cid = [p \in Procs |-> 0]
    /\ conn = [p \in Procs |-> TRUE]
    /\ canc = [p \in Procs |-> FALSE]
    /\ res = [p \in Procs |-> ""]
    /\ dnext = [p \in Procs |-> "done"]
    /\ rdrop = [p \in Procs |-> FALSE]
    /\ opsLeft = MaxOps /\ faultsLeft = MaxFaults /\ extLeft = MaxExternal
    /\ reported = {} /\ excused = {} /\ stuck = FALSE /\ resetLost = FALSE

---------------------------------------------------------------------------
(* Helpers.  Finish reads branchDb', so every caller fixes branchDb' first. *)

ReleaseLocks(p) ==
    /\ claim' = IF claim = p THEN NoProc ELSE claim
    /\ fillLock' = IF fillLock = p THEN NoProc ELSE fillLock

Finish(p, r) ==
    /\ pc' = [pc EXCEPT ![p] = "done"]
    /\ res' = [res EXCEPT ![p] = r]
    /\ ReleaseLocks(p)
    /\ resetLost' = (resetLost \/ (op[p] = "reset" /\ rdrop[p] /\ ~branchDb'[arg[p]].ex
                                   /\ r \notin {"pool", "copy", "existing"}))

Step(p, s) ==
    /\ pc' = [pc EXCEPT ![p] = s]
    /\ UNCHANGED <<res, resetLost>>

StepR(p, s, r) ==
    /\ pc' = [pc EXCEPT ![p] = s]
    /\ res' = [res EXCEPT ![p] = r]
    /\ UNCHANGED resetLost

ToDiscard(p, r, nx) ==
    /\ pc' = [pc EXCEPT ![p] = "discard"]
    /\ res' = [res EXCEPT ![p] = r]
    /\ dnext' = [dnext EXCEPT ![p] = nx]
    /\ UNCHANGED <<claim, fillLock, resetLost>>

See(p) == seen' = [seen EXCEPT ![p] = [gen |-> base.gen, oid |-> base.oid]]
SeenCurrent(p) == seen[p].gen = base.gen /\ seen[p].oid = base.oid

Counters == <<opsLeft, faultsLeft, extLeft>>
Ghosts   == <<reported, excused, stuck>>
Params   == <<op, arg, flag, conn, canc>>

---------------------------------------------------------------------------
Start(p) ==
    /\ ~Running(p) /\ opsLeft > 0
    /\ \E o \in Ops, b \in Branches, t \in BOOLEAN :
         /\ o \notin {"ensure", "reset", "drop"} => b = AnyBranch
         /\ o \notin {"ensure", "reset", "baseclose", "adopt"} => t = FALSE
         /\ op' = [op EXCEPT ![p] = o]
         /\ arg' = [arg EXCEPT ![p] = b]
         /\ flag' = [flag EXCEPT ![p] = t]      \* --ttl for ensure and reset, --terminate for base close and adopt
         /\ pc' = [pc EXCEPT ![p] = First(o)]
    /\ seen' = [seen EXCEPT ![p] = NoSeen]
    /\ cid' = [cid EXCEPT ![p] = 0]
    /\ conn' = [conn EXCEPT ![p] = TRUE]
    /\ canc' = [canc EXCEPT ![p] = FALSE]
    /\ res' = [res EXCEPT ![p] = ""]
    /\ dnext' = [dnext EXCEPT ![p] = "done"]
    /\ rdrop' = [rdrop EXCEPT ![p] = FALSE]
    /\ opsLeft' = opsLeft - 1
    /\ UNCHANGED <<base, sessions, nextOid, sealed, copies, nextId, branchDb, late,
                   claim, fillLock, faultsLeft, extLeft, reported, excused, stuck, resetLost>>

(* ensure, first claim section: ensure.go:75-98 and claimPool, ensure.go:169-208 *)
EClaim1(p) ==
    /\ pc[p] = "e_claim1"
    /\ UNCHANGED <<base, sessions, nextOid, sealed, nextId, late, cid, dnext, rdrop>>
    /\ UNCHANGED <<Params, Counters, Ghosts>>
    /\ IF Bad(p)
       THEN UNCHANGED <<copies, branchDb, seen>> /\ Finish(p, "error")
       ELSE /\ claim = NoProc
            /\ IF ~Marked
               THEN UNCHANGED <<copies, branchDb, seen>> /\ Finish(p, "no_base")
               ELSE IF branchDb[arg[p]].ex
               THEN UNCHANGED <<copies, branchDb, seen>> /\ Finish(p, "existing")
               ELSE IF Openish
               THEN UNCHANGED <<copies, branchDb, seen>> /\ Finish(p, "base_open")
               ELSE IF PoolTarget > 0 /\ ReadyPool # {}
               THEN \E c \in ReadyPool :
                      /\ copies' = copies \ {c}
                      /\ branchDb' = [branchDb EXCEPT ![arg[p]] = NewBranch(c, flag[p])]
                      /\ UNCHANGED seen
                      /\ Finish(p, "pool")
               ELSE /\ UNCHANGED <<copies, branchDb, claim, fillLock>>
                    /\ See(p)
                    /\ Step(p, "create")

(* CREATE DATABASE ... TEMPLATE, outside the claim lock except for base create: copy.go:25-51 *)
CopyCreate(p) ==
    LET isCreate == op[p] = "basecreate"
        srcOK == isCreate \/ (base.st # "absent" /\ sessions = 0)
        new(k) == [id |-> nextId, kind |-> k,
                   gen |-> seen[p].gen,
                   content |-> IF isCreate THEN 0 ELSE base.content,
                   old |-> FALSE]
    IN
    /\ pc[p] = "create"
    /\ UNCHANGED <<base, sessions, nextOid, sealed, branchDb, seen, dnext, rdrop>>
    /\ UNCHANGED <<Params, Counters, excused, stuck>>
    /\ IF ~Bad(p)
       THEN IF srcOK
            THEN /\ copies' = copies \cup {new("unmarked")}
                 /\ nextId' = nextId + 1
                 /\ cid' = [cid EXCEPT ![p] = nextId]
                 /\ UNCHANGED <<late, reported, claim, fillLock>>
                 /\ Step(p, "mark")
            ELSE /\ UNCHANGED <<copies, nextId, cid, late, reported>>
                 /\ Finish(p, "error")
       ELSE \/ /\ UNCHANGED <<copies, nextId, cid, late, reported>>
               /\ Finish(p, "error")
            \/ /\ srcOK
               \* The server finished the copy; recoverCopyMarker marks it and leaves it (copy.go:101-122).
               /\ copies' = copies \cup {new("tmp")}
               /\ nextId' = nextId + 1
               /\ reported' = reported \cup {nextId}
               /\ UNCHANGED <<cid, late>>
               /\ Finish(p, "error_left")
            \/ /\ srcOK /\ ~conn[p]
               \* The original backend is still copying when recovery looks (copy.go:115-117).
               /\ late' = late \cup {new("unmarked")}
               /\ nextId' = nextId + 1
               /\ reported' = reported \cup {nextId}
               /\ UNCHANGED <<copies, cid>>
               /\ Finish(p, "error_maybe")

(* COMMENT ON DATABASE with up to three attempts: copy.go:52-72, markNewCopy *)
CopyMark(p) ==
    LET after == CASE op[p] \in {"ensure", "reset"} -> "e_claim2"
                   [] op[p] = "fill"                -> "f_claim2"
                   [] op[p] = "basecreate"          -> "bcr2"
        marked == {IF c.id = cid[p] THEN [c EXCEPT !.kind = "tmp"] ELSE c : c \in copies}
    IN
    /\ pc[p] = "mark"
    /\ UNCHANGED <<base, sessions, nextOid, sealed, nextId, branchDb, late, seen, rdrop>>
    /\ UNCHANGED <<Params, Counters, excused, stuck>>
    /\ IF ~Bad(p)
       THEN /\ copies' = marked
            /\ UNCHANGED <<cid, dnext, reported, claim, fillLock>>
            /\ Step(p, after)
       ELSE \/ /\ copies' = marked
               /\ UNCHANGED <<cid, reported>>
               /\ ToDiscard(p, "error", "done")
            \/ /\ ~conn[p]
               \* Every attempt to reconnect failed; the copy stays unmarked (unmarkedCopyError).
               /\ reported' = reported \cup {cid[p]}
               /\ cid' = [cid EXCEPT ![p] = 0]
               /\ UNCHANGED <<copies, dnext>>
               /\ Finish(p, "error_unmarked")

(* discardCopy: mutation.go:158-176.  It uses a cleanup context and reconnects if needed. *)
Discard(p) ==
    /\ pc[p] = "discard"
    /\ UNCHANGED <<base, sessions, nextOid, sealed, nextId, branchDb, late, seen, dnext, rdrop>>
    /\ UNCHANGED <<Params, Counters, excused, stuck>>
    /\ cid' = [cid EXCEPT ![p] = 0]
    /\ \/ /\ copies' = copies \ OwnCopy(p)
          /\ UNCHANGED reported
       \/ /\ ~conn[p]
          /\ UNCHANGED copies
          /\ reported' = reported \cup {c.id : c \in OwnCopy(p)}
    /\ IF dnext[p] = "done"
       THEN Finish(p, res[p])
       ELSE Step(p, dnext[p]) /\ UNCHANGED <<claim, fillLock>>

(* ensure, second claim section: ensure.go:117-167 *)
EClaim2(p) ==
    /\ pc[p] = "e_claim2"
    /\ UNCHANGED <<base, sessions, nextOid, sealed, nextId, late, seen, rdrop>>
    /\ UNCHANGED <<Params, Counters, Ghosts>>
    /\ IF Bad(p)
       THEN UNCHANGED <<copies, branchDb, cid>> /\ ToDiscard(p, "error", "done")
       ELSE /\ claim = NoProc
            /\ IF ~Marked
               THEN UNCHANGED <<copies, branchDb, cid>> /\ ToDiscard(p, "no_base", "done")
               ELSE IF branchDb[arg[p]].ex
               THEN UNCHANGED <<copies, branchDb, cid>> /\ ToDiscard(p, "existing", "done")
               ELSE IF Openish
               THEN UNCHANGED <<copies, branchDb, cid>> /\ ToDiscard(p, "base_open", "done")
               ELSE IF ~SeenCurrent(p)
               THEN UNCHANGED <<copies, branchDb, cid>> /\ ToDiscard(p, "", "e_claim1")
               ELSE IF OwnCopy(p) # {}
               THEN \E c \in OwnCopy(p) :
                      /\ copies' = copies \ {c}
                      /\ branchDb' = [branchDb EXCEPT ![arg[p]] = NewBranch(c, flag[p])]
                      /\ cid' = [cid EXCEPT ![p] = 0]
                      /\ UNCHANGED dnext
                      /\ Finish(p, "copy")
               ELSE \* The copy was swept by base close, gc or pool drain: retry.
                    /\ UNCHANGED <<copies, branchDb, dnext, claim, fillLock>>
                    /\ cid' = [cid EXCEPT ![p] = 0]
                    /\ Step(p, "e_claim1")

(* reset: lifecycle.go:66-97 *)
R1(p) ==
    /\ pc[p] = "r1"
    /\ UNCHANGED <<base, sessions, nextOid, sealed, copies, nextId, late, seen, cid, dnext>>
    /\ UNCHANGED <<Params, Counters, Ghosts>>
    /\ IF Bad(p)
       THEN UNCHANGED <<branchDb, rdrop>> /\ Finish(p, "error")
       ELSE /\ claim = NoProc
            /\ IF ~Marked
               THEN UNCHANGED <<branchDb, rdrop>> /\ Finish(p, "no_base")
               ELSE IF Openish
               THEN UNCHANGED <<branchDb, rdrop>> /\ Finish(p, "base_open")
               ELSE /\ rdrop' = [rdrop EXCEPT ![p] = branchDb[arg[p]].ex]
                    /\ branchDb' = [branchDb EXCEPT ![arg[p]] = NoBranch]
                    /\ UNCHANGED <<claim, fillLock>>
                    /\ Step(p, "e_claim1")

(* drop: lifecycle.go:48-64 *)
Drop(p) ==
    /\ pc[p] = "drop"
    /\ UNCHANGED <<base, sessions, nextOid, sealed, copies, nextId, late, seen, cid, dnext, rdrop>>
    /\ UNCHANGED <<Params, Counters, Ghosts>>
    /\ IF Bad(p)
       THEN UNCHANGED branchDb /\ Finish(p, "error")
       ELSE /\ claim = NoProc
            /\ IF ~Marked
               THEN UNCHANGED branchDb /\ Finish(p, "no_base")
               ELSE IF branchDb[arg[p]].ex
               THEN branchDb' = [branchDb EXCEPT ![arg[p]] = NoBranch] /\ Finish(p, "dropped")
               ELSE UNCHANGED branchDb /\ Finish(p, "not_found")

(* pool fill: pool.go:48-148 *)
FLock(p) ==
    /\ pc[p] = "f_lock"
    /\ UNCHANGED <<base, sessions, nextOid, sealed, copies, nextId, branchDb, late, seen, cid, dnext, rdrop>>
    /\ UNCHANGED <<Params, Counters, Ghosts>>
    /\ IF Bad(p) THEN Finish(p, "error")
       ELSE IF fillLock # NoProc THEN Finish(p, "skipped")
       ELSE fillLock' = p /\ UNCHANGED claim /\ Step(p, "f_claim1")

FClaim1(p) ==
    /\ pc[p] = "f_claim1"
    /\ UNCHANGED <<base, sessions, nextOid, sealed, copies, nextId, branchDb, late, cid, dnext, rdrop>>
    /\ UNCHANGED <<Params, Counters, Ghosts>>
    /\ IF Bad(p)
       THEN UNCHANGED seen /\ Finish(p, "error")
       ELSE /\ claim = NoProc
            /\ IF ~Marked THEN UNCHANGED seen /\ Finish(p, "no_base")
               ELSE IF Openish THEN UNCHANGED seen /\ Finish(p, "base_open")
               ELSE IF Cardinality(ReadyPool) >= PoolTarget THEN UNCHANGED seen /\ Finish(p, "filled")
               ELSE See(p) /\ UNCHANGED <<claim, fillLock>> /\ Step(p, "create")

FClaim2(p) ==
    /\ pc[p] = "f_claim2"
    /\ UNCHANGED <<base, sessions, nextOid, sealed, nextId, branchDb, late, seen, rdrop>>
    /\ UNCHANGED <<Params, Counters, Ghosts>>
    /\ IF Bad(p)
       THEN UNCHANGED <<copies, cid>> /\ ToDiscard(p, "error", "done")
       ELSE /\ claim = NoProc
            /\ IF ~Marked \/ Openish
               THEN UNCHANGED <<copies, cid>> /\ ToDiscard(p, "error", "done")
               ELSE IF ~SeenCurrent(p)
               THEN UNCHANGED <<copies, cid>> /\ ToDiscard(p, "", "f_claim1")
               ELSE /\ copies' = {IF c \in OwnCopy(p) THEN [c EXCEPT !.kind = "pool"] ELSE c : c \in copies}
                    /\ cid' = [cid EXCEPT ![p] = 0]
                    /\ UNCHANGED <<dnext, claim, fillLock>>
                    /\ Step(p, "f_claim1")

(* base open: base.go:188-207 *)
BaseOpen(p) ==
    /\ pc[p] = "bo"
    /\ UNCHANGED <<sessions, nextOid, sealed, copies, nextId, branchDb, late, seen, cid, dnext, rdrop>>
    /\ UNCHANGED <<Params, Counters, Ghosts>>
    /\ IF Bad(p) THEN UNCHANGED base /\ Finish(p, "error")
       ELSE /\ claim = NoProc
            /\ IF ~Marked THEN UNCHANGED base /\ Finish(p, "no_base")
               ELSE base' = [base EXCEPT !.open = TRUE, !.allow = TRUE] /\ Finish(p, "ok")

(* base close, first write: base.go:193-211 and closeBase, base.go:230-244 *)
BC1(p) ==
    /\ pc[p] = "bc1"
    /\ UNCHANGED <<nextOid, sealed, copies, nextId, branchDb, late, cid, dnext, rdrop>>
    /\ UNCHANGED <<Params, Counters, Ghosts>>
    /\ IF Bad(p) THEN UNCHANGED <<base, sessions, seen>> /\ Finish(p, "error")
       ELSE /\ claim = NoProc
            /\ IF ~Marked THEN UNCHANGED <<base, sessions, seen>> /\ Finish(p, "no_base")
               ELSE IF Closed THEN UNCHANGED <<base, sessions, seen>> /\ Finish(p, "noop")
               ELSE /\ claim' = p /\ UNCHANGED fillLock
                    /\ sessions' = IF flag[p] THEN 0 ELSE sessions
                    /\ base' = [base EXCEPT !.open = TRUE, !.allow = FALSE]
                    /\ See(p)
                    /\ Step(p, "bc2")

(* base adopt, first write: base.go:136-178 and closeBase with adopt *)
BA1(p) ==
    /\ pc[p] = "ba1"
    /\ UNCHANGED <<nextOid, sealed, copies, nextId, branchDb, late, cid, dnext, rdrop>>
    /\ UNCHANGED <<Params, Counters, Ghosts>>
    /\ IF Bad(p) THEN UNCHANGED <<base, sessions, seen>> /\ Finish(p, "error")
       ELSE /\ claim = NoProc
            /\ IF base.st = "absent" THEN UNCHANGED <<base, sessions, seen>> /\ Finish(p, "no_base")
               ELSE IF Marked /\ Openish THEN UNCHANGED <<base, sessions, seen>> /\ Finish(p, "base_open")
               ELSE IF Marked THEN UNCHANGED <<base, sessions, seen>> /\ Finish(p, "noop")
               ELSE /\ claim' = p /\ UNCHANGED fillLock
                    /\ sessions' = IF flag[p] THEN 0 ELSE sessions
                    /\ base' = [base EXCEPT !.st = "marked", !.open = TRUE, !.allow = FALSE, !.gen = FirstGen]
                    /\ seen' = [seen EXCEPT ![p] = [gen |-> FirstGen, oid |-> base.oid]]
                    /\ Step(p, "bc2")

(* checkClosedSessions: base.go:306-337 *)
BC2(p) ==
    /\ pc[p] = "bc2"
    /\ UNCHANGED <<base, nextOid, sealed, copies, nextId, branchDb, late, seen, cid, dnext, rdrop>>
    /\ UNCHANGED <<Params, Counters, Ghosts, claim, fillLock>>
    /\ IF Bad(p) THEN UNCHANGED sessions /\ StepR(p, "brestore", "error")
       ELSE IF flag[p] THEN sessions' = 0 /\ Step(p, "bc3")
       ELSE IF sessions = 0 THEN UNCHANGED sessions /\ Step(p, "bc3")
       ELSE UNCHANGED sessions /\ StepR(p, "brestore", "in_use")

BaseMatch(p) == Closing /\ SeenCurrent(p)    \* checkMutation, mutation.go:24-53

(* finishBaseClose, final write, then the stale pool sweep: base.go:255-274, 194-201, gc.go:39-64 *)
BC3(p) ==
    LET ng == IF op[p] = "adopt" THEN base.gen ELSE base.gen + 1
        swept == {c \in copies : ~(c.kind \in {"pool", "tmp"} /\ c.gen < ng)}
    IN
    /\ pc[p] = "bc3"
    /\ UNCHANGED <<sessions, nextOid, nextId, branchDb, late, seen, cid, dnext, rdrop>>
    /\ UNCHANGED <<Params, Counters, Ghosts>>
    /\ IF ~Bad(p) /\ BaseMatch(p)
       THEN /\ base' = [base EXCEPT !.open = FALSE, !.gen = ng]
            /\ sealed' = [sealed EXCEPT ![ng] = base.content]
            /\ \/ copies' = swept
               \/ "loss" \in Faults /\ UNCHANGED copies     \* the sweep failed and became a warning
            /\ Finish(p, "closed")
       ELSE /\ UNCHANGED <<base, sealed, copies, claim, fillLock>>
            /\ StepR(p, "brestore", "error")

(* finishBaseClose, restore: base.go:278-303.  Runs on a new connection, without the lock, after a loss. *)
BRestore(p) ==
    /\ pc[p] = "brestore"
    /\ UNCHANGED <<sessions, nextOid, sealed, copies, nextId, branchDb, late, seen, cid, dnext, rdrop>>
    /\ UNCHANGED <<Params, Counters, reported, excused>>
    /\ \/ /\ BaseMatch(p)
          /\ base' = IF op[p] = "adopt"
                     THEN [base EXCEPT !.st = "unmarked", !.open = FALSE, !.allow = TRUE, !.gen = 0]
                     ELSE [base EXCEPT !.allow = TRUE]
          /\ UNCHANGED stuck
          /\ Finish(p, res[p])
       \/ /\ ~BaseMatch(p) \/ ~conn[p]
          /\ UNCHANGED base
          /\ stuck' = TRUE
          /\ Finish(p, "restore_failed")

(* base create: base.go:53-134.  The claim lock is held from the first lookup to the rename. *)
BCR1(p) ==
    /\ pc[p] = "bcr1"
    /\ UNCHANGED <<base, sessions, nextOid, sealed, copies, nextId, branchDb, late, cid, dnext, rdrop>>
    /\ UNCHANGED <<Params, Counters, Ghosts>>
    /\ IF Bad(p) THEN UNCHANGED seen /\ Finish(p, "error")
       ELSE /\ claim = NoProc
            /\ IF base.st # "absent" THEN UNCHANGED seen /\ Finish(p, "exists")
               ELSE /\ claim' = p /\ UNCHANGED fillLock
                    /\ seen' = [seen EXCEPT ![p] = [gen |-> FirstGen, oid |-> 0]]
                    /\ Step(p, "create")

BCR2(p) ==
    /\ pc[p] = "bcr2"
    /\ UNCHANGED <<sessions, nextId, branchDb, late, seen, rdrop>>
    /\ UNCHANGED <<Params, Counters, Ghosts>>
    /\ IF ~Bad(p) /\ base.st = "absent" /\ OwnCopy(p) # {}
       THEN \E c \in OwnCopy(p) :
              /\ base' = [st |-> "marked", open |-> FALSE, allow |-> FALSE, gen |-> c.gen,
                          content |-> c.content, oid |-> nextOid]
              /\ nextOid' = nextOid + 1
              /\ sealed' = [sealed EXCEPT ![c.gen] = c.content]
              /\ copies' = copies \ {c}
              /\ cid' = [cid EXCEPT ![p] = 0]
              /\ UNCHANGED dnext
              /\ Finish(p, "created")
       ELSE /\ UNCHANGED <<base, nextOid, sealed, copies, cid>>
            /\ ToDiscard(p, "error", "done")

(* gc: gc.go:25-64 *)
GcEligible(c) ==
    \/ c.kind = "pool" /\ c.gen < base.gen
    \/ c.kind = "tmp" /\ (c.gen < base.gen \/ (c.gen = base.gen /\ c.old))

GC(p) ==
    /\ pc[p] = "gc"
    /\ UNCHANGED <<base, sessions, nextOid, sealed, nextId, late, seen, cid, dnext, rdrop>>
    /\ UNCHANGED <<Params, Counters, Ghosts>>
    /\ IF Bad(p) THEN UNCHANGED <<copies, branchDb>> /\ Finish(p, "error")
       ELSE /\ claim = NoProc
            /\ IF ~Marked THEN UNCHANGED <<copies, branchDb>> /\ Finish(p, "no_base")
               ELSE /\ copies' = {c \in copies : ~GcEligible(c)}
                    /\ branchDb' = [b \in Branches |->
                                      IF branchDb[b].ex /\ branchDb[b].exp = "expired" THEN NoBranch ELSE branchDb[b]]
                    /\ Finish(p, "ok")

(* pool drain: pool.go:173-191 *)
Drain(p) ==
    /\ pc[p] = "drain"
    /\ UNCHANGED <<base, sessions, nextOid, sealed, nextId, branchDb, late, seen, cid, dnext, rdrop>>
    /\ UNCHANGED <<Params, Counters, Ghosts>>
    /\ IF Bad(p) THEN UNCHANGED copies /\ Finish(p, "error")
       ELSE /\ claim = NoProc
            /\ IF ~Marked THEN UNCHANGED copies /\ Finish(p, "no_base")
               ELSE copies' = {c \in copies : c.kind \notin {"pool", "tmp"}} /\ Finish(p, "ok")

ProcStep(p) ==
    \/ Start(p) \/ EClaim1(p) \/ CopyCreate(p) \/ CopyMark(p) \/ Discard(p) \/ EClaim2(p)
    \/ R1(p) \/ Drop(p) \/ FLock(p) \/ FClaim1(p) \/ FClaim2(p)
    \/ BaseOpen(p) \/ BC1(p) \/ BA1(p) \/ BC2(p) \/ BC3(p) \/ BRestore(p)
    \/ BCR1(p) \/ BCR2(p) \/ GC(p) \/ Drain(p)

---------------------------------------------------------------------------
(* Faults *)

PV == <<op, arg, flag, seen, cid, dnext, rdrop>>
DB == <<base, sessions, nextOid, sealed, copies, nextId, branchDb, late>>

Cancel(p) ==
    /\ "cancel" \in Faults /\ faultsLeft > 0 /\ Running(p) /\ ~canc[p]
    /\ canc' = [canc EXCEPT ![p] = TRUE]
    /\ faultsLeft' = faultsLeft - 1
    /\ UNCHANGED <<DB, PV, claim, fillLock, pc, conn, res, opsLeft, extLeft, Ghosts, resetLost>>

(* Advisory locks are session-scoped, so a lost connection releases them. *)
ConnLoss(p) ==
    /\ "loss" \in Faults /\ faultsLeft > 0 /\ Running(p) /\ conn[p]
    /\ conn' = [conn EXCEPT ![p] = FALSE]
    /\ ReleaseLocks(p)
    /\ faultsLeft' = faultsLeft - 1
    /\ UNCHANGED <<DB, PV, pc, canc, res, opsLeft, extLeft, Ghosts, resetLost>>

Crash(p) ==
    LET srcOK == op[p] = "basecreate" \/ (base.st # "absent" /\ sessions = 0)
        orphan == [id |-> nextId, kind |-> "unmarked",
                   gen |-> seen[p].gen,
                   content |-> IF op[p] = "basecreate" THEN 0 ELSE base.content, old |-> FALSE]
    IN
    /\ "crash" \in Faults /\ faultsLeft > 0 /\ Running(p)
    /\ faultsLeft' = faultsLeft - 1
    /\ stuck' = (stuck \/ pc[p] \in {"bc2", "bc3", "brestore"})
    /\ \/ /\ UNCHANGED <<copies, nextId>>
          /\ excused' = excused \cup (IF cid[p] = 0 THEN {} ELSE {cid[p]})
       \/ /\ pc[p] = "create" /\ srcOK
          /\ copies' = copies \cup {orphan}
          /\ nextId' = nextId + 1
          /\ excused' = excused \cup {nextId}
    /\ UNCHANGED <<base, sessions, nextOid, sealed, branchDb, late, PV, conn, canc, opsLeft, extLeft, reported>>
    /\ Finish(p, "crashed")

---------------------------------------------------------------------------
(* Environment *)

EnvU == <<claim, fillLock, pc, op, arg, flag, seen, cid, conn, canc, res, dnext, rdrop,
          opsLeft, faultsLeft, reported, excused, stuck, resetLost>>

Connect ==
    /\ base.st # "absent" /\ base.allow /\ sessions = 0
    /\ sessions' = 1
    /\ UNCHANGED <<base, nextOid, sealed, copies, nextId, branchDb, late, extLeft, EnvU>>

Disconnect ==
    /\ sessions = 1
    /\ sessions' = 0
    /\ UNCHANGED <<base, nextOid, sealed, copies, nextId, branchDb, late, extLeft, EnvU>>

Migrate ==
    /\ sessions = 1 /\ base.content < MaxContent
    /\ base' = [base EXCEPT !.content = base.content + 1]
    /\ UNCHANGED <<sessions, nextOid, sealed, copies, nextId, branchDb, late, extLeft, EnvU>>

(* More than one hour passes for a temporary copy; a branch TTL expires. *)
Age ==
    /\ \E c \in copies :
         /\ c.kind = "tmp" /\ ~c.old
         /\ copies' = (copies \ {c}) \cup {[c EXCEPT !.old = TRUE]}
    /\ UNCHANGED <<base, sessions, nextOid, sealed, nextId, branchDb, late, extLeft, EnvU>>

Expire ==
    /\ \E b \in Branches :
         /\ branchDb[b].ex /\ branchDb[b].exp = "live"
         /\ branchDb' = [branchDb EXCEPT ![b].exp = "expired"]
    /\ UNCHANGED <<base, sessions, nextOid, sealed, copies, nextId, late, extLeft, EnvU>>

LateCreate ==
    /\ \E c \in late :
         /\ late' = late \ {c}
         /\ \/ copies' = copies \cup {c}
            \/ UNCHANGED copies
    /\ UNCHANGED <<base, sessions, nextOid, sealed, nextId, branchDb, extLeft, EnvU>>

ExternalDrop ==
    /\ External /\ extLeft > 0 /\ base.st # "absent" /\ sessions = 0
    /\ base' = AbsentBase
    /\ extLeft' = extLeft - 1
    /\ UNCHANGED <<sessions, nextOid, sealed, copies, nextId, branchDb, late, EnvU>>

ExternalCreate ==
    /\ External /\ extLeft > 0 /\ base.st = "absent"
    /\ \E c \in 0..MaxContent :
         base' = [AbsentBase EXCEPT !.st = "unmarked", !.allow = TRUE, !.content = c, !.oid = nextOid]
    /\ nextOid' = nextOid + 1
    /\ extLeft' = extLeft - 1
    /\ UNCHANGED <<sessions, sealed, copies, nextId, branchDb, late, EnvU>>

Env == Connect \/ Disconnect \/ Migrate \/ Age \/ Expire \/ LateCreate \/ ExternalDrop \/ ExternalCreate

Terminal == Quiescent /\ opsLeft = 0 /\ UNCHANGED vars

Next == (\E p \in Procs : ProcStep(p) \/ Cancel(p) \/ ConnLoss(p) \/ Crash(p)) \/ Env \/ Terminal

Spec == Init /\ [][Next]_vars

Bounded == base.gen <= MaxGen /\ nextId <= MaxCopies + 1

---------------------------------------------------------------------------
(* Invariants *)

TypeOK ==
    /\ base.st \in {"absent", "unmarked", "marked"}
    /\ sessions \in 0..1
    /\ claim \in Procs \cup {NoProc} /\ fillLock \in Procs \cup {NoProc}
    /\ \A c \in copies : c.kind \in {"unmarked", "tmp", "pool"}
    /\ \A p \in Procs : cid[p] \in 0..(MaxCopies + 1)

(* The claim lock is held only by a live process inside a multi-step section. *)
MutexOK ==
    /\ claim # NoProc => conn[claim] /\ pc[claim] \in {"bc2", "bc3", "brestore", "create", "mark", "bcr2", "discard"}
    /\ fillLock # NoProc => conn[fillLock] /\ op[fillLock] = "fill" /\ Running(fillLock)

(* A closed base has no client session, so its content cannot change. *)
BaseProtected == Closed => sessions = 0
SealedBase    == Closed => base.content = sealed[base.gen]

(* A marker that says closed never sits on a base that accepts connections. *)
MarkerAgrees  == Marked /\ ~base.open => ~base.allow

(* A pool or branch database holds exactly the content the base had in the generation it is stamped with. *)
Fresh ==
    /\ \A c \in copies : c.kind = "pool" => c.content = sealed[c.gen]
    /\ \A b \in Branches : branchDb[b].ex => branchDb[b].content = sealed[branchDb[b].gen]

(* Weaker form for runs where the base is recreated outside pgcheckout. *)
FreshCurrent ==
    Closed =>
      /\ \A c \in ReadyPool : c.content = base.content
      /\ \A b \in Branches : branchDb[b].ex /\ branchDb[b].gen = base.gen => branchDb[b].content = base.content

NoFutureGen ==
    Marked =>
      /\ \A c \in copies : c.kind = "pool" => c.gen <= base.gen
      /\ \A b \in Branches : branchDb[b].ex => branchDb[b].gen <= base.gen

PoolBound == Marked => Cardinality(ReadyPool) <= PoolTarget

(* Without faults, an unmarked copy exists only between CREATE and COMMENT of its owner. *)
UnmarkedTransient ==
    Faults = {} => \A c \in copies : c.kind = "unmarked" => \E p \in Procs : pc[p] = "mark" /\ cid[p] = c.id

(* Whatever is left when everything has stopped was named in an error, or its owner crashed. *)
NoSilentLeak ==
    Quiescent => \A c \in copies : c.kind \in {"unmarked", "tmp"} => c.id \in reported \cup excused

(* The base is not left half closed without a report. *)
ClosingReported == Quiescent /\ Closing => stuck

(* Expected to be violated: reset is a drop followed by an ensure, not one step. *)
ResetAtomic == ~resetLost

---------------------------------------------------------------------------
(* Action properties *)

SealedImmutable == [][\A g \in DOMAIN sealed : sealed[g] # -1 => sealed'[g] = sealed[g]]_vars
BaseKept        == [][base.st # "absent" => base'.st # "absent"]_vars
GenMonotonic    == [][base.st = "marked" /\ base'.st = "marked" => base'.gen >= base.gen]_vars
BranchStable    == [][\A b \in Branches : branchDb[b].ex /\ branchDb'[b].ex =>
                        /\ branchDb'[b].gen = branchDb[b].gen
                        /\ branchDb'[b].content = branchDb[b].content]_vars

---------------------------------------------------------------------------
(* Reachability probes: each is expected to be violated, which shows the state is reachable. *)

ProbePool      == \A p \in Procs : res[p] # "pool"
ProbeCopy      == \A p \in Procs : res[p] # "copy"
ProbeRetry     == \A p \in Procs : ~(pc[p] = "discard" /\ dnext[p] = "e_claim1")
ProbeInUse     == \A p \in Procs : res[p] # "in_use"
ProbeLeft      == \A p \in Procs : res[p] # "error_left"
ProbeCreated   == \A p \in Procs : res[p] # "created"
ProbeGen3      == base.gen < 3
ProbeRestoreFailed == \A p \in Procs : res[p] # "restore_failed"
ProbeMaybe     == \A p \in Procs : res[p] # "error_maybe"
ProbeUnmarked  == \A p \in Procs : res[p] # "error_unmarked"
ProbeOrphan    == \A c \in copies : ~(c.kind = "unmarked" /\ c.id \in excused)
ProbeRecreatedFresh == ~(base.oid > 1 /\ Closed /\ \E b \in Branches : branchDb[b].ex /\ branchDb[b].gen = base.gen)
ProbeRecreatedStale == ~(base.oid > 1 /\ Marked /\ \E b \in Branches : branchDb[b].ex /\ branchDb[b].gen < base.gen)
ProbeUnlockedRestore == \A p \in Procs : ~(pc[p] = "brestore" /\ ~conn[p] /\ claim \notin {NoProc, p})
=============================================================================
