# Step 20 — as built

`extremumAcc` rewritten to flat typed storage. **h2o gb7 goes from 23× slower
than polars to 1.6×.**

Authoritative where it disagrees with [`step-19-as-built.md`](./step-19-as-built.md),
the vision docs and [`design/`](./design/).

**1403 test cases green** — 487 top-level and 916 subtests (1386 before this step)
— under `GOEXPERIMENT=simd` × `GODEBUG=simd={512,256,128,0}`, with the experiment
off, and under `-race`. `make levels` and `go vet` clean. 198 files, ~60.0k lines
excluding `bench/`.

```
h2o gb7   GroupBy(id3).Agg(Max(v1), Min(v2))   over 10,000,000 rows / 100,000 groups

              before      after
  1 thread   77,751 ms   4,847 ms    16.0x
  8 threads  28,461 ms   1,952 ms    14.6x        polars: 1,222 ms

through the benchmark harness, which also reports memory:
              38,146 ms / 1.91 GB  ->  2,360 ms / 0.23 GB
```

The answer is unchanged and still exact: `sum_range_v1_v2 = 399,879`, zero nulls,
matching the duckdb reference at both thread counts. **h2o now validates 15/15**
and PDS-H 22/22, so `REPORT.md` has nothing struck through for the first time.

The 8× memory drop is the same cause as the speed: a `*data.Column` per group,
gone.

---

## 1. What was wrong with it

`extremumAcc` kept a representative single-row **`*data.Column` per group**. Per
batch it also allocated a `map[int32]int`, and then per group in that batch it
called `Take` to materialise a one-row column and `concatColumn` to compare two of
them. For 100,000 groups across 1,220 batches that is millions of heap Columns.

The generality was real — one implementation covered every type, including
strings — and the cost was 35% of every object the engine allocated. `sumAcc`,
thirty lines above in the same file, has always used flat typed slices.

## 2. What replaced it

Four accumulators, one per payload shape:

| | for | comparison |
| --- | --- | --- |
| `extremumNum[T data.Primitive]` | every fixed-width numeric, and the temporal types via `Physical()` | `<` for integers, **`OrderKeyF64`/`F32` for floats** |
| `extremumStr` | String and Binary | `<` on the Go string |
| `extremumI128` | Int128, and so Decimal | `i128.Cmp` |
| `extremumBool` | Bool — and therefore also `Any` and `AllTrue` | false < true |

`AddBatch` reads the column once per batch through `data.Values[T]` and updates a
flat loop. No map, no `Take`, no `concatColumn`, no allocation per group.
`Finish` is the two lines `sumAcc.Finish` uses: `data.NewFixed(name, a.out,
a.best[:n], seenBitmap(a.seen, n))`.

### The identity-value trap, avoided a second way

The step-1 docs record it: a SIMD min/max must use `IfElse(valid, identity)` with
+Inf and −Inf, because the obvious `Masked` zero-fills and makes
`Min([3,null,5])` return 0. The old code could not step in it because a row index
has no identity value. These hold values, so the guard is `seen` — a per-group
bool that stays false until a **non-null** value arrives. An all-null group never
sets it and `seenBitmap` turns it into a null.

### Comparison is the total order, not `<`

Floats compare through `OrderKeyF64`/`OrderKeyF32`, so NaN is one canonical
bucket above everything and −0.0 equals +0.0 — the order `order.go` documents and
`Sort` uses. `TestExtremumAgreesWithSort` pins the consequence directly:
`min(x)` must equal `sort(x)[0]` and `max(x)` must equal `sort(x)[last]`, on data
containing NaN, both infinities and both zeros.

### The output dtype is `bind.Out`, not the physical type

That is what keeps the max of a Datetime a Datetime, of a Decimal a Decimal and
of an Enum an Enum, over int64/Int128/uint32 storage. Reintroduced, it returns
`Int64` where `Datetime(us, UTC)` belongs — a plausible-looking tick count rather
than an error.

---

## 3. A hazard that the old design did not have

`data.StringAccessor.Get` returns a string **aliasing the batch's character
buffer** — its own doc says so. Storing that alias as the running best would keep
every batch that ever won a comparison alive for the whole aggregation, which is
a worse memory profile than the per-group Columns being removed.

So `extremumStr` compares against the alias, which is free, and clones only on
the store — at most once per group per batch, usually far less.

---

## 4. Verification

`make test-all` (four SIMD widths plus experiment-off), `make race`,
`make levels`, `go vet` — all clean. The existing suite carried most of the
weight: `TestMinMaxSkipNulls`, `TestMinOverAnAllNullGroup`,
`TestMinOverAnAllNullStringGroup`, `TestMergeEquivalence` and
`TestMergeEquivalenceOverStrings` all predate this step and all had to keep
passing.

New: `internal/kernel/extremum_test.go` — the total order, agreement with `Sort`,
the output dtype across Datetime/Date/Duration/Decimal, every payload shape
including int8 negatives and uint64, and the aliasing guard.

Five teeth checks fired:

