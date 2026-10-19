# Step 287 — as built

**The 0.6.6 report, and its documents.** It ran on step 286's code. One fix its plan
diff found changed code; a test of the scope's second claim was added.

0.6.6 is **feature completeness, part 2: namespaces and generators.** Steps 263 to 286
built it:

- **G1–G3 (263–265):** generators, temporal ranges, and values from their components;
- **F1, F2 (266, 267):** folds and constructors; selection by position and the index
  functions;
- **§3 (268):** `List().Eval` and `Element()`;
- **L, S, D, C, B, T (269–275):** the `.list`, `.str` and `.dt` methods, `.cat` on an
  Enum, `.bin`, and struct fields of several names;
- **M1–M5 (276–283):** the statistics, integers as bits, search and counting,
  sequences and run lengths, and the numeric odds;
- **tier 2 (284–286):** the rolling moments, rank, cov and corr; `ewm_sum` and the
  ewms by time; `hist` and `cumulative_eval`.

## 1. The scope's claims

**It answers as Polars 1.44.1 does.** Every step's tests hold each answer to Polars'
on the same inputs. Many hold to the bit, the rounding, the moments, the ewms and
`hist` ported from Polars' own source. The exceptions:

- **The scope's §6.3:** `Hash` is ursus's own algorithm, `Sample` and `Shuffle`
  ursus's own generator, and `ApproxNUnique` counts exactly.
- **`feature-gaps.md` §9:** each difference is recorded on purpose. They include:
  - a skew an ulp apart about once in a thousand, where glibc's pow, Polars', rounds
    the wrong way near a midpoint;
  - `Cot`'s and `Tan`'s last bits, Go's tangent;
  - a covariance's and correlation's last bits, on ursus's rolling means;
  - text answers where Polars' are Categoricals, as `Cut`'s are;
  - first-seen orders where Polars' are its hash table's;
  - a centred rolling rank where Polars checks the window's end.

**It spills or streams where its pieces do.** Steps 284 and 285 tested their windows
under a limit. The rest had no such test, so this step added
`TestV066UnderAMemoryLimit`: one query an item, 20,000 rows in batches of 64, under
16 KiB. A group-by over the frame's 2,000 keys runs under 64 KiB instead, and must
spill there.

- **The same answer as without a limit:**
  - a generator of literal arguments, a date range a row;
  - `CumSumHorizontal`, `ArgSort`, `List().Eval` of a rank;
  - `ContainsAny`, `IsoYear`;
  - the bitwise and small aggregates by key, the bits of integers;
  - `ValueCounts`, `UniqueCounts`, `SearchSorted`, `Append`;
  - `RoundSigFigs` and `Cot`.

  Per-row functions stream a batch at a time; a reshape in a `Select` spills through
  the frame operator it is written in; an aggregate spills with the group-by.
- **The same answer as one thread:** skew, kurtosis and entropy by key spill. But
  their last bits, like `Sum`'s, `Mean`'s, `Var`'s and `Std`'s, depend on how many
  threads merged their partial states. A probe of eight aggregates found all but
  `Corr` differing between one thread and eight. A spilled run equals the one-thread
  run to the bit. That is ursus's, not 0.6.6's, and `feature-gaps.md` §1 records it
  now.
- **Refused, each by name, each recorded:**
  - `CorrSpearman` and `Mode` by key: their per-group pairs and values do not spill;
  - `RleID` and `Rle` over the whole frame, which needs the partition, as step 281
    said;
  - `SampleN` and `Hist` in a `Select`, which implode the column, as implode always
    refuses;
  - `CumulativeEval`, each of whose rows reads its whole prefix;
  - **a generator whose argument is an aggregate of the frame, beside a string
    column:** `WithColumns(IntRange(0, Len(), 1))`.
    - The plan stacks the frame beside the generator's branch, a keyless aggregate,
      which reads all its input before it answers, so the frame's side is held
      meanwhile.
    - When the limit is reached, the aggregate, holding 1.2 KiB and nothing it can
      spill, refuses, rather than the stack spilling what it holds.
    - An int-only frame's rows are the scan's own buffers, uncharged, so the probe
      over integers never showed it.
    - A literal end streams, and so does `WithRowIndex`.

  The test asserts each refusal, so the one that lifts it moves its case up.

