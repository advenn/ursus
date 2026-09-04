# Step 5 — as built

Predicate pushdown through joins, and in-memory parallel execution. **v0.1 is
complete.** Authoritative where it disagrees with
[`step-4-as-built.md`](./step-4-as-built.md) and the design docs.

**550 tests green** under `GOEXPERIMENT=simd` × `GODEBUG=simd={512,256,128,0}`,
with the experiment off, and under `-race` — which for the first time proves
something, because before this step the library started no goroutines at all.

```
BenchmarkPipelineThreads/threads=1    37.6 ms/op
BenchmarkPipelineThreads/threads=2    20.5 ms/op
BenchmarkPipelineThreads/threads=4    13.3 ms/op
BenchmarkPipelineThreads/threads=8    12.7 ms/op     2.97x, 1M rows, 4 cores + HT
```

---

## 1. Parallelism is order-preserving, and that is the whole design

The serial driver had always supplied an implicit total ordering — `exec.Collect`'s
`append` is what made output batch order equal input batch order. **Five things
depended on it and none of them said so:**

| | what breaks if order is lost |
| --- | --- |
| `sortSink` | stability — ties stop keeping their input order |
| `positionAcc` | `First`/`Last` are POSITIONAL |
| `distinctOp` | keeps the FIRST row per key |
| `limitOp` | *which* rows survive, not just how many |
| `hashAggSink` | group ids are assigned in first-appearance order |

None of the five would error. They would return different rows. So the obligation
is now written into the `Operator` contract, with that list — a scheduler may run
operators concurrently, but it may not reorder what they produce.

### How the ordering is structural rather than restored

One dispatcher pulls source batches serially and hands them to workers
**round-robin**; the consumer reads results back round-robin in the same order.
Batch *i* goes to worker *i mod N* and is read at position *i*. No sequence
numbers, no reorder buffer, no heap — the order falls out of the topology, and the
"one closed lane means end of stream" rule is sound because round-robin dispatch is
monotone.

It also self-bounds. Each worker has a two-deep queue, so a slow morsel stalls its
own lane instead of letting the scan run ahead and materialise the whole input.
That is the difference between "parallel" and "parallel until it runs out of
memory".

### What is parallel, and what deliberately is not

**Parallel:** the BatchOps — filter, project, with_columns — over a source.

**Not parallel:** pulling from the source, which happens in the dispatcher. That is
the right split: reading a batch is I/O or a slice bump, evaluating expressions over
it is the CPU work. A side effect worth noting is that this does not actually depend
on sources being concurrency-safe, though all four are.

**Not parallel:** every pipeline breaker and its stateful consumers. `parallelise`
wraps a chain of `stage`s over a base and returns everything else untouched.

That boundary is not a compromise, it is where the safety is. The audit found
`limitOp.remaining` doing an unguarded read-modify-write, `distinctOp.seen` a bare
map whose concurrent write is a **runtime throw** rather than a race, `breaker` and
`joinBreaker` able to double-drain, and `batchOperator.i` an unguarded index. All
five stay serial and are untouched.

## 2. The design docs' claim, audited

Since step 1 the docs have said parallelism is nearly free: *"Filter and Project are
already BatchOps… only internal/exec's loop changes. Zero kernels, zero operators,
zero evaluator touched."*

Audited against the code as it stood, that is **about 40% true** — and the true 40%
is exactly what step 5 needed.

**Held up:** every `BatchOp` is genuinely pure (checked body by body — no `Apply`
assigns to a receiver field); `stage` and `scanOp` were already safe for concurrent
`Next`; all four sources carry the mutexes the contract promised; `data.Column`,
`data.Batch`, `dtype.Schema`, `plan.Node` and `expr.Node` are immutable; the
`arrowx` allocator is a global but lock-free; the `dtype` interner is
`RWMutex`-guarded. **Zero kernels and zero evaluator changes were needed**, exactly
as claimed.

**Did not hold up:** "only exec's loop changes" — the parallel operator is new, and
five operators would break if the boundary were drawn anywhere else. And two things
the docs cite as already present, `Batch.meta.Partition` and `Batch.sel`, **do not
exist**; `data.Batch` is `{schema, cols, rows}`.

## 3. Three pre-existing defects the audit surfaced

- **CSV and Parquet `reader.Close()` ran outside the mutex `Next` holds** — a data
  race between a worker and teardown. Latent while the engine was single-threaded,
  reachable the moment a scan has more than one consumer. Both now take the lock,
  with a `closeLocked` for the in-`Next` call site.
- **`hashAggSink.enc` was a dead field** — declared "rebuilt per batch", never read
  or written.
- **`hashAggSink.Merge` folded accumulators positionally with no check.** Two sinks
  fed different data assign group ids by first appearance in their *own* stream, so
  worker A's group 0 may be "alice" and worker B's "bob" — and the merge would add
  bob's total to alice, silently, with no length mismatch to catch it. Parallel
  aggregation is out of scope, so the remap is still not implemented; instead the
  unsupported case now **fails loudly** via `sameNumbering`. When someone builds the
  remap, `TestHashAggSinkMergeRejectsDivergentNumbering` is what tells them to
  delete the guard.