| Reintroduced | Caught by | What it looked like |
| --- | --- | --- |
| Compare floats with `<` | `TestExtremumUsesTheTotalOrder` | `max = 1, want NaN`; and `max = +Inf but sort(x)[last] = NaN` |
| Mark every group valid instead of `seenBitmap` | `TestMinOverAnAllNullGroup` | an all-null group stopped being null |
| Publish `out.Physical()` | `TestExtremumPreservesTheOutputDType` | `max of Datetime(us, UTC) produced Int64` |
| Merge without step 17's remap | `TestHashAggSinkMergeRemapsGroupIds` | `1,100;2,200;3,300` where `1,300;2,200;3,100` belongs |
| Drop `strings.Clone` | `TestExtremumDoesNotPinTheBatchBuffer` | `min = "XXX"` — the accumulator read a scribbled-over buffer |

### One of those tests was vacuous when first written

`TestExtremumDoesNotPinTheBatchBuffer` originally built a column with
`data.NewStringParts` from a `[]byte` it then overwrote. It **passed with
`strings.Clone` deleted**, because `NewStringParts` copies its input — the
scribble never reached the column. Rewritten to mutate the column's own buffer
through `RawChars`, it fails immediately. The lesson is the same one step 17
recorded about a single-batch fixture: a test of a copy has to mutate the thing
that was allegedly copied *from*.

---

## 5. Two smaller fixes

**CI was red by hardware lottery.** `ubuntu-latest` runners are a mix of AVX-512
and AVX2, and the `test (512)` leg panics at `simd.init` on the latter:

```
panic: Requested GODEBUG=gosimd=512 is larger than the simd length (256)
       supported on this cpu
```

Two consecutive pushes went red then green on effectively identical code. A guard
step now reads the runner's capability and **skips** the leg it cannot run, with a
visible `::notice::`. Skipping rather than clamping is deliberate: clamping to 256
would run that width twice and report it as 512 coverage, which is a worse lie
than an honest skip. All four widths still run locally through `make test-all`.

**The first version of that guard was itself broken.** It used
`grep -qw avx2 /proc/cpuinfo && have=256`; GitHub runs `run:` blocks under
`bash -e`, so a failing grep exits non-zero and would have failed the step on
exactly the AVX2-only runners the guard exists to protect. It is now `if grep …;
then …; fi`, simulated under `bash -e` for both hardware cases before shipping.

**`bench/results/REPORT.md` is now published.** It was gitignored along with the
rest of `results/`, so the README's reference to it pointed at a file that does
not exist on GitHub. The single report file is now tracked; the datasets, raw
timings, plots and dashboard stay ignored.

**And `make run` was measuring a stale binary.** `bench/bin/runner` is built by
`setup-go`, which only `make bench` depended on — so `make run` timed whatever
binary happened to be in `bin/`. It reported gb7 at **38,146 ms and FAILING
VALIDATION** from a build that predated steps 19 and 20, while the working tree
had it at 2,360 ms and correct. Both numbers looked completely real.

`run` now depends on `setup-go`. A benchmark that measures the wrong build is
worse than no benchmark, and this one came within one command of being published.

### The report is only partly refreshed, deliberately

PDS-H (all 22 queries) and h2o's `gb7` were re-measured with the current build.
The other fourteen h2o rows were not: that suite at N=1e7 saturates the machine
for tens of minutes, and the author asked for it not to be run routinely — a
reasonable request, since it also makes the timings it produces noisy. So the h2o
geomean in the report mixes a fresh `gb7` with older rows and **understates the
engine**. The README says so rather than letting the number speak unqualified.

---

## 6. Honest gaps

- **`argExtremumAcc` still stores a `*data.Column` per group** — the same shape
  just removed, for ArgMin/ArgMax. They are order-dependent, far rarer, and step
  17 already forces them onto the serial path, so it was left alone rather than
  changed without a measurement to justify it.
- **`positionAcc` (First/Last) still uses `assembleRows`**, which step 19
  established is not broken but is the same per-group-Column shape.
- **The CSV reader's per-value string allocation** — 3,177,442 allocations per
  `ScanCSV`, the exact defect Parquet lost in step 18 and fixable with the same
  `data.NewStringParts`. Now the clearest remaining allocation win.
- **The six `map[string]int32` hash tables**, ~11% of CPU; **the serial join
  probe** (j1–j5 at 6–15×); **string sort keys** on the comparator fallback;
  **CSE**; **`JoinWhere`**; **nested types**; and **`reverseSink.Merge`'s missing
  test** are all unchanged.
- **No LICENSE.** The repo is public without one, so nobody may legally use the
  code. Only the author can choose.

---

## 7. Files

| File | Change |
| --- | --- |
| `internal/kernel/agg.go` | `extremumAcc` replaced by `extremumNum[T]`, `extremumStr`, `extremumI128`, `extremumBool`, behind `newExtremum` |
| `internal/kernel/aggstat.go` | `newBoolExtremum` returns `extremumBool` |
| `internal/kernel/extremum_test.go` | new |
| `.github/workflows/ci.yml` | the vector-width capability guard (§5) |
| `bench/.gitignore`, `bench/results/REPORT.md` | the report is published (§5) |
