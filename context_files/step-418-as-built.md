# Step 418 — as built

**A source keeps its schema, but not a caller's cancellation.** H2 of
`v0.7.2-scope.md` (the scope's suspicion of the cached node, in its real form,
confirmed by its prober and H8's reading), the plan's 426, decided as D13
recommends.

## 1. What was wrong

The CSV, Parquet, NDJSON and files (IPC dataset) sources read their schema once, in a
`sync.Once`, and kept whatever the first read gave:

- **One cancelled Collect poisoned the LazyFrame:** a client that went away while its
  query opened the file left the source's `Once` done with its `context.Canceled`,
  and every later `Collect` of the same LazyFrame, a service's shared query, failed
  with it for good.
- **A caller waiting on another's read ignored its own deadline:** `sync.Once` makes
  every caller wait for the first, so a request with 100 ms waited as long as the
  first read took.

## 2. The change

- **`source.Once`** (`internal/source/once.go`): one run at a time; a waiter gives up
  when its own context ends; an answer is kept, a failure among them, but for a run
  that failed because its caller's context ended, which the next caller runs again; a
  panic passes through, keeps nothing and releases the waiters.
- **The four sources' `Schema`** run their reads under it, their panics recovered into
  the read's answer as before. Their `err` fields are gone; what a read writes to the
  source is read after `Do` returns, ordered by its lock.
- **Not changed:** a transient open error is still kept, as D13 recommends; the Arrow
  source checks its caller's context before its own `Once` and is not affected.

## 3. Tests

- **`TestACancelledCollectDoesNotPoisonTheFrame`** (root): CSV, NDJSON and Parquet from
  openers whose first open cancels its caller, as the prober's did: the first `Collect`
  fails with `context.Canceled`, the next reads 50 rows, where it failed for good.
- **`TestAWaiterKeepsItsOwnDeadline`** (root): a second `Collect` under 100 ms, while the
  first's open is held, returns its deadline, where it outlived it by 5 s.
- **`source.Once`'s own** (in the package, under `-race` too): sixteen callers, one run,
  a failure kept; a cancellation not kept; a 50 ms waiter's deadline, a patient
  waiter's answer; a panic passing through and keeping nothing.
- **`TestSchemaIsNotPoisonedByACancellation`** (`files`): a dataset over a fake file
  whose first schema read sees its caller go away.

**Teeth: 3 of 3 bite:** cancellations kept fails the root, `Once` and files tests;
waiters ignoring their contexts fails the root and `Once` tests; a panic keeping its
nil fails `Once`'s.

**Gate:** the source packages' tests; the root package's reader, schema, panic,
cancellation, hive and dataset tests; vet and levels. CI's every width, the scalar
build and the race detector on the pull request.
