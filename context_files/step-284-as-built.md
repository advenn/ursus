# Step 284 — as built

**Tier 2 of `v0.6.6-scope.md`, first part: the rolling functions.** These are Polars'
`rolling_skew`, `rolling_kurtosis`, `rolling_rank`, `rolling_cov` and `rolling_corr`.
`EwmSum` and `EwmMeanBy` are step 285; `CumulativeEval` and `Hist` step 286.

## 1. What changed

**Five rolling functions,** appended to the window ops in a second block, so no op
moves. `IsRolling` takes both blocks, so each gets the rolling functions' plumbing
with no change: typing, `.Over`, centring, and streaming with a bounded history.

**`RollingSkew(size, bias)` and `RollingKurtosis(size, fisher, bias)`,** Polars'
algorithm, ported from `crates/polars-compute/src/moment.rs` and `rolling/moment.rs`
at `py-1.44.1` (`internal/kernel/rollingmore.go`):

- **The state:** `pebay`, Polars' `SkewState` and `KurtosisState` in one: weight,
  mean, and the second to fourth central moments, by Pébay's insert and pairwise
  combine.
- **The queue:** `momentQueue`, Polars' `MomentWindow`. Values enter a back stack,
  merged into one state. When a value must leave and the front is empty, the back is
  walked newest first into the front, each entry the state of it and every value
  after it, and the front's top leaves. A window is the back's state merged with the
  front's top, so nothing is subtracted.
  - A NaN or an infinity enters as 0 and is counted; a window holding one is NaN.
  - The walk pops, then pushes, as Polars', and restarts a window that begins past
    the last one's end.
- **The answer:** Polars' finalize. A spread of zero to the mean's precision,
  `m2 ≤ (ε·mean)²`, is NaN, so values an ulp apart are NaN as in Polars. The
  corrections without bias need 3 and 4 values. A Float32 answers a Float32.
- **Rounding where Rust rounds:**
  - Go may fuse `a*b + c` into one FMA under GOAMD64=v3, across statements too; Rust
    never does. So every product feeding an addition or a subtraction is written
    `float64(a * b)`, an explicit rounding Go may not fuse. Three teeth run under v3
    show the fusion changes answers where the barrier is missing.
  - A probe found it first: `p := x * s` was fused into a later addition.
- **`m2^1.5`:**
  - Go's `math.Pow(x, 1.5)` is `Exp(Log(x)/2)·x`, which agreed with glibc's `pow`,
    Polars', for 12% of 50,000 values. So `pow15` is x·sqrt(x) in double-double,
    rounded once, which agreed for 99.9%.
  - Where the two differ, glibc's is the one off: its pow errs by up to 0.52 ulp. At
    row 201 of the fingerprint test the exact value is 0.497 ulp from pow15's answer
    and 0.503 from glibc's.
  - So a skew is Polars' to the bit but for about one in a thousand, an ulp apart. A
    kurtosis has no power and is Polars' to the bit.

**`RollingRank(size, method)`:**

- **Methods:** min, max, average and dense, as Polars' `rolling_rank`. Ordinal is
  refused, as Polars has none; Polars' random is not here.
- **The order:** values compare as `Sort` orders them, NaN above every number and −0
  with 0.
- **The window:** its valid rows kept sorted by binary insertion, a value's equals
  in arrival order, so a leaving row is the first of its equals. Dense keeps the
  distinct values with their counts.
- **What it takes:** numbers, instants and Booleans. Text and a Decimal are refused,
  as Polars refuses them. Min, max and dense are a UInt32, average a Float64.
- **`RollingCenter`:** a row is ranked in the window centred on it, its own value,
  not the padded trailing window the other functions take an answer from a later row
  of.
- **A difference:** Polars' centred rank answers null where the window's last row
  is null, though the row's own value is not. It checks the end's validity, written
  for trailing windows. Here such a row is ranked.

**`RollingCov(a, b, size)` and `RollingCorr(a, b, size)`,** functions of two
expressions, as Polars' `pl.rolling_cov` and `pl.rolling_corr`:

- **Polars' formula,** from rolling means and variances, pandas' way, over the
  window's rows where both hold a value, n of them:
  - cov = (mean(a·b) − mean(a)·mean(b)) · n/(n − ddof);
  - corr = that at ddof 1 / sqrt(var(a)·var(b)).

  `Ddof` sets the covariance's; a correlation ignores it, as Polars 1.40 deprecated
  it.
