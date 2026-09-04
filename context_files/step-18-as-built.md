# Step 18 — as built

The sort, and the per-row string allocation. **The first step whose scope was
chosen by a profiler rather than by a design doc.**

Authoritative where it disagrees with [`step-17-as-built.md`](./step-17-as-built.md),
the vision docs and [`design/`](./design/).

**1378 test cases green** — 479 top-level and 899 subtests (1321 before this step)
— under `GOEXPERIMENT=simd` × `GODEBUG=simd={512,256,128,0}`, with the experiment
off, and under `-race`. `make levels` and `go vet` clean.

196 files, ~59.2k lines (excluding `bench/`). No API change at all.

```
                       before      after   speedup      allocs/op        x
WindowRank            1623 ms     320 ms     5.08x   1,064,249 -> 16,019   66x
SortUnbounded         1308 ms     403 ms     3.24x
SortSpilling          1273 ms     462 ms     2.75x
JoinAsOf              1236 ms     617 ms     2.00x
ScanParquet            358 ms     213 ms     1.68x   2,137,635 -> 41,760   51x
GroupByLowCardinality  152 ms      92 ms     1.65x   1,066,138 -> 17,909   60x
```

---

## 1. Two bottlenecks, both named by a profile

Neither was a guess. `bench/micro` plus `go tool pprof`, over `WindowRank`,
`SortUnbounded`, `JoinInner`, `GroupByStringKey` and `StringContains` together:

```
kernel.ArgSort            6.46s  42.2%   of which 99.85% is sort.SliceStable
  windowSink.segments     3.85s  59.6%   of ArgSort
  sortSink.order          2.61s  40.4%
sort.symMerge_func        5.82s  38.0%   cumulative
reflectlite.Swapper.func6 0.82s   5.4%   flat — SliceStable swaps by reflection
kernel.OrderKeyF64        1.78s  11.6%   FLAT
```

**One function was 42% of the engine's hot-path CPU.** And the last line was the
giveaway: `OrderKeyF64` turns a float into a uint64 whose unsigned ordering IS
ursus's documented total order, and it was being recomputed *on every comparison*
— O(n log n) calls to answer an O(n) question. The key it produces is exactly what
a radix sort wants.

The second was an allocation profile: `byteArrayCol.read` was **35% of every
object the engine allocated**, because it appended one Go string per value and
then let `data.NewString` copy every byte a second time.

| | ns/op | allocs/op |
| --- | --: | --: |
| `ScanParquet` (2 string columns, 1<<20 rows) | 358 ms | 2,137,635 |
| `ScanParquetProjected` (no string columns) | 56 ms | 11,485 |

2,137,635 / 1,048,576 = **2.04 allocations per row**, one per string value.

---

## 2. The radix sort

`ArgSort` keeps its signature and its contract. Three things carry the
correctness, and each has a teeth check behind it:

- **Stability is met by construction.** `ArgSort`'s doc calls it load-bearing
  three times over; an LSD radix distributes into buckets in input order, so ties
  keep their original order because of how the algorithm works rather than as an
  obligation on it.
- **Descending is `^key`.** Inverting every bit reverses the order exactly and
  leaves equal keys equal, so there is no second code path and no cost to
  stability.
- **Nulls are a partition, not a comparison.** Each pass splits null from non-null
  stably, radixes the rest, and concatenates per `NullsLast`. That reproduces
  `nullRule`'s actual rule — placement applied *before* direction, so `NullsLast`
  means the same thing ascending and descending — without needing a 65th bit in
  the key.

**Multi-column is LSD over columns, last key first**, which is what made this
worth doing at all: `windowSink.segments` ALWAYS sorts on two or more columns
(the partition id plus the rank or order key) and was 60% of ArgSort's time. A
single-column fast path would have missed the larger half.

Byte passes whose histogram is uniform are skipped, so an Int32 partition id costs
about two rounds rather than eight.

The fallback — String, Int128, Decimal, and any mixed set containing one — keeps
the comparator, but `ArgSort` itself moved from `sort.SliceStable` to
`slices.SortStableFunc`, so the reflection swapper is gone from every path.

### What it does not cover, and the proof

TPC-H q1 sorts on two String columns and takes the fallback. Predicted in the
plan, measured after: 243 ms at one thread against 174 ms at eight, which is the
parallel scan rather than the sort. A byte-wise radix over `GroupKeyEncoder`'s
output would cover strings — its doc already notes the encoding is
order-preserving — and §5 records why that is its own step.

---

## 3. Parquet strings straight into Arrow's layout

`byteArrayCol` accumulates `offs []int32` and `chars []byte` as it decodes, and
`finish` hands them to a new `data.NewStringParts`. The per-row allocation becomes
a handful of slice doublings, and the second full copy of every byte disappears.

The copy that remains is not optional and never was: the `parquet.ByteArray`
points into a decode buffer arrow-go reuses across batches. `append` performs it,
which is why this could not be made *more* wrong by removing a guard — see §5.

Blast radius beyond the scan itself, all from the same change:
`GroupByLowCardinality` 60× fewer allocations, `WindowMean` 66×, `CollectAll` 43×,
`StringContains` 44×, `JoinInner` 17×.

---

## 4. Verification

`make test-all` (four SIMD widths plus experiment-off), `make race`,
`make levels`, `go vet` — all clean.

