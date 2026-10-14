# Step 261 — as built

**The 0.6.5 report, and its documents.** No code changed. Everything ran on step
260's code (`git log -1` at the report: "feat: unnest as an expression").

0.6.5 is **feature completeness, part 1:** the bugs `feature-gaps.md` §1 found, and
the expression model. Steps 248 to 260 built it:

- **B1–B3 (249):** `FillNull` with a strategy, `Dt().Date()` and `Dt().Time()`, and
  seven stale documents;
- **E1 (250):** an aggregate in `Select`, `WithColumns` and `Filter` is a scalar,
  and a window in `Filter` is computed;
- **E2 (251):** `Expr.Filter` as an aggregate's input;
- **E3 (252, 253, 257):** length-changing expressions in an aggregation, among them
  `Gather`;
- **E5 (254–256):** expression-valued arguments;
- **E6 (257):** List literals;
- **E4 (258–260):** length-changing expressions in `Select` and `WithColumns`, and
  `Explode` and `Unnest` as expressions.

## 1. The scope's claims

**It answers as Polars 1.44.1 does.** Every step's tests hold each answer to Polars'
on the same inputs. The differences are recorded in `feature-gaps.md` §9, each on
purpose:

- `GroupByDynamic` with keys puts its index first (251);
- `List().Get` is null out of range, and refuses a float index (254);
- a null replacement value gives null (255);
- a `Select` of literals alone is a row for each of the frame's rows (250, 257);
- `DropNans` of a column that is not a float, and `Explode` of one that is not a
  List, are refused where Polars returns them as they were (253, 258, 259).

**The old refusals flipped:**

- `agg_test.go`'s refusal of an aggregate in `Select`, at step 250, with four others
  its record lists;
- the four stubs: `Expr.DropNulls` (252, alone in `Select` at 258), `Expr.DropNans`
  (253, 258), `Expr.Explode` (259) and `Expr.Unnest` (260).

**It spills or streams where its pieces do:**

- **E1:** a broadcast aggregate is a window with no partition, which streams
  (`TestABroadcastAggregateStreamsPastItsLimit`, 250);
- **E2:** a filtered aggregate feeds the same accumulators, which spill
  (`TestAFilteredAggregateSpills`, 251);
- **E3:** a group's values are held by its implode, which refuses past the limit, as
  implode always has;
- **E4:** each column is a plan of its own whose sort, reverse or filter spills as the
  frame-level one does (`TestLengthChangingExpressionsSpill`, 258).

**No regression.**

- **The plans the suites run are unchanged.** Every PDS-H and h2o query's plan
  (`Explain`, optimized) was printed by the bench's own query code, built against
  v0.6.4 (`git archive v0.6.4`) and against step 260's code. The 37 plans, 22 PDS-H at
  SF=0.1 and 15 h2o at 2,000,000 rows, are **byte-identical**. The one hot path a
  step touched in place, the aggregate's input at step 251, costs an unfiltered
  aggregate one check of its expression a batch.
- **So no timing A/B ran,** as the scope's §3 asks of a release that touches no path
  the suites share. The figures of 0.6.4's report stand.
- **The answers:** PDS-H's 22 at SF=0.1 match DuckDB's at every step's gate, and
  h2o's 15 at 2,000,000 rows, over Parquet, ran once for this report and match too.
- **The 34 golden plans** held at every gate.

## 2. What 0.6.5 leaves

**Of the expression model:**

- **`IsIn(Col("b").Implode())`,** membership among a column's values, moved to 0.7:
  a scalar subquery, the implode run once before the query that reads it (step
  258). E1's broadcast scalars would use the same, read twice rather than spilled
  once;
- **a length-changing expression in `Filter`;**
- **`Gather` in `Select`;**
- **a reshape of a selection of several columns,** `Col("a", "b").Sort()`, which
  only the input's schema expands;
- **the rest of Polars' length-changing expressions** (`feature-gaps.md` §2):
  `ArgSort`, `Append`, `ExtendConstant`, `Sample`, `Shuffle`, `Rle`, `RleID`,
  `RepeatBy`, `ArgTrue`, `ArgUnique`;
- **Series literals** (a column of values), and the selectors' set algebra.

The rest of feature completeness is 0.6.6 to 0.6.8, as the scope's §5 lists.

## 3. The documents

- **`CHANGELOG.md`:** the 0.6.5 section, where it ends, its breaks and its known
  limitations, dated for the tag.
- **`README.md`:** the expressions row, and why the figures stand.
- **`feature-gaps.md` §2:** what 0.6.5 built.
- **`v0.6.5-scope.md`:** where it stands.

**Gate:** no code changed since step 260's gate.
