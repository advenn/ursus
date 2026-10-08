# Step 121 — as built

**What the audit of 0.4's expression work found, fixed.** The read-only review of
steps 100–107 and 114 traced three defects in what they changed. Each was reproduced
by a test first.

## 1. The defects

| | what happened | why |
| --- | --- | --- |
| a guarded branch | `When(x.Lt(10)).Then(x.Mul(2)).Otherwise(0)` over an Int128 column holding Max failed with *"overflows Int128"*, for the row the guard sent to `Otherwise`. A strict cast guarded the same way failed the same way. | Both branches are evaluated over every row, and `evalCond`'s doc said that was safe because "an arithmetic fault produces a NULL". Since step 100 an Int128 overflow is an error. A strict cast always was. |
| Int128 `Neg` and `Abs` | Of -2^127 they returned -2^127, where `Lit(0).Sub(x)` refused it. | They predate step 100's "exact or refused" and wrapped as the narrower types do. |
| TopK of ±0 | `TopK(1)` of a group of -0.0 and +0.0 answered whichever arrived first. A parallel group-by decides that. | The total order calls them equal, and the accumulator keeps the first of equals. |

## 2. The fixes

- **`evalBranch`:** a branch is evaluated over the batch as before. If that fails
  with a value error, it is evaluated again over only the rows it is taken for: the
  true rows for `Then`, and the rest, nulls included, for `Else`. The result is then
  scattered back, with null elsewhere.
  - The common case costs nothing more.
  - An error on a row the branch IS taken for still surfaces, numbered within those
    rows.
  - Polars evaluates both branches too, and wraps its integers instead.
- **`unaryI128` refuses the minimum** for `Neg` and `Abs`, with the overflow message
  the binary operators use. A null minimum is not judged.
- **TopK compares float zeros by sign:** +0.0 ranks above -0.0.

## 3. Tests and teeth

`expraudit_test.go`:

- **`TestAGuardedBranchFailsOnlyOnItsOwnRows`** covers `Then` and `Otherwise` each
  guarded, an unguarded row that still fails, and a guarded strict cast.
  - The test's first version guarded with `Abs()`, which the step itself makes refuse
    the minimum: the guard needed a guard.
- **`TestInt128NegAndAbsAtTheMinimum`.**
- **`TestTopKOrdersSignedZeros`**, both arrival orders.
  - Its first version used `Explode()` as an expression, which is refused. It reads
    `List().First()`.

| tooth | result |
| --- | --- |
| no retry of a branch | **bites:** both branches and the cast |
| `Else` retries `Then`'s rows | **bites** |
| Int128 `Neg` and `Abs` wrap again | **bites** |
| TopK ties ±0 by arrival | **bites** |

**Gate:** with steps 118–122; see step 122.
