# Step 433 — as built

**Teeth for six kernel and dtype guards, recorded silent because no public query
reaches them.** The plan's 438, M2 of `v0.7.2-plan.md` (H3f1). Tests only: each
guard holds today, and each test is shown failing under its mutation. One item of the
plan's seven is not done; §2 says why.

## 1. The tests

**`internal/kernel/guards_internal_test.go`:**

- **`TestARankOrInterpolationTheKernelDoesNotKnowIsInternal`** (step 72). Planning
  refuses an undeclared method or interpolation before a kernel sees one
  (`ResolveWinFn`, `Agg.Field`). Past it:
  - `winRank` with method 99 returns an internal error naming it. Its default arm was
    once the average arm, writing to a buffer that did not exist.
  - `interpolate` with interpolation 99 panics with an internal error naming it, where
    it once answered linearly. Step 72 left the quantile kernel's default unrun.
- **`TestAPayloadFreeOperandIsRead`** (step 147): a column of nulls with no payload
  (`data.NewNull`) is given one to read (`readable`) by `bitCount` and by `cut`, and
  each answers five nulls. No public query hands them one: an aggregate over nothing
  and a typed null literal come with a payload.
- **`TestACumulativeProductNeverAdds`** (step 62): a `WinCumProd` to an Int128, a
  Decimal or a Duration is never the running sum. `winCumInt128` ignores its function
  and adds, so the gate's half `fn == WinCumSum` is what keeps a product out of it.
  No exact product binds today, which made that half silent. The test accepts a
  refusal or a panic, recovered as one, and fails only on the sum.

**`dtype/guards_dtype_internal_test.go`:**

- **`TestEveryPowerOfTenIsTheTruncatedOne`** (step 334): every one of the 696
  Eisel-Lemire entries is checked by multiplying back, where the table is built by
  dividing.
  - Each entry has its top bit set.
  - For q ≥ 0 it is 10^q's leading 128 bits, rounded down, or exact.
  - For q < 0, m·10^−q ≤ 2^k < (m+1)·10^−q.
  - 10^−348 and 10^0 equal Go's `strconv` table's entries.

  Eisel-Lemire's checks absorb an error of one in the last bit. That is why step 334's
  1.2 million inputs could not tell a table rounded up.
- **`TestCategoricalIsTextUnderneath`** (step 351): `Categorical.Physical()` is
  String, and it has string storage. Nothing routes a Categorical by `Physical()`, so
  the type stating its storage went untested.

## 2. Not done: the kurtosis insert pinned to Polars' bits

The plan's item 284 was to simulate the moment insert's m4 term with `math.FMA` and
pin it to Polars' bits under v3. Step 284 recorded this tooth as silent because fusing
it changed no answer probed: no input is known on which the fused and unfused inserts
differ. A test needs such an input first, and Polars, not on this machine, to give its
bits. The insert keeps its `float64()` barrier, as step 284 left it.

## 3. Teeth: 7 of 7 bite

| mutation | |
| --- | --- |
| the negative powers rounded up by one | **bites:** 10^−348 not rounded down |
| Categorical's physical type its own | **bites** |
| rank's default arm the first rank | **bites:** no error |
| interpolation's default linear | **bites:** answered 2 |
| a bit count reading its operand as given | **bites:** "no fixed-width payload" |
| cut reading its operands as given | **bites:** the same |
| the cumulative gate on any function | **bites:** cum_prod to Int128 and Decimal the running sum |

**Gate:**
- `dtype` and `./internal/...` in full, and vet;
- no code the root package runs changed;
- on the pull request, CI's every width, the scalar build and the race detector.