**No regression.**

- **The plan diff:** every PDS-H and h2o query's optimized plan was printed by the
  bench's own query code, built against v0.6.5 (`git archive v0.6.5`) and against this
  code, as step 261 did.
- **What it found:** 36 of the 37 were byte-identical. The 37th, h2o's gb9, read
  `lit(1).len().alias("len").cast(Float64).alias("n")` where v0.6.5 read
  `lit(1).len().cast(Float64).alias("n")`.
  - Step 273 named `Len()` "len" with an Alias node. It said an alias of an alias
    replaces the name, so the benchmarks' plans would not change. gb9 casts between
    the two, and the inner alias stayed.
  - **The fix:** `OutputName` names an `AggLen` of a literal "len", which is what
    ursus's `Lit(x).Len()` means, a count of rows. `Len()` is `Lit(1).Len()` again,
    as at v0.6.5.
  - Now all 37 plans are **byte-identical**, and step 273's naming tests hold:
    removing the name fails `TestGetCategories`.
- **So no timing A/B ran,** as at 0.6.5; 0.6.4's figures stand.
- **The answers:** PDS-H's 22 at SF=0.1 matched DuckDB's at every step's gate. h2o's
  15 ran once for this report, at the suite's default 10,000,000 rows over Parquet
  on 8 threads, each in 0.1 to 0.6 seconds, and all 15 match DuckDB's.
- **The 34 golden plans** held at every gate.
- **Found on the way, not 0.6.6's:** a count reads every column. `Select(Len())` over
  a Parquet scan projects all of them, where Polars reads none. That is a
  performance release's (`feature-gaps.md` §8).

## 2. What 0.6.6 leaves

**Of its own scope:**

- `.arr`, which waits for an Array type (§6.1);
- an integer by with a `"2i"` half-life or period, for the ewms and rolling windows
  by time;
- `rolling_rank_by` and Polars' random rank; `rolling_map` and weights;
- `CumulativeEval` in `GroupBy(...).Agg` and inside `List().Eval`;
- the spills above: Spearman's and the mode's values, a generator's held stack, a
  streamed rolling var or std, kurtosis, skew, cov or corr to the bit.

**Found by it, wider than it,** in `feature-gaps.md` §5: a window inside a window,
and an aggregate inside an aggregate's input, both refused where Polars takes them.

The rest of feature completeness is 0.6.7, frame operations, and 0.6.8, I/O, then
the types, as the scope's §9 lists.

## 3. The documents

- **`CHANGELOG.md`:** the 0.6.6 section, dated for the report: where it ends, its
  known limitations, and `Len()`'s fix.
- **`README.md`:** the expressions row, and why the figures stand.
- **`feature-gaps.md`:** §1 the generator's refusal and the thread count's last
  bits; §5 the aggregates whose values do not spill; §8 the count that reads every
  column.
- **`v0.6.6-scope.md`:** where it stands.

## 4. Tests and teeth

- **`TestV066UnderAMemoryLimit`:** fourteen items equal under the limit, the moments
  equal to one thread, eight refusals.
  - **The first gate failed it:** the group-bys over the 2,000 keys, the small
    aggregates, the moments and `UniqueCounts`'s frame form, ran at 16 KiB first.
    Their states sit at that edge: 16.1 to 19.3 KiB held under `GODEBUG=simd=128` and
    the race detector, within it otherwise, one run in twenty.
  - They run under 64 KiB now, still a fraction of their state, so each must spill,
    which the test asserts.
  - The test passed twenty runs at each SIMD width, ten scalar-only and three under
    the race detector.
- **A tooth:** removing the "len" name from `OutputName` bites (`TestGetCategories`).

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