## 4. Predicate pushdown through the join

Deferred from step 4 because its failure mode is silent wrong rows. The legality
table, now implemented:

| `p` reads | Inner | Left | Right | Full | Semi | Anti | Cross |
| --- | :-: | :-: | :-: | :-: | :-: | :-: | :-: |
| left only | → left | → left | **BAR** | **BAR** | → left | → left | → left |
| right only | → right | **BAR** | → right | **BAR** | n/a | n/a | → right |
| both sides | **BAR** | **BAR** | **BAR** | **BAR** | n/a | n/a | **BAR** |
| `Validate` set | **BAR** | **BAR** | **BAR** | **BAR** | **BAR** | **BAR** | n/a |

**Left + right-only is the trap.** An unmatched left row carries all-null right
columns; `p(null)` is null, so the Filter above drops it. Push `p` into the right
input and it deletes right rows — which turns previously-*matched* left rows into
unmatched ones that **reappear**, null-padded, with the filter that would have
dropped them now gone. Extra rows, no error, invisible on any fixture where every
left row matches.

**The `Validate` row is not about rows at all.** A pushed predicate can delete the
duplicate keys a `ValidateManyToOne` check exists to catch, turning a query that
*must* error into one that returns a plausible answer. Result comparison cannot
catch that — one side errors — so it has its own test.

### Classification goes through the layout, not the child schemas

A suffixed right column (`amount_right`) exists in **neither** child, and a
coalesced key is both sides at once. So `rewriteForSide` consults `JoinLayout` and
rebuilds the expression under the child's name — `v_right` descends as `v`. That is
the same output-name → child-name translation `pushdownJoin` does for projections,
except this one has to rewrite rather than just record a name.

Pushing a *copy* of a coalesced-key predicate to both sides is the classic
key-propagation win and is deliberately not done: it has to reason about the
promoted key type, and `k > 2^40` pushed into an Int32 side is a wrong answer rather
than a slow one.

### `Rule.Apply` now takes `Flags`

So an individual ARM can be gated, not just a whole rule. `JoinPredicatePushdown`
is the newest and most dangerous piece of the optimizer and "turn off all predicate
pushdown" is too blunt to bisect with when the two live in one recursive sweep.

## 5. Honest gaps

- **Pipeline breakers stay serial.** Parallel aggregation needs the group-id remap
  above; `Sink` also has no per-worker construction path — `planSort`,
  `planAggregate` and `planJoin` each build exactly one sink. A group-by query
  therefore shows ~2x rather than ~3x (`BenchmarkGroupByThreads`: 44.4 → 22.5 ms),
  which is the pipeline half parallelising and the aggregate half not.
- **No work stealing.** Round-robin dispatch means one slow morsel stalls its lane;
  a work-stealing queue would not. That is the v0.2 morsel scheduler.
- **No spilling, no backpressure beyond the fixed queue depth.**
- `exec.Collect` still peaks at 2× the result, `sortSink` at ~3×.
- **No CSV predicate pushdown**; Parquet pruning is row-group only.
- Temporal types round-trip through neither IO format.
- As-of and inequality joins; window functions; string and temporal namespaces —
  all v0.2.

## 6. Verification

```bash
make test-all   # 4 SIMD widths + experiment-off
make race       # now has goroutines behind it
make levels
make bench      # now measures the engine, not just the arrow-go bet
```

| Test | What it would otherwise miss |
| --- | --- |
| `TestParallelIsOrderPreserving` | the five order-dependent behaviours, at 1/2/3/4/8/16 threads — head, distinct, group-by, First/Last, stable sort, join order |
| `TestParallelEarlyBreakDoesNotLeak` | workers parked forever after `CollectBatches` breaks early; the assertion is a goroutine count |
| `TestParallelConcurrentCollects` | what finally gives `-race` something to detect |
| `TestParallelRespectsCancellation` | a cancelled query hanging on a full queue |
| `TestJoinPredicatePushdownSoundness` | every legality cell, optimized vs unoptimized — the only net, since `Filter.Schema()` never reads `Preds` and `Verify` is blind |
| `TestJoinPredicatePushdownMovesAndRenames` | a rule that is a silent no-op, and a suffixed name pushed down verbatim |
| `TestJoinPredicatePushdownPreservesValidate` | a required error turning into a plausible answer |
| `TestSortSinkMergeEquivalence` | a merge that loses tie stability |
| `TestHashAggSinkMergeRejectsDivergentNumbering` | a documented gap silently corrupting instead of failing |
| `BenchmarkPipelineThreads` | "it's parallel now" being an assertion rather than a number |

Teeth verified by reintroduction: allowing right-only predicates through a Left
join, allowing left-only through a Right join, dropping the `Validate` barrier, and
reversing the sort merge — each fails the corresponding test.
