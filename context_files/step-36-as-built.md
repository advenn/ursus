# Step 36 — as built

**The join probe runs on N workers over one frozen table.** And the more useful
result: **step 27's headline measurement was wrong about its own cause.**

Authoritative where it disagrees with [`step-35-as-built.md`](./step-35-as-built.md),
the vision docs and [`design/`](./design/).

**1517 test cases green** — 1467 before this step — under `GOEXPERIMENT=simd` ×
`GODEBUG=simd={512,256,128,0}`, with the experiment off, and under `-race`.
`make levels` and `go vet` clean in all three modules. PDS-H SF=0.1 validates 22/22
for ursus.

---

## 1. The fixture, not the join

Step 27 reported this and scoped a step from it:

```
opbench join, 5M ⋈ 100k, no file IO
  -threads 1   1,097.7 ms
  -threads 8   1,253.4 ms      <- more threads is SLOWER
```

The reading was "the pipeline below the join parallelises and then funnels into one
probe goroutine". That was half right. The other half is that **there was only ever
one probe job to hand out.**

`ursus.Frame(Values(...))` gives its batches to `memsrc`, which returns them exactly
as given and **ignores `WithBatchSize`**. So opbench's 5M-row left side is ONE batch.
A join whose probe side is one batch has one unit of work, and no scheduler can
divide it. The negative number was real; its cause was the fixture.

Corrected — the same 5M rows expressed as 64 concatenated frames, which is also the
shape a Parquet scan actually produces — the direction reverses:

| matched pair | 1 thread | 8 threads |
| --- | --: | --: |
| 1 | 722.7 ms | 678.8 ms |
| 2 | 1,207.3 ms | 783.7 ms |
| 3 | 766.5 ms | 547.5 ms |
| 4 | 740.6 ms | 560.3 ms |

**Roughly 1.3x, and positive in every pair.** The plan set 1.5x as the bar for
"clearly above this machine's noise", and this is under it — so the evidence is the
consistent SIGN across four matched pairs, not any single ratio. Pair 2's outlier is
load; the pairs are run adjacently so drift moves both.

Note the baseline moved too: 1,098 ms became ~740 ms at one thread, from the fixture
change alone. Step 27's numbers are not comparable to these, and quoting the
reversal as "1,253 → 510" would be false.

Why only 1.3x on eight cores is not a mystery — it is step 27's *other* finding,
which still stands: 72% of the probe is `KeyTable.Get` waiting on four dependent
random accesses. Memory-latency-bound work does not scale with cores.

---

## 2. The same defect was in the tests, and the teeth are what found it

`parFrame` — the helper every parallel test is built on — has the property described
above: one frame, one batch. So `TestParallelIsOrderPreserving`'s join case ran
every job on lane 0.

The tooth for it was "make all N workers share ONE `joinProbeOp`", which shares the
cursor, the selection arrays and the encoder. Under `-race`, for 21 seconds, **it
passed.** Not because the sharing was safe — because only one worker ever ran.

Direct instrumentation settled it:

```
one in-memory batch      jobs=1   max concurrent workers=1
20 concatenated batches  jobs=20  max concurrent workers=8
```

With the join cases rebuilt on a `parChunked` helper the same tooth produces five
`WARNING: DATA RACE`. The lesson is the one step 24 recorded about benchmarks and
step 29 about a fixture whose first batch began with a null: **a test that cannot
reach the code proves nothing, and it looks exactly like one that can.**

---

## 3. Two more fixtures that could not see their own bug

The gate keeps Right and Full joins serial, because `matched` is written by the probe
and read by a flush the parallel path does not run at all — dropping the gate loses
every unmatched build row silently. The tooth for it passed twice before it bit:

1. The first Right fixture joined a left side containing every tag against a right
   side that was a subset, so **no build row was ever unmatched**. There was nothing
   for the missing flush to lose.
2. Fixed, and it still passed: `Head(3_000)` over a 171,000-row join left the
   unmatched rows outside the compared window.