The central safety net already existed, which is what made this tractable:
`TestArgTopKMatchesArgSort` requires `ArgTopK(n,k) == ArgSort(n)[:k]` EXACTLY,
including ties. Any stability break fails it immediately.

The new one is a differential: `TestRadixMatchesComparatorSort` compares
`ArgSortColumns` against `NewComparator` + `ArgSort` **permutation for
permutation** — not sorted values, because comparing values cannot see tie order —
across every radix-able dtype × ascending/descending × NullsFirst/NullsLast × with
and without nulls × one to three key columns. Values are drawn from small ranges
on purpose: a fixture of distinct values passes with a completely unstable sort.

Six teeth checks fired:

| Reintroduced | Caught by | What it looked like |
| --- | --- | --- |
| Distribute in reverse order (unstable) | `TestRadixMatchesComparatorSort` | ties inverted on the first float column |
| Apply direction to null PLACEMENT too | same | `NullsLast` inverted on every nullable column |
| Process columns first-to-last (MSD) | same | `two_keys` diverged at row 0 |
| Skip a byte pass that is not uniform | same | `int32` diverged at row 0 |
| Drop the leading zero from the offsets | `TestParquetRoundTrip` | `column "s" has 6 rows, but column "b" has 7` |
| Mark string nulls valid | `TestParquetRoundTrip` | nulls became `""` |

**Two did not fire, and are recorded rather than dressed up.**

Aliasing arrow-go's reused decode buffer — the hazard the original comment warns
about — has no guard to remove, because `append` copies by definition. The design
makes that bug unrepresentable rather than defended against.

And consuming a value for a *null* row instead of an empty slice changes nothing
observable: a null's bytes are read by nothing that respects validity, so it wastes
space and is not a wrong answer. `appendNull` is an optimisation, not a
correctness guard.

---

## 5. Honest notes on the measurement

- **The baseline had to be recorded twice.** The first `make micro-baseline` ran
  concurrently with the first edit to `ArgSort`, and there was no way to prove
  whether it compiled before or after. Rather than assume, `sort.go` was reverted
  to its pristine form, `radix.go` parked, the baseline re-recorded, and the
  changes restored. Every number in this document is against that verified
  baseline, five samples per side, same machine, same session.
- **The end-to-end numbers are noisier than the micro ones** and are reported as
  approximate. h2o `gb8` — `Rank().Over()`, the most sort-bound query in the suite
  — goes from a recorded 27,973 ms to **8,036 ms**, but the load average during
  measurement was 7.11 on an eight-thread machine, entirely from this session's own
  benchmarks. Cross-session end-to-end comparisons are not trustworthy right now;
  the micro suite is.
- **`bench/results/REPORT.md` still predates steps 17 and 18** for ursus. The
  direct runner invocations here deliberately wrote nothing to `results/`, so the
  report reflects the last full harness run. Refreshing it needs a quiet machine
  and a full `make bench`.

### The memory trade, measured

The radix path allocates a `[]uint64` key and one scratch permutation:
**+13 MB per million rows** for `SortUnbounded`. The null-partition buffers are
allocated only when a column actually has nulls, which halved that from the +25 MB
a first version paid.

Those buffers are *outside* the `execopt` ledger, and deliberately: the budget
accounts memory that is RETAINED across batches, and `budget.go` already states it
"is silent about a single kernel's transient output". `BenchmarkSortSpilling`
confirms nothing moved — **4.062 peakMiB and 4 spills, identical to baseline** —
while getting 2.75× faster.

---

## 6. Honest gaps

- **The CSV reader has the same defect Parquet just lost.** `ScanCSV` still
  allocates 3,177,442 times per iteration — unchanged — because it builds Go
  strings per value exactly as `byteArrayCol` used to. The same
  `data.NewStringParts` fixes it.
- **String kernels allocate per value too.** `StringSliceUpper` still shows 2.1M
  allocations; a kernel that produces strings builds them one at a time.
- **String sort keys stay on the comparator path**, so TPC-H q1's sort is
  unimproved (§2).
- **The six `map[string]int32` hash tables** cost ~11% of CPU
  (`mapaccess2_faststr` 7.5%, `matchH2` 2.9%, `memHashAES` 1.1%). Six call sites
  with three different spill invariants is why it was not this step.
- **h2o gb7's 96 spurious NULLs** (step 17 §8) — still the only wrong answer in
  the suite and still ahead of any of this on merit. `kernel.assembleRows`
  computing a validity bitmap it never uses is the lead.
- Parallel join build, the serial join probe, CSE and `JoinWhere` are unchanged
  from step 17.

---

## 7. Files

**New:** `internal/kernel/radix.go` (327), `internal/kernel/radix_test.go` (219).

| File | Change |
| --- | --- |
| `internal/kernel/sort.go` | `ArgSort` uses `slices.SortStableFunc`; new `ArgSortColumns` dispatches radix-or-fallback |
| `internal/physical/sort.go` | `order` calls `ArgSortColumns`; the comparator is now built only for the top-k path |
| `internal/physical/window.go` | `segments` calls `ArgSortColumns` |
| `internal/source/parquet/column.go` | `byteArrayCol` accumulates offsets and chars (§3) |
| `internal/data/column.go` | `NewStringParts`, the constructor a decoder can build into directly |
| `bench/results/micro-baseline.txt`, `micro.txt` | before and after, five samples each |
