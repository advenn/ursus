# Step 26 — as built

h2o validated: **15/15 on parquet and 15/15 on CSV**. And the run turned out to
be the end-to-end measurement steps 22–24 never got — **the suite is 2.31x faster
than the published numbers, and gb8, its worst query, is 4.95x faster.**

Authoritative where it disagrees with [`step-25-as-built.md`](./step-25-as-built.md),
the vision docs and [`design/`](./design/).

One Go change, in the benchmark module only: `bench/micro/fixture_test.go` gained
a `TestMain`. `internal/` was not touched; 1415 test cases still green.

```
h2o, 1e+07 rows, ursus alone, one iteration, against the duckdb reference

  IO=parquet   15/15 answers match
  IO=csv       15/15 answers match
```

---

## 1. The machine could not run it, and that was my fault

The first thing the h2o run found was that this laptop could not take it:

```
Mem:    15 GB total, 12 GB used,  3 GB available
Swap:   24 GB total, 15 GB ALREADY IN USE
/tmp:  7.7 GB tmpfs at 99%        <- RAM-backed, so it IS the memory
```

ursus's h2o peak is over 5 GB. `bench/README.md`'s own rule is that a run which
swaps measures the swap, not the engine.

The 6.5 GB was **38 `ursus-micro-*` directories at 172 MB each**, leaked by my own
`bench/micro` runs across steps 22–25. `build()` called `os.MkdirTemp` and nothing
ever removed it — one leak per `go test` process, onto a RAM-backed filesystem.
Deleting them took `/tmp` from 7.6 GB to 1.4 GB and drained 7 GB of swap.

The fix is `TestMain`, not `b.TempDir()`. The distinction is the whole reason the
bug existed: the fixture is deliberately process-scoped — built once under
`sync.Once` and shared by every benchmark, because writing a million rows per
benchmark is not what any of them measure — so a per-benchmark directory would be
removed the moment the first benchmark finished while the rest still read the
files. `TestMain` is the process-scoped counterpart that was missing. `build()`
also now cleans up after itself when it fails partway, since that path leaves a
directory `TestMain` can never see (`shared` stays nil).

Verified by running a benchmark and confirming no `/tmp/ursus-micro-*` survives.

**What remains is not mine**: 12 GB of the 15 is GoLand, PyCharm, JetBrains
toolbox, Firefox and Telegram. Available memory went 3.3 GB → 6.3 GB after the
cleanup, and the h2o run completed with some swapping. For a CORRECTNESS run that
is acceptable — swapping makes it slow, not wrong — but it is why the timings
below are indicative rather than publishable.

---

## 2. The validation

15/15 in both IO modes. What each mode was for:

- **parquet** is where j1–j5 and gb8 live, and those are what exercise step 23's
  `KeyTable` hardest: the join build sink's table plus its `Merge` reordering, and
  the window sink's table. gb8 additionally runs a rank over a partition, so
  step 24's radix rewrite lands on it too.
- **CSV** is the only end-to-end check step 22's string rewrite can get — PDS-H is
  parquet-only, so 1.5 GB of h2o CSV input (542 MB `g1`, 495 MB `big`, 497 MB `x`)
  is the first time that path has been validated at scale.

**gb7 is right**, which matters: it is the query that returned a wrong answer
before step 19, and it passes through the rewritten min/max accumulators.

**How h2o is checked, and why it is weaker evidence than PDS-H.** `validate.py`
compares one-row checksums — row count plus the sum of a pinned column list from
`[checksum.h2o]` in `config/suites.toml` — not full answers, because a join at
1e7 produces tens of millions of rows. It is the check the h2o benchmark itself
applies. PDS-H's 22/22 compares answers in full; this 15/15 does not.

---

## 3. The measurement steps 22–24 never got

Step 24 shipped a 2.4–4.1x kernel improvement and said plainly that "the
end-to-end effect on a real query was not measured" — this laptop's drift
exceeded the effect on every available proxy. The h2o run supplies it by
accident, against the published step-20 column in `REPORT.md`:

```
query    step20      now     gain     vs polars then   vs polars now

gb1        1885      518    3.64x            11.8x            3.2x
gb2        3281      741    4.43x             3.5x            0.8x
gb3        6739     2184    3.09x             4.7x            1.5x
gb4        1696     1183    1.43x             9.5x            6.6x
gb5        5082     1553    3.27x             6.1x            1.9x
gb6        3248     1890    1.72x             3.8x            2.2x
gb7        2360     1527    1.55x             1.9x            1.2x
gb8       27973     5646    4.95x            20.4x            4.1x
gb9        4432     1271    3.49x             4.1x            1.2x
gb10      23405     9840    2.38x             4.0x            1.7x
j1         6842     4467    1.53x             6.0x            3.9x
j2         8253     5313    1.55x             7.9x            5.1x
j3        10477     6095    1.72x            15.1x            8.8x
j4         8503     5458    1.56x             8.4x            5.4x
j5        36235    20725    1.75x             7.9x            4.5x

geomean    6470     2801    2.31x            6.42x           2.78x
```

**gb8 is the one to read.** It was the worst ratio in the whole suite — 20.4x
polars — and it is a window rank over 10M rows, which step 24's profile showed to
be 92% sort. 27,973 ms → 5,646 ms is that prediction cashing out, and it is the
number step 24 could not obtain.

The group-by row — gb1 3.64x, gb2 4.43x, gb9 3.49x, gb5 3.27x, gb3 3.09x — is
step 23's `KeyTable` at a scale the micro benchmarks could not reach. On the
micro proxy it measured 1.16x; here it is over 3x, because 1e7 rows put the group
table far outside cache where an arena and a fused get-or-insert are worth much
more than they are at 1e6.

**Read these as indicative, not published.** One iteration against the report's
three, on a machine running two IDEs and swapping; the polars column is from a
different day. The effects are 2–5x and the drift I measured in step 23 was 30%,
so the signal is real, but the precise figures are not. `REPORT.md`'s provenance
note now carries the geomean and the gb8 figure with exactly that caveat.

**Not everything improved.** j1–j5 gained only 1.53–1.75x and remain the weakest
area at 3.9–8.8x polars — consistent with step 24's finding that no cheap proxy
exists for them, and they are now clearly the largest remaining gap.

---

## 4. A memory regression worth naming

gb10's peak went **4.95 GB → 6.94 GB (+40%)** while getting 2.38x faster. j5 went
the other way, 4.98 GB → 4.26 GB. The timings.csv history also holds a run from
earlier today where gb10 **OOM-killed under a 6 GB limit** — a limit it would have
fitted inside before.

The likeliest cause is not an algorithmic one: a query that finishes 2.4x sooner
gives the collector 2.4x fewer opportunities to run, so the high-water mark rises
even at an unchanged allocation rate. `KeyTable` should if anything be smaller
than the map it replaced (no 16-byte string header per key), and step 24's radix
buffer adds only 8 bytes per row — 80 MB at 10M, not 2 GB.

That is a hypothesis, not a finding. It is recorded because a 40% peak increase on
the largest group-by is the kind of thing that turns into someone's OOM, and
because step 24 already noted that transient kernel scratch is invisible to
`execopt`. Checking it wants `GOGC` and a memory profile, not a guess.

---

## 5. Verification

- `make validate SUITE=h2o` at `IO=parquet` and `IO=csv`: "all 15 answers match
  the duckdb reference" both times. `validation.csv` gains 30 rows, every one
  `valid=1`.
- `free -m` before and after the cleanup: available 3,283 MB → 6,348 MB.
- The leak fix: a benchmark run leaves zero `/tmp/ursus-micro-*` behind.
- `go vet` clean in `bench/micro`; the root module is untouched and still builds
  and tests clean.
- **`REPORT.md` checked with `git status` before committing** — the trap step 25
  fell into. Untouched this time; the only change is the provenance note, edited
  by hand.

---

## 6. What is still open

- **The join family is now the largest gap** — j1–j5 at 3.9–8.8x polars, having
  gained only ~1.6x while the group-bys gained 3x+. No cheap proxy exists on this
  laptop (`BenchmarkJoinInner` is scan-bound at 1M rows), but h2o at 1e7 now
  provides one that works, at roughly 40 seconds a query.
- **gb10's +40% peak memory** (§4).
- **REPORT.md's timings are stale** and now say by how much. Refreshing them needs
  a full multi-engine run.
- **What regenerated REPORT.md** in step 25 — still unresolved.
- **`windowSink` parallelism** and the **top-k rewrite** for gb8: less urgent now
  that gb8 is 4.1x rather than 20.4x, but still the two structural answers.
- **The arrow-go Parquet floor**, **unaccounted kernel scratch**,
  **`hashAggSink`'s unaccounted group table**, **`argExtremumAcc`/`positionAcc`**,
  **`nuniqueAcc`'s per-group sets**, **string kernels**, **CSE**, **`JoinWhere`**,
  **nested types**.
