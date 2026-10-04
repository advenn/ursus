# Step 75 — as built

**Aggregations and windows answer correctly.** This is `audit.md` §7's silent wrong
answers, its crash and its refusal (A1–A5, A10, A13, A14), with W1 from §3. It is the
second step of the road to 0.3, after step 74's join keys.

Twelve commits and this document.

Authoritative where it disagrees with the vision docs and [`design/`](./design/).

Each of these was asserted on its own exit code:

- `make test-all` exit 0, with **105** package-ok lines: 21 packages × 5 SIMD
  configurations.
- `make race` exit 0 (21).
- `make levels` and `go vet` clean in all three modules.
- PDS-H SF=0.1 exit 0, with **22/22** matching the duckdb reference. TPC-H's means are
  over Decimal and Float64, so the integer mean change could not move them, and did not.

No benchmarks. The golden plans did not move.

**The suite is 2658 passing tests and subtests**, counted from `go test -json`'s pass
events as at step 74 (2624).

---

## 1. What was wrong, measured

Each row was traced to current code and measured against Polars 1.44 and DuckDB 1.5.

| row | today | the answer |
| --- | --- | --- |
| A1 | Int64 `ShiftFill(1, 1.5)` is `ErrInternal`: *select branches must share a type*. Int32 `ShiftFill(1, 0)` refuses its own Int64 zero. | Float64 `[1.5, 1, 2]`; Int32 `[0, 1, 2]` |
| A2 | `Quantile(0)` of `[−inf, 1, 2]` is NaN; the median of `[−1.7e308, 1.7e308]` and the midpoint of `[1e308, 1.5e308]` are +Inf. | −inf; 0; 1.25e308 |
| A3 | `PctChange` of UInt8 `[3, 1, 255, 0]` subtracts at UInt8: 84.67 where the answer is −0.67. | `[null, −0.667, 254, −1]` |
| A4 | The mean of Int64 `[2^53+1, 2^53+2]` is 2^53: summed in float64, while `Sum` of the same column is exact. DuckDB is exact; Polars is not. | 2^53+2, the nearest double to 2^53+1.5 |
| A5 | `Var(0)` of `[1e308, −1e308]` is **−Inf**. | +Inf, Polars' answer; DuckDB refuses |
| A10 | `Sum` of a Bool is refused, with a hint to use `Count()`, which counts rows. | the trues: 2 |
| A13 | Integer `Product` of `[3, 0, −5]` is −0. `GroupBy().Agg()` of nothing is shape (0, 0). | +0; one row |
| A14 | `Closed(99)` answers as `ClosedLeft`, in `GroupByDynamic` and `IsBetween`. | refused |
| W1 | `WithColumns(x*10 as w, w.sum().over(g))` sums the input's w: 200, where ursus's sequential rule says 30. With a new name, `unknown column`. | 30 |

**Polars differs on W1 by design.** It evaluates `with_columns` against its input.
ursus documents `WithColumns` as sequential, and step 70's O1 fix made predicate
pushdown honour that, so a window was the one expression that read the wrong frame.

## 2. Evidence first, in two commits

**E1 is `aggwindow_byhand_test.go`**: 22 cases, 19 wrong and listed in a two-way
`knownAggDefects` ratchet, and 3 controls. W1's two cases were already in the
optimizer's by-hand ratchet from step 70. Cases added beside the fixes bring it to 31,
4 of them controls:

- A Bool's grouped `Sum` and its `CumSum`.
- `CumProd`'s −0, and the empty aggregate over no rows.
- `Rolling` with `Closed(99)`.
- A window over an earlier window, a replaced column keeping its place, and a column
  read outside the window, which must not split.
- **A node split twice, with expressions after each split.** It was added after the
  teeth, in its own commit, because without it two teeth were silent (§4).

**E2 is `TestIntegerMeanIsTheNearestDouble`** (kernel), the twin of
`TestDecimalMeanIsTheNearestDouble`.
- **Columns:** every integer type's mean over values at its own edges, every other
  type's edges, and around 2^53. Each is computed whole and split across two merged
  accumulators.
- **Oracle:** `big.Rat`'s nearest double of sum/count.
- **Today:** the 32-bit and narrower types are exact in float64 by construction.
  Int64 (6), Uint64 (2) and Int128 (6) were wrong, counted in `knownInexactIntMeans`.

## 3. The fixes

1. **A2: `lerp` and `midpoint`** (`kernel/aggstat.go`), the only quantile code.
   - At an exact rank, or between equal neighbours, the quantile is that value.
   - Between values of opposite sign, `lerp` weights each end: `(1−t)·a + t·b`, which
     is DuckDB's form.
   - The midpoint of two values of one sign is `a + (b−a)/2`.
   - Nothing overflows that the answer does not.
2. **A4: an integer mean divides an exact sum.**
   - The binding is `Acc Int128, Out Float64`.
   - `Finish` divides once through `finishDecimal`, an integer being a Decimal of
     scale 0. `finishDecimal` read its carry unguarded, and only a 128-bit input
     allocates one, so it is guarded first.
   - Every mean shares the accumulator: GroupBy, window, rolling, spill, `list.mean`
     and `FillNull(Mean)`. E2's ratchet is empty.
3. **A5: Welford without the overflow.** When `delta` overflows from two finite values,
   the mean updates as `x/n − mean/n`, which stays finite. m2 then takes the +Inf the
   variance truly overflows to. The variance of equal huge values stays 0.
