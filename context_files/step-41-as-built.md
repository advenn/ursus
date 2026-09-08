# Step 41 — as built

**The published numbers are true again**, and the refresh found two things nobody had
measured: the parallel join costs ~40% more memory than the serial one, and my own
confident diagnosis of a regression was wrong twice before the data settled it.

Authoritative where it disagrees with [`step-40-as-built.md`](./step-40-as-built.md),
the vision docs and [`design/`](./design/).

No `internal/` change. **525 answers validate against the duckdb reference** — 155 at
PDS-H SF=1, 155 at SF=0.1, 100 on h2o parquet, 115 on h2o CSV. Nothing struck through.

---

## 1. Why this had to happen

`REPORT.md` was measured at `cd1d78a` on 2026-09-05 and had been overtaken on both
axes: steps 36–37 took the join from serial to parallel, step 39 halved q21's memory.
The README points at that file and the Show HN draft quotes it, so it was publishing
figures I had specific reason to believe were wrong — worse than not knowing.

A **full multi-engine** re-run, not ursus-only: the report takes the latest row per
(engine, suite, query, size, io), so refreshing one engine would have put today's
ursus beside everyone else's from three days earlier.

---

## 2. What moved

**The joins, which is what steps 36–37 were for.** h2o parquet:

| | old | new | |
| --- | --: | --: | --: |
| j5 | 19,699 ms | **10,372** | −47% |
| j2 | 5,606 | **3,404** | −39% |
| j3 | 5,668 | **3,586** | −37% |
| j1 | 4,486 | **3,238** | −28% |
| j4 | 4,595 | **3,327** | −28% |

h2o geomean against polars: **4.58x → 4.00x**. PDS-H at SF=0.1: **4.17x → 2.09x**.

**The memory, which is what step 39 was for.** PDS-H SF=1 peak RSS:

```
ursus   3.98 GB  ->  2.26 GB          polars  0.77 -> 0.81      duckdb  0.31 -> 0.31
```

The ratio against polars goes from about 5.2x to **2.8x**, which is what step 39
predicted (~2.7x) from a single-query measurement.

**PDS-H SF=1 wall clock barely moved in RATIO** — 10.73x polars before, 10.84x now —
because polars got faster this session too (98 → 86 ms geomean) while ursus went
1,057 → 936. The absolute improvement is real; the relative position is not, and
saying only the first would be spin.

---

## 3. The regression that was mostly contention, and two wrong diagnoses

Three queries looked much worse: q6 +74%, q2 +68%, q1 +29%. The plan said a slower
result is the finding, so it was chased.

**First wrong answer: alignment.** Step 37 made `Column.Slice` share a sub-buffer,
which can be less than 64-byte aligned, and `arrowx`'s doc warns kernels treat
alignment as an optimisation hint. Refuted immediately: `IsAligned` is not called
anywhere in `internal/`, so no kernel has an aligned fast path to lose. And q6 has no
join, no group-by and no limit, so `Slice` is not on its path at all.

**Second wrong answer: my own instrumentation.** Step 38 added a heap sampler calling
`runtime.ReadMemStats` every 5 ms — which stops the world — unconditionally, on every
Go-engine query. That fitted beautifully: only ursus regressed, and short scan-bound
queries would suffer most.

It is wrong, and the test that killed it is worth keeping. **duckdb-go and arrow-go
run through the same `engine.Run` and the same sampler**, and both got *faster* (0.94x
and 0.76x median across queries). arrow-go's only passing query **is q6**. Same
harness, same sampler, opposite direction.

**What it actually was.** The per-iteration spread:

```
q6   old [652, 597, 641]      new [670, 1739, 1112]
q2   old [301, 318, 342]      new [350, 533, 740]
q1   old [981, 1006, 1038]    new [1054, 1298, 1408]
```

Old iterations are tight, new ones vary up to 2.6x, and the FASTEST new iteration is
close to the old median every time. That is interference during the run — preflight
warned of it explicitly: *"9.0 GiB of swap is already in use. Even a run that fits in
RAM will contend with swap-in from whatever got paged out."*

Only ursus shows it because ursus's queries are the longest of the fast engines —
600–1300 ms against polars' 50–250 — so they present a far bigger window to be
interrupted. Short queries finish inside the quiet gaps.

Cross-checked with the minimum of iterations, the estimator that is robust to
contention: geomean **0.863** against the median's 0.888, and q6's +74% becomes +12%,
q2's +68% becomes +16%.

**The report was NOT switched to minimums.** Changing estimator after seeing that the
median looks bad is how a benchmark becomes dishonest, and the cross-check belongs in
the record rather than in the table. What q1/q2/q6 need is a re-run on a quiet
machine, and until then the published median stands.

---

## 4. The thing nobody had measured: the parallel join costs memory

h2o parquet peak RSS, ursus:

| | old | new |
| --- | --: | --: |
| j1 | 2.53 GB | 3.67 |
| j2 | 2.78 | 4.16 |
| j3 | 3.19 | 4.44 |
| j5 | 4.80 | 6.36 |
| gb10 | 8.11 | 8.75 |

**The joins are 28–47% faster and 30–45% heavier.** Step 36 put N probe workers over
one frozen table, and each worker holds its own selection arrays plus whatever sits in
its lane — `parQueueDepth` is 2, over 8 workers, on both the job and result channels.
Step 36's as-built accounted for the selection arrays at "~0.5 MB at 8 threads" and
did not account for the batches in flight, which are the larger term.

That is a real trade introduced five steps ago and invisible until a full re-run. It
is not obviously the wrong trade — but it was never a decision, and now it can be.

---

## 5. A coverage change worth naming

The h2o **CSV** table went from three engines to nine. The previous CSV run only
covered gota, QFrame and ursus; this one ran every enabled engine, so the table now
compares against duckdb, polars and the rest rather than only the two pure-Go
libraries. More information, but a different comparison — the "queries passed" row is
where that is visible, and it is why that row is on the verification list.

chdb also newly attempts h2o and fails: a 600-second timeout on gb5, and gb6 errors
with `quantile_cont` not existing in its dialect. Those are chdb's dialect and speed,
recorded rather than hidden, and they are why `make run` exits non-zero.

---

## 6. Verification

- **525 answers validated** against duckdb across four suite/scale/IO combinations.
  Steps 36–40 changed join threading, in-memory batch boundaries, `Column.Slice`'s
  aliasing and an accumulator's storage, and all had only ever been checked at SF=0.1.
  This is the first check at SF=1, and it passed.
- **The provenance stamp works**: *"Produced from `4e0ce65` … on 2026-09-08."* Step 25
  built that and this is its first real exercise since.
- `timings.csv` carries today's timestamps for every engine, so no session mixing.
- No code changed, so the standard gate is a formality — run anyway and clean.

---

## 7. What is still open

- **q1, q2, q6 need a quiet-machine re-run** before anyone can say whether the
  residual 7–16% is real.
- **The parallel join's memory** (§4) is now a measured trade with no decision behind
  it: `parQueueDepth`, worker count and what a lane holds are all adjustable.
- **The heap sampler** is unconditional and calls a stop-the-world function every 5 ms.
  It did not cause this regression, but it is still overhead on every Go-engine run
  for a number only one step needed. It should be behind the flag that already exists.
- The standing list: `JoinWhere`, inline keys for `KeyTable`, the parallel build side,
  Right/Full parallel probe, `quantile`/`median` storage, nested writing and
  `as_struct`, `.list` set operations, Map/Array, Pivot/Unpivot, SQL, cloud stores,
  join reordering.
