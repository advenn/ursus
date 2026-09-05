# Step 24 — as built

The radix sort's keys now travel with their rows instead of being read through a
permutation. **The sort is 2.4x to 4.1x faster, and the gain grows with n** —
which is the point, because the queries that hurt are the large ones.

Authoritative where it disagrees with [`step-23-as-built.md`](./step-23-as-built.md),
the vision docs and [`design/`](./design/).

**1415 test cases green** — 499 top-level and 916 subtests (1411 before this step)
— under `GOEXPERIMENT=simd` × `GODEBUG=simd={512,256,128,0}`, with the experiment
off, and under `-race`. `make levels` and `go vet` clean in both modules. 201
files, ~60.9k lines excluding `bench/`.

```
BenchmarkArgSortColumns          before      after

  1 key   n = 1,048,576        114.0 ms    47.0 ms    2.43x
  2 keys  n = 1,048,576        185.6 ms    66.3 ms    2.80x
  1 key   n = 8,388,608       1078.1 ms   359.4 ms    3.00x
  2 keys  n = 8,388,608       2571.3 ms   620.5 ms    4.14x

transient memory per sort      16n bytes   24n bytes
```

---

## 1. This step exists because step 23's recommendation was wrong

Step 23 closed by naming the window sink's single-threadedness as "the largest
addressable structural gap": `windowSink.Merge` is refused, so h2o gb8 runs on one
core against polars' eight, 27,973ms against 1,371ms. That was reasoning from the
code rather than from a measurement, and a profile of `BenchmarkWindowRank`
overturned it:

| inside `windowSink` | cum |
| --- | --: |
| `segments` → `argSortRadix` → **`radixByKey`** | **0.52s** |
| `OrderKeyF64`, the key extraction | 0.16s |
| `KeyTable.GetOrInsert` — ALL of `Consume`'s group work | 0.05s |

`segments` is 0.57s of the 0.62s the sink costs. **The window is the sort.**
Parallelising `Consume` would have divided the 0.05s. Two steps running, two
recommendations overturned by profiling the thing before working on it.

---

## 2. The defect

`radixByKey`'s distribution loop:

```go
for _, row := range src {
    d := byte(keys[row] >> (8 * b))   // random read into an n*8-byte array
    dst[off[d]] = row
    off[d]++
}
```

`keys` is indexed by ROW; `src` is a permutation. On the first round the
permutation is the identity and the read is sequential — from the second round on
it is scattered. A two-key window sort runs ten or eleven rounds and all but the
first read that way. The histogram pass had the same shape.

The fix is to gather the keys into `src` order once, then let the key travel with
its row:

```go
ka, kb := r.keyBuf(n), keys[:n]
for i, row := range src {
    ka[i] = keys[row]         // one random gather, replacing ~9
}
...
for i, row := range src {
    d := byte(ka[i] >> (8 * b))   // sequential
    dst[off[d]] = row
    kb[off[d]] = ka[i]            // the key goes where the row goes
    off[d]++
}
src, dst = dst, src
ka, kb = kb, ka
```

A fully random read becomes a sequential read plus a second bucketed write, and a
bucketed write touches 256 active cache lines where the read touched n.

**The second key buffer is `keys` itself.** Nothing reads it in row order after
the gather and `argSortRadix` allocates a fresh one per column, so it is free to
be overwritten. That keeps the cost to one extra buffer, and it is the one thing
in the file that would surprise a reader, so the doc says it outright.

---

## 3. The measurement needed a new instrument

The plan's two proxies — `SortUnbounded` and `WindowRank`, both in `bench/micro` —
**could not resolve this change at all**, and it is worth being precise about why
rather than calling it noise.

`BenchmarkWindowMean` was carried as a control: an aggregate window broadcasts
through `kernel.Take` and never sorts, so nothing this step did can touch it. It
moved **32%** between the before and after runs (134.0ms → 176.7ms). An
interleaved run of two separately compiled binaries did no better — the control
still differed by 29% between alternating invocations of code that is identical in
that path. The laptop throttles under sustained load and the drift is larger than
the end-to-end effect being looked for.

The deeper problem was scale. This machine's L3 is 8 MiB and `bench/micro`'s
fixture is 1,048,576 rows, so its key array is 1M × 8 = **8 MB, exactly L3**. The
benchmarks were sitting on the boundary between the two regimes the change is
about, while also spending most of their time in the Parquet scan.

