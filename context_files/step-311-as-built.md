# Step 311 — as built

**A null condition takes `Otherwise`,** as Polars' `when` and SQL's `CASE WHEN` do.
Not a scope item: step 310 found that ursus made such a row null, by design and in no
document, and the maintainer chose Polars'.

## 1. What changed

**The rule.** A row whose condition is null falls to the next `When`, or to
`Otherwise`:

- `When(b).Then(x).Otherwise(0)` of `b = [true, false, null]` is `[x, 0, 0]`, Polars'
  answer, where it was `[x, 0, null]`;
- a chain's null condition tries the next one: Polars' `[2, 3, 1]`, held.

**Where:**

- `kernel.Select` reads a null mask as false, its Else branch. A Boolean, text, a
  fixed width, and step 310's `Take` path for Lists and Structs alike.
- `Cond.field` makes the result nullable only where a branch is; a nullable condition
  no longer adds a null of its own.
- The comments that said "neither branch" say what is true now, in the kernel, the
  typing, the evaluator, `When`'s doc and the concepts page.
- `evalBranch`, which re-evaluates a failing branch over its own rows, needed nothing:
  it already gave the Else branch every row not true.

**The CHANGELOG lists it under breaks.** A query whose condition can be null answers
differently.

## 2. What the library builds from conditionals

Each was read for a condition that can be null:

- **`Coalesce`, `FillNull` and its strategies** test `IsNotNull`, never null.
- **`Clip`** of a null value still gives null: its comparisons are null, and it falls
  to `Otherwise`, the value. Of a null bound, it now clips nothing on that side,
  Polars' `[10, 20, null]`, where it gave null.
- **`IsClose`** of a null is still null, its finiteness test null.
- **`PeakMax` and `PeakMin`** test `IsNull().Shift(n).IsNull()`, never null.
- **`FillNan` leaned on the old rule.** Its condition is `IsNotNan`, null on a null
  row, and its doc said the null row survived because a null mask took neither
  branch; `TestFillNanLeavesNullsAlone` failed at once. The condition is now
  `IsNotNan or IsNull`, true on a null row, which keeps it. `Then` is still the value,
  so the column keeps its name.

**`When(Null(NullT))`,** a condition no Boolean, which Polars refuses, is every row's
Otherwise here.

## 3. Tests that changed, each now Polars' answer

- `TestWhenThenOtherwiseChain`: a null score grades "F";
- `TestCondNullPredicateTakesNeitherBranch` is `TestCondNullPredicateTakesOtherwise`:
  `[10, 20, -3, -4]`, and the result not nullable;
- `TestConditionalOfNestedTypes`: the null row is `[9]` and `{x: 0}`;
- internal/expr's `TestFieldResolution`: a nullable condition's result not nullable;
- the kernel's three select tests: a null mask takes the else branch, its payload bit
  ignored.

`TestWhenNullConditionAsPolars` is new: a chain, a null literal, `Clip` of null
bounds.

## 4. Teeth, all biting

- a null mask null again. It breaks more than the null tests: the typing says a
  result is not nullable, and the old kernel puts nulls in it, which the verifying
  collect refuses;
- a nullable condition nullable;
- `FillNan`'s nulls filled.

**Gate:** test-all 125 ok, race 25 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.

The first gate failed `TestEveryCitedTestExists`. A comment in `select_test.go`
still cited `TestSelectNullMaskTakesNeitherBranch`, which the renamed test's own old
comment block had kept above the new one. The two comments are now one, and it keeps
the old one's point: `Filter` and `When` agree that a null predicate is not true.
