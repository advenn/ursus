# Step 403 — as built

**A query's numbers, to a hook.** C3 of `v0.8-scope.md`.

## 1. What was wrong

A service could read its engine's figures now (`Engine.Stats`, step 395), a query's
own if it asked for them (`WithMemoryStats`), and a query's plan, timed node by node
(`Profile`). Nothing heard each query as it ended: a histogram of a service's query
durations, its spills or its failures by kind had to wrap every call that ran one.

## 2. How

- **`WithEngineQueryHook(fn)`**, an engine option: fn is called as each query on the
  engine ends, with the query's context, which carries what the service put in it,
  a request's route or a trace's span, and its **`QueryStats`**: `Op`, how it was
  run, as its errors name it ("collect", "count", "sink_parquet"...); `Duration`, from
  the call to its end, planning and all; `Rows`, those it gave; `Peak`, its
  operators' most, as `MemoryStats.Peak`; `Spills`; `Threads`; and `Err`, an
  `*Error` or its context's. ursus imports no logging or tracing package.
- **Once a query, a failed one included,** on the goroutine that ran it, before the
  call returns. Every entry point that runs one, `Collect`, `Count`, `Profile` and
  the writers, defers **`collectCfg.end`** first, on a cfg declared before it is
  filled, in place of `uerr.Catch`: it recovers a panic into the error as `Catch`
  did, and reports after `done` has left the engine, so the hook sees the error a
  panic became. `CollectBatches` reports from a deferred function of its own, without
  recovering, as a range function may not recover around its loop's body; the error
  it yielded is the report's.
- **Rows** are counted by `counted`, a wrapper on the root `compile` returns, only on
  an engine with a hook. The figures live in `collectCfg.query`, a pointer `finish`
  makes, as `done`'s receiver is a copy.
- **Not reported:** what reads a schema alone (`CollectSchema`, `Columns`, `DTypes`,
  `Width`, `Explain`), and a `Describe` whose schema fails before it runs a query;
  what a `DataFrame` does without a query.

## 3. Measured

A 1,000-row group-by on an engine with a hook that does nothing, against one with
none, 3,000 runs each twice, alternated: 267 allocations a query where 266, the
wrapper; its time 29.4 to 35.6 µs where 27.3 to 30.8, within the machine's noise,
a second pair 30.6 and 31.8 against 30.7 and 30.8. With no hook nothing is wrapped.

## 4. Tests

- **`TestEveryQueryIsReportedOnce`** (root): every method of `*LazyFrame` that runs a
  query, found by reflection as `TestEveryEntryPointRecovers` finds them, run on the
  default engine with a hook, through `SetDefaultEngine`: on five rows, one report,
  no error, its rows, the request's context; on a plan that panics as it is
  resolved, one report whose error is the one the method returned, internal. The
  five that read a schema report none; `Describe` none of the panicking plan.
- **`TestAHookIsToldWhatAQueryDid`**: on an engine of four threads, a 1,000-row
  group-by is 10 rows on one thread, a peak, no spills and a duration under a
  minute; a 200,000-row sort under 256 KiB of its own spills on four threads; a stream
  broken off after a batch reports once, its rows the batch's; a cancelled query
  `context.Canceled`; an unknown column a `KindSchema` error and no rows; a
  partitioned Parquet write its 1,000 rows; a query on another engine nothing.
- **`ExampleWithEngineQueryHook`**: a `Collect` and a `Count` of three groups.

**Teeth: 12 of 12 bite:** the rows not counted; no recover, which
`TestEveryEntryPointRecovers` bites; the error not reported; a stream's error not
reported; no peak; no spills; no threads; the hook not kept; no start, which the
minute bites; not the query's context; a stream not reported; `Count` not reported.

**Gate:** the light gate, 29 packages ok, levels, vet ×3 and the bench engine tests;
the 22 PDS-H answers match DuckDB's at SF=0.1; `make api`, none gone.