So `internal/kernel/radix_bench_test.go` is new: `BenchmarkArgSortColumns`, no IO,
no fixture file, one seeded PRNG, and two sizes chosen to straddle the cache —
1<<20 keys are 8 MB and 1<<23 are 64 MB. It measures the sort and nothing else.
Under it the answer is unambiguous and scales exactly as the mechanism predicts:
2.43x at a million rows, 4.14x at eight million on the two-key window shape.

The honest limit: **the end-to-end effect on a real query was not measured.** The
sort is 2.4–4.1x faster in isolation; what that is worth to gb8 depends on the
fraction of gb8 that is the sort, and this laptop cannot resolve it.

---

## 4. What it costs

Transient memory per sort goes from 16n bytes to **24n** — `keys` + `idx` +
`scratch` gains one gathered-key buffer. Measured rather than argued:
16,777,216 B/op → 25,165,824 B/op at n = 1,048,576, which is exactly 16n → 24n.

The plan said "16n to 20n". That was an arithmetic slip — adding an n×8 buffer to
16n gives 24n — and the benchmark caught it.

None of it is on the memory ledger. `sortSink` accounts its retained batches and
key batches, but the radix scratch inside `ArgSortColumns` is invisible to
`execopt`; `radixSorter`'s own doc quantifies its buffers, so the size was known
and the gap is pre-existing. This step makes it 50% larger without introducing it.
Recorded, not fixed — wiring transient kernel scratch into the budget changes when
a limited query spills and wants its own step.

---

## 5. Teeth, and a test that earned its place

**Swap the key buffers on a SKIPPED round.** The uniform-byte skip is what makes a
narrow key cheap — an Int32 partition id spends two rounds rather than eight — and
a skipped round must not swap the keys either, or they fall one permutation behind
`src`. Reintroduced, `TestRadixSkippedRoundsKeepKeysAligned` fails **and
`TestRadixMatchesComparatorSort` does not.** The pre-existing differential, over
every radix-able dtype and both directions, cannot see it: it takes a key with a
uniform byte in the MIDDLE, so that live rounds sit on both sides of the skip.
That is the new test justifying itself.

**Gather by row instead of by position** — `ka[row] = keys[row]`. Here the old
differential does fail, immediately, because every multi-column sort hands
`radixByKey` a non-identity permutation.

New cases, all against `assertAgrees`, which compares PERMUTATIONS against
`NewComparator` + `ArgSort` so that tie order stays observable:

| case | why |
| --- | --- |
| uniform middle byte, both directions | the skipped-round hazard above; descending inverts every bit, so it skips a different byte |
| a constant key — every round skipped | the gather runs and nothing else does; the permutation must come back identical |
| n = 1, 2, 3 | the early return, and the smallest input that reaches the gather |
| a 14%-present column with wide values | `sortPass` hands `radixByKey` the `nonNull` SUBSET, so the gather must index by position within that subset, not by row |

---

## 6. An environment note

`make test-all` failed mid-step with eight packages reporting `[build failed]`.
The cause was not the code: `/tmp` is a **7.7 GiB tmpfs and it was at 100%**, and
Go links through `$TMPDIR`, so `link: mapping output file failed: no space left on
device`. The gate was run with `TMPDIR` pointed at the root filesystem, which has
96 GiB free.

This session's scratch accounted for about 1 GiB of it (including two 35 MB test
binaries built for the interleaving attempt, since removed); the other ~6.6 GiB
belongs to something else and was left alone. Worth knowing because it will bite
any build, not just this one.

---

## 7. What is still open

- **`windowSink` is still single-threaded**, and still worth roughly 8% of the
  window rather than the 20x gb8 needs (§1). The honest question for gb8 is
  whether `Filter(rank_over(...) <= k)` should be rewritten into a bounded top-k
  per partition, which skips the global sort entirely — a plan rule plus an
  operator, and a real feature rather than a tuning change.
- **The end-to-end value of this step is unmeasured** (§3).
- **The join family** (j1–j5, 6–15x). `BenchmarkJoinInner` is scan-bound at a
  million rows — the whole join subtree is 13% of its samples — so there is no
  cheap proxy on this laptop.
- **Transient kernel scratch is unaccounted** (§4).
- **`argExtremumAcc` and `positionAcc` still hold a `*data.Column` per group** —
  the shape step 20 removed from min/max for 16x. Confirmed still present, but no
  benchmark or suite query uses ArgMin/ArgMax/First/Last, so a fix would be
  unmeasurable until a fixture exists.
- **The arrow-go Parquet floor** — PDS-H q6: arrow-go 56ms against polars 7ms.
  Not fixable from here.
- **`hashAggSink` does not account its group table**, **`nuniqueAcc`'s per-group
  sets**, **string kernels**, **CSE**, **`JoinWhere`**, **nested types**.