Now: small frames, no `Head`, and the left side deliberately missing a tag the right
side has. Both Right and Full fail without the gate.

---

## 4. The design

**One frozen table, N workers.** `joinTable`'s doc has said since step 4 that
"nothing mutates it after freeze, which is what would let a future morsel scheduler
share one table across N probe workers with no lock", and step 23 kept it true by
giving `KeyTable.Get` no writes. This is that use. Each worker owns its encoder,
`lsel`/`rsel` and `row`/`hit`/`entered` cursor and shares only the table.

**A terminator, because the probe is one-in-many-out.** `parallelOp` can keep its
lanes aligned by counting, since every job yields exactly one result. A probe job
yields zero, one or many. So a worker follows job *i*'s outputs with a terminator and
the consumer drains a lane until it before advancing — order still falls out of the
topology, because lane *i* holds job *i*'s outputs in order. `parallel.go` is
untouched: its `parResult` documents the exactly-one invariant and that stays true.

**No probe logic is written twice.** `probeStep` split into `fillCurrent` (pull a
batch) and `stepCurrent` (drain the one I have). The serial path calls both; a worker
is handed its batch and calls only the second. The `hit`/`nHit` resume that makes
output batch-size invariant exists once.

**The gate**, and each condition is a real dependency:

| stays serial | because |
| --- | --- |
| Right, Full | `matched` spans the probe and a flush the parallel path never runs |
| one-to-one / one-to-many validation | `seen` detects a duplicate left key across the WHOLE stream |
| a spilled build side | `routeProbe` writes per-bucket files; replay is one bucket at a time anyway |

`joinBuildSink` gained one field, `threads`, set only in `planJoin`. `newSub` builds
its sub-sink field by field and does not carry it, so **spill replay stays serial
without anyone having to remember to zero it.**

---

## 5. Verification

`TestParallelIsOrderPreserving` — 1, 2, 3, 4, 8 and 16 threads, compared with
`AssertFrameEqual` — gained a multi-batch probe side and six join cases: fan-out
(many output batches per input batch, which is what the terminator is for), Left,
Semi, Anti, Right and Full on the serial path, and both degenerate shapes.

`internal/physical/parjoin_test.go` asks the ordering question of the operator
directly, over a fixture small enough to name every row. It exists because the
end-to-end test answers slowly and, when the lane protocol is broken, **hangs rather
than failing** — the tooth ran past 600 seconds. The focused test fails in 0.00s with
`row 100 is lv=64, want 25`.

All four teeth bite:

| tooth | what fails |
| --- | --- |
| advance the turn on the first result | `TestParProbeKeepsInputOrder`, instantly |
| all workers share one `joinProbeOp` | five data races — **only after the fixture fix** |
| drop the `spilled()` gate | five spilling-join tests |
| drop the Right/Full gate | both, once the fixture had unmatched build rows |

---

## 6. What is still open

- **The build side.** `joinBuildSink.Merge` is implemented, tested and *still* never
  called — it needs a per-worker sink construction path that `Sink` does not have and
  a key-id remap of the kind `hashAggSink` needed in step 17.
- **Right and Full**, which per-worker `matched` arrays ORed before the flush would
  cover, and **validated joins**, which need a different mechanism for `seen`.
- **`KeyTable.Get`'s memory latency** — the ceiling on everything above. The obvious
  layout fix is refuted (1.85 MB → 4.20 MB, out of L2, 57% slower); what is left is
  reducing the arena access, not the indirection.
- **`memsrc` ignoring `WithBatchSize`.** This step worked around it in two places.
  Making an in-memory frame respect the batch size would make every parallel path
  behave the same on in-memory data as on a scan — and would have made this step's
  first four measurements meaningful.
- **Join reordering** — still no cost model and no statistics to build one from.

Unchanged: nested writing and `as_struct`, `.list` set operations, Map/Array,
Pivot/Unpivot, SQL, cloud stores, plan serialization.
