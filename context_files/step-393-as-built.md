# Step 393 — as built

**A goroutine's panic relayed to the function that started it.** A1 of
`v0.8-scope.md`.

## 1. What was wrong

Every entry point that runs a query recovers a panic and returns it as `ErrInternal`
(`lazy.go:684`, step 72), and every goroutine the engine starts recovers its own;
`TestEveryGoroutineIsCovered` held every `go` statement to it. A WaitGroup's `Go`
starts a goroutine too, and the test did not look at it. Ten sites so started
recovered nothing:

- `ranges` (`kernel/parsort.go`), the parallel sort's pieces (step 361);
- a text sort's runs (`kernel/textsort.go`);
- first-seen keys' parts and their renumbering (`kernel/firstseen.go`);
- a Spearman rank's second column (`kernel/aggrank.go`);
- a sort's pieces (`eachPiece`, `physical/sort.go`);
- a unique's key tables and its ranges (`physical/uniquekeys.go`);
- `PartitionBy`'s parts, `ToDummies`' columns and `Corr`'s through `inParallel`.

A panic in any of them, an index out of range on a corrupt column, a bug, ended the
process: in a service, every request it was serving. `PartitionBy`, `ToDummies` and
`Corr` did not recover at their entry either. Step 389's `spreader` recovered, but
raised the bare value again, so the error named the line that raised it again, not
the one that panicked. Six other sites recovered in their own bodies, and do still.

## 2. How

- **`uerr.Relay`** (`internal/uerr/relay.go`): `Go(wg, fn)` runs fn on a goroutine of
  wg's and keeps the first panic among them; `Raise`, once the WaitGroup is done,
  panics with it in the caller, where the query's recovery is. A panic is kept as it
  is where it is a `Raise` or ursus's own internal error, else recorded where it was
  raised, its site and stack the goroutine's, which `FromPanic` and `Attributed` read
  (`panicOf`) in place of the second raise's.
- **The ten sites** start their goroutines through a Relay, `spreader` too; the three
  frame operations recover at their entry, as `Gather` does.
- **`TestEveryGoroutineIsCovered`** counts a WaitGroup's `Go`, of one function, as a
  site unless its body recovers: a deferred `recover`, a `uerr.Guard` or `GuardErr`,
  or one call to a function of its package that defers a `recover`. A Relay's `Go`
  takes the WaitGroup and the function, so is not one.
- **The probe** of the scope is `bench/micro/svcprobe`, with `pl_lat.py`, Polars'
  twin of its latencies, where it lived in the scratchpad.

Not timed: a deferred recover a goroutine, on goroutines that each work 65,536 rows
or a table's part.

## 3. Tests

- **`TestARelayRaisesItsGoroutinesPanic`** (`uerr`): a panic on one of three
  goroutines reaches the caller's `Catch` as an internal error under the caller's
  operation, its site and stack the goroutine's; a runtime error keeps its type, a
  `Raise` its kind; of four panics one; without one, nothing.
- **`TestAPiecesPanicReachesTheCaller`** (`kernel`): `ranges` and a spreader, a
  piece panicking, the site the piece's.
- **`TestAPhysicalPiecesPanicReachesTheCaller`** (`physical`): `eachPiece`,
  `eachRange` and a unique's `eachPart`.
- **`TestAFrameOperationsPanicIsAnError`** (root): `PartitionBy` and `ToDummies` of a
  frame whose last row of text runs past its characters, and `inParallel` panicking,
  each an internal error.
- **`TestConcatOwnedNIsConcat`** (step 389) recovered the spreader's panic by hand and
  compared its bare value; it reads it through `uerr.GuardErr` now, as a query does.

**Teeth: 6 of 7 bite:** a bare `wg.Go` in `ranges`, which the lint finds alone, and
which ends the kernel's test binary; a piece's panic not raised; a relayed panic
recorded at its second raise; a relayed panic's record not read; `PartitionBy` not
recovering. Silent: the lint taking every `Go` for recovered, which on this code has
no site left to miss; the first tooth is the lint's proof.

**Gate:** the light gate, one package at a time on four procs: the tests at v3, 28
packages ok; levels, vet ×3 and the bench engine tests clean; the 22 PDS-H answers
match DuckDB. `./internal/...` and the root package ran whole before it, where step
389's test failed as above, and the gate ran again after its change.
