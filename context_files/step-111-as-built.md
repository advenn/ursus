# Step 111 — as built

**Refusals name what the caller wrote.** This is `v0.4-scope.md` item 23's
misleading errors: `audit.md` S15, S16, A11 and A15. Each was reproduced against
master by a probe before any change. J10's `WhereExists` advice no longer appears,
and its unknown-column error now lists the suffixed names, so it needed nothing.

## 1. What each said, and says

| row | before | after |
| --- | --- | --- |
| S15 | `Duration * 1.5` refused with *"scaling it by 2.5 has no exact answer"* and the recipe `.Mul(2.5)`: a factor the caller never wrote | *"a fractional factor has no exact answer"*, recipe `.Cast(ursus.Float64).Mul(factor).Cast(Duration(ns))`. Only the operand's type reaches the resolver, so no factor is quoted. The recipe was checked to work. |
| S16 | `FillNan` on a String: *"is_not_nan() requires a floating-point operand"*; `FillNullWith` of the wrong type: *"when: the then and otherwise branches have no common type"*; `FillNull(FillMean)`: *"mean() requires a numeric operand"* | *"fill_nan() cannot be applied to a String column"* and *"fill_null() cannot be applied to a String column"*, with the original as the cause |
| A11 | `CumSum` of a String: *"sum() requires a numeric operand"* | *"cum_sum() is not defined for String"*, with what it accepts |
| A15 | a udf failing on the frame's fourth row, under `CollectBatches` with batches of two: *"udf "f" at row 1 of column "v""* | *"udf "f" failed on 4, row 1 of its batch of column "v""*, and *"panicked on …"* for a panic. A string input is quoted. |

## 2. How

- **`expr.Cond` gains `Sugar`,** the method a conditional was desugared from. When a
  sugared conditional fails to resolve, `Cond.Field` wraps the failure, keeping its
  kind, with a headline naming the method and the receiver's type. The receiver is
  the Then branch of every desugaring that sets it.
  - `FillNullWith` and `FillNan` set it.
  - `FillNull`'s min, max and mean strategies set it through `sugared`, on the
    conditional `Coalesce` builds.
  - A rewrite that rebuilds a `Cond` may drop the field. That is harmless: types
    are resolved before any rule runs, and that is where these refusals arise.
- **`cumulativeErr`** renames the refusal `cum_sum` and `cum_prod` borrow from
  `sum` and `product`.
- **The udf error names the input value,** which is what finds the row. The index is
  called what it is: the row's place in its batch, which is not its place in the
  frame under `CollectBatches`, a filter, or several workers. The panic handler's
  closure needed `src` declared before it.

**Two existing tests changed with the wording:**

- `TestPanicsAreErrors` pinned the old udf text.
- My first comment cited a test that does not exist; `TestEveryCitedTestExists`
  caught it, and it now names `TestDurationScaledByNumber`.

## 3. Tests and teeth

**`TestRefusalsNameWhatTheCallerWrote`:** five refusals. Each must contain its
phrases. The Duration one must not quote 2.5, and no first line may name
`is_not_nan`, `when:` or ` sum()`. The leading space is deliberate: "cum_sum()"
contains "sum()", which is how the check's first version failed.

**`TestAUdfFailureNamesItsInput`:** the fourth row under batches of two.

| tooth | result |
| --- | --- |
| `FillNan` without `Sugar` | **bites** |
| `Cond.Field` ignoring `Sugar` | **bites:** all three fills |
| `cum_sum` passing the borrowed error through | **bites** |
| the udf error naming no value | **bites** |
| the Duration hint quoting 2.5 again | **bites** |

**Gate:** test-all 105 ok, race 21 ok, levels, vet ×3 and the bench engine tests clean; PDS-H SF=0.1 22/22 against DuckDB.
