# Step 309 — as built

**G2: `Having`** (`v0.6.7-scope.md` §4), as Polars' `GroupBy.having`.

## 1. What Polars 1.44.1 answers, probed

`having(*predicates)` returns the group-by, "equivalent to adding the predicates to
the aggregation and filtering afterwards":

- **a group is kept where every predicate holds;** a null one leaves it out;
- **predicates combine with &,** given several, chained, or as a list;
- **a predicate may read a column the aggregation does not;**
- **`sum()`, `len()`, `head()` and `tail()` honour it,** and so does
  `group_by_dynamic`;
- **an empty `agg()`** gives the keys of the groups kept;
- **a predicate of rows,** `pl.col("v") > 1`, fails: "cannot cast List type";
- **a predicate not Boolean,** a sum, is cast: every group whose sum is not 0 kept.

## 2. The API

```go
func (g *GroupBy) Having(preds ...Expr) *GroupBy
```

It returns a copy, its predicates added to those before it, so a group-by can be
reused.

## 3. How it works

**`Agg` with predicates** (`aggHaving`):

1. Agg's expressions and the predicates, each under a name no column has, aggregated
   together by the same `agg` that lowers and builds every group-by;
2. the result's predicates checked Boolean, and refused otherwise in Having's words;
3. a filter by them;
4. a select of every column but them.

**Every way a group-by is answered gets it:**

- the shorthands, `Sum`, `Len`, `Count` and the rest, all call `Agg`;
- a dynamic or rolling group's `Agg` builds its node from the same list, so its
  windows are filtered too, as Polars' are;
- `Head` and `Tail` filter the rows first by each predicate as a window over the
  keys, so that each row carries its group's, then rank as before. With no keys the
  window is the frame's.

**A predicate of rows is refused when it is written,** by `expr.IsAggregation`:
"having's predicate (col("v") > lit(1)) is not an aggregate of its group", naming
the bare column. Without the check the aggregation refused it under its hidden
name, `.alias("__having0")`.

## 4. Found on the way: a cast `Len` named "literal"

The tests cast `Len()` to compare it with Polars' UInt32 count, and the column came
out named "literal".

- `OutputName` names `Len()` "len" by a case of its own. Wrapped, in a cast or a sum,
  it fell to the fallback, which takes the first root column, and with none says
  "literal".
- Polars names `len().cast(Int64)`, `len() + 1` and `len().cast() * 2` "len", and
  `lit(1).cast()` "literal", probed: the first input's name.
- The fallback now recurses into the first input when there is no column. A literal
  still names itself "literal".

The root package, the docs' examples and every internal package passed with the
change before the gate. `TestLenKeepsItsNameThroughACast` holds four names, and §1
has the row, fixed.

## 5. Against Polars

**Every answer of §1 is Polars',** sums cast to its Int64, in `TestHaving`. The
guide's example, the regions of more than two orders, is Polars' answer.

**§9:** a predicate not Boolean is refused, where Polars casts it.

## 6. The site

`docs/guide/aggregation.md` on `dev` has "Filtering groups: having", new in 0.6.7,
with `Example_aggHaving`. The Polars page maps it.

## 7. Tests and teeth

**The tests:**

- `TestHaving`: fifteen answers, a dynamic group's, three refusals;
- `TestLenKeepsItsNameThroughACast`.

**Teeth, all biting:**

- predicates ignored;
- only the first predicate;
- a null predicate kept;
- `Head` and `Tail` unfiltered;
- `Having` replacing, not adding;
- a predicate of rows let through;
- a predicate not Boolean let through;
- a cast `Len` named "literal".

Two of them first failed to build, and were rewritten to compile. One was silent:
the chained test's second predicate alone kept the same groups as both. The case
now needs both, as Polars does: `Len() > 1` then `Max() < 6` keep a and b, the second
alone c too.

**Gate:** test-all 125 ok, race 25 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.

**CI's race job failed once** on `TestV066UnderAMemoryLimit`, the moments under 64
KiB: "the query is holding 64.3KiB, 1.3KiB of it in this operator".

- Nothing of this step is in that query: no `Having`, no `Head`, every aggregate
  aliased.
- Three local race runs passed, and so did the job rerun, every job green.
- A flake at the limit's edge, in §8 beside the heap bound's.