- **The kernel:** `rollingCovCorr`, three of `rollingFloat`'s states for a, b and
  a·b, over `PairOf(a, b)`, as a rolling function by time carries its by. It is named
  after a, as Polars names it, and its plan reads `rolling_cov(col("x"), col("y"),
  …)`.
- **Not sugar:** first it was to be built of `RollingMean` and `RollingVar`, as
  Polars builds it. But a Float32's `RollingVar` is a Float64 here, so two Float32s'
  correlation could not be a Float32, and an expression has no type to cast to until
  it is resolved. As an op it is typed where the types are known: a Float32 for two,
  a Float64 otherwise.
- **Last bits:** Polars' rolling means are its own, and the formula cancels, so its
  answers' last bits are not Polars'. 2.3333333333333317 is Polars' covariance of
  [1, 4, 2] with itself, where 7/3 is exact. They are held within 1e-12, and a
  Float32 within 1e-6.

**Streaming, and a gap it showed:**

- Over the whole frame each function streams with its window's rows as history, and
  over a key its partitions spill.
- Streamed, a window's state is rebuilt from each batch's history, so the queue's
  flips and the rolling state's periodic recompute fall at other rows. The last bits
  can differ from the answer in memory.
- A probe showed `RollingVar` and `RollingStd` already did: 35–52% of rows over
  `sin(i)·1000`. The streaming test has quarter-valued floats, whose sums are exact,
  so it never saw it. `feature-gaps.md` §1 records it now, against §10's claim of
  bit-exact spilling.
- The fix is to carry a rolling kernel's state across batches, as an ewm's is. That
  is wider than this step, so the streaming test here holds the ranks equal and the
  floats within 1e-9.

## 2. Tests and teeth

Every answer is Polars 1.44.1's: the moments and ranks by their bits, cov and corr
within a tolerance.

- **`TestRollingSkewAndKurtosis`:**
  - skew, unbiased, from 2;
  - kurtosis, unbiased, Pearson's, from 1;
  - integers, centred, over g;
  - alike values; a NaN in the window; alike after another value; an ulp apart;
  - near 1e8, centred; one row;
  - a fingerprint of 300 rows' bits for four forms;
  - a Float32;
  - a Boolean, a date, a Decimal and text refused.
- **`TestRollingRank`:**
  - min, max, dense, average;
  - integers, ties four ways, dense and min past a tie, from 1;
  - NaN above, −0 with 0, a Float32;
  - dates, Booleans, UInt64 past 2^63;
  - centred three ways, a null after;
  - text, a Decimal and ordinal refused.
- **`TestRollingCovAndCorr`:**
  - cov, corr, corr at ddof 0, cov from 2 at ddof 0;
  - integers, Float32s, a Float32 and a Float64, one side alike;
  - over g, rank over g;
  - the name; text refused.
- **`TestRollingMoreStreamPastTheirLimit`:** six functions over the whole frame and
  three over a key, under 16 KiB.

| tooth | result |
| --- | --- |
| skew by Go's Pow | **bites** |
| the front merged into | **bites** |
| a NaN as a value | **bites** |
| no zero spread | **bites**, once an ulp apart was added |
| unbiased skew by n−1 | **bites** |
| fisher inverted | **bites** |
| m4 insert's 6 | **bites** |
| m3 combine's 3 | **bites** |
| min as max | **bites** |
| dense as min | **bites**, once a tie below was added |
| a null row ranked | **bites** |
| a centred rank padded | **bites** |
| one side counted | **bites** |
| corr by the ddof given | **bites**, once ddof 0 was added |
| a Float32 for one | **bites** |
| cov named 0 | **bites** |
| not rolling, so not streamed | **bites** |
| v3: m3 insert fused | **bites** |
| v3: combine's m3 fused | **bites** |
| v3: combine's m4 fused | **bites**, once the fingerprints were added |
| v3: pow15's product fused | **bites** |
| no reset past the end | silent |
| v3: insert's m4 fused | silent |

The two silent teeth:

- **The reset is an equivalence:** a window that starts past the last one's end pops
  every value first, which leaves the queue as a reset leaves it.
- **Insert's m4,** fused, changed no answer probed. Its barrier stays, as a fusion
  the compiler may choose another day.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