4. **A3: `PctChange` computes in float.**
   - It reaches a float first through an internal call, `FnMathAsFloat`, typed at plan
     time. An integer or Decimal becomes Float64, a Duration's ticks become Float64,
     and a float is kept. An instant is refused with a message about pct_change.
   - Dividing by an Int8 1 was tried first, as a way to reach a float with no type in
     hand. It refused Durations, whose `PctChange` had worked; the typed call keeps
     them.
   - The call-family count is 63.
5. **A1: the fill meets the column as `FillNullWith`'s does.**
   - It is built with `liftWeak`.
   - `expr.WinFnType` resolves the output through `ResolveCond`: a literal that fits
     keeps the column's type, and otherwise the two promote.
   - `WinFn.Field` and the physical planner both ask `WinFnType`.
   - `finishOrdered` casts the column and the fill to that type strictly before the
     kernel runs.
6. **A10: a Bool sums as 0 and 1**, as an Int128 like every integer sum.
   - `sumAcc` and `widenToInt128` gain Bool arms, so `CumSum` of a Bool counts too.
   - The hint is gone.
   - The one-row identity sweeps (`physical/aggexact_test.go`) read a Bool input as
     the 0 or 1 it sums as. A Sum of one true is 1, and agrees with the Min of it.
7. **A13: −0 and the row of nothing.**
   - An integer or Decimal `Product` or `CumProd` of zero is +0, normalised where the
     answer is published.
   - The aggregate sink and the rename after it carry a global aggregate's one row with
     `NewBatchRows` when there are no columns to derive it from.
8. **A14: `Closed.Valid()`**, beside `RankMethod`'s and `Interpolation`'s.
   - `TemporalGroup.Schema` refuses an undeclared value with KindValue. That covers
     `GroupByDynamic`, `Rolling` and `FromPlan`, at `Collect`, `CollectSchema` and
     `Explain` alike.
   - `IsBetween` refuses one at build.
9. **W1: `resolveWithColumns` splits the node** before the first expression whose
   window reads a name an earlier expression defines.
   - The result is `WithColumns{WithColumns{in, out[:i]}, out[i:]}`, and the upper
     half is resolved recursively, so it splits again if it needs to.
   - Only columns read inside a window count. A column read outside one is evaluated
     by the node itself, which already sees the earlier definitions.
   - The output is the one sequential walk, column for column.
   - Select is parallel and unchanged.

## 4. Teeth

Every patch was checked to have applied, and every one ran against a green baseline.
All 24 bite.

| reintroduce | fails |
| --- | --- |
| no exact-rank short-circuit | A2 Quantile(0) and Quantile(1) |
| `lerp` of opposite signs as `a + t(b−a)` / midpoint as `(a+b)/2` | A2 median / A2 midpoint |
| the integer mean in Float64 | the three A4 cases, E2 |
| the carry read unguarded | A4, the one-row identity sweep, and two window-aggregate tests; the plan predicted a crash on Int64 means |
| Welford's plain update | A5 Var and Std |
| `PctChange` at the column's width | both A3 cases |
| the fill not resolved / strong / column uncast / fill uncast | A1 1.5 / A1 0 / A1 1.5 / A1 0 |
| Bool refused in Sum / no `sumAcc` arm / no `widenToInt128` arm | all three A10 / Sum and grouped Sum / CumSum |
| Product's −0 kept / CumProd's | the A13 Product case / the CumProd case |
| `NewBatch` in the sink / in the rename | both empty-aggregate cases, each time |
| `Valid` always true / `TemporalGroup` unchecked / `IsBetween` unchecked | all three A14 and `TestEveryDeclaredMethodIsValid` / Dynamic and Rolling / IsBetween |
| no split | the three W1 cases and both optimizer W1 cases |
| the split stops at the window / does not recurse | the two-split case / the two-split case |

**Silent at first, and fixed:**

- **The split that keeps only the window, dropping what follows it.** Every W1 case
  ended at its window, so the case added in its own commit was needed. It also catches
  a split that does not recurse.
- **Two teeth did not build**, which is not a bite. `CumProd`'s left `exact` unused,
  and my first recursion tooth named a function that does not exist. Both were
  re-aimed, and both bite.

## 5. Behaviour changes

- **An integer mean is the nearest double to the exact sum over the count**, where it
  was a float64 running sum's. Past 2^53 the two differ.
- **A quantile at an exact rank over an infinity is that infinity**, not NaN. A median
  or midpoint of huge values is finite.
- **A variance that overflows is +Inf.**
- **`PctChange` of a narrow integer is computed in Float64.**
- **`ShiftFill`'s output type is the column's and the fill's common type.** A Go
  literal that fits keeps the column's type.
- **`Sum` and `CumSum` of a Bool count its trues**, as Int128.
- **An integer `Product` or `CumProd` of zero is +0.**
- **`GroupBy().Agg()` with nothing to aggregate is one row**, with no columns.
- **An undeclared `Closed` is refused** with KindValue.
- **A window in `WithColumns` reads the columns defined before it.** A plan with such a
  window has two `WithColumns` nodes where it had one.

## 6. Still open

- **A16, new:** `Diff` on an unsigned column subtracts at its width and wraps. UInt8
  `[3, 1, 255, 0]` diffs to `[null, 254, 254, 1]`, where Polars widens to Int16 and
  answers `[null, −2, 254, −255]`. `PctChange` no longer goes through it.
- **A8:** the sugar inside `Over` is refused, where Polars supports it. That is a
  feature.
- **A9, A11, A12:** hints and messages only.
- **A13's last sentence:** temporal `Median`, `Quantile` and `Std` are refused, and the
  docs do not say so.
- **A15.**
- The rest of the road to 0.3: scalar silent wrong answers next (S5–S13).
