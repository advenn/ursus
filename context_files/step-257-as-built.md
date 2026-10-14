# Step 257 — as built

**E6 of `v0.6.5-scope.md`: List literals.** A Go slice given to `Lit` is one List
value. Also `Expr.Gather`, the reshape E3 left for them.

## 1. What changed

**`Lit[T Literal | ListLiteral]`.** `ListLiteral` is the slices of the scalar
literal types:

- `[]byte` is absent: it is a Binary literal, as it always was;
- the terms are exact, so a defined slice type is converted first.

**The literal.** `listLit` (`litreflect.go`) makes each element the literal it would
be on its own, and the List's element type is the zero element's, so an empty slice
has one too. It holds them as `[]*expr.Lit`. That shape gives the literal:

- **its text:** `lit([1, 2])`, by `Lit.text`, so two lists read apart wherever an
  expression's text keys a computation;
- **its column:** in `litColumn`, the elements' columns end to end, one list one row
  long.

**Where it goes.** A List literal is a column one row long, so every operand that
takes one takes it:

- `Col("x").IsIn(Lit([]int64{1, 2}))`, whose list goes into step 254's hash set
  once;
- `Lit(...).List().Contains(Col("v"))` and `.Get(Col("i"))`;
- a `WithColumns` beside the frame's columns, the list in every row.

**A `Select` of literals alone,** a List literal among them, is a row for each of the
frame's rows, as step 250 left it. Polars' is one row. `feature-gaps.md` §9 now
records it.

**`Expr.Gather(indices)`**, Polars' `gather`, as a reshape:

- a List literal of indices is the same for every group, the literal itself the
  second operand of `list.gather`;
- a column of indices is each group's own, imploded beside the values. The values may
  be reshaped first, `Sort().Gather(...)`: the indices are positions, not partners,
  so there is no length to keep equal;
- an index out of range is refused, as Polars refuses it, and a negative one counts
  from the end;
- refused: one index (`Get` is that), and indices with an aggregate, a reshape or a
  filter in them.

## 2. Tests and teeth

**`TestListLiterals`.** Every value is Polars 1.44.1's for `pl.lit` of the same list,
but the height of a `Select` of one alone:

- the literal alone, and its text;
- beside the columns, summed;
- `IsIn` of it, a value a row in it, a row's index into it, and a Float64 list
  against an Int64 column;
- an empty list, strings, instants and durations.

**`TestGatherInAnAggregation`.** Polars' answers for:

- a literal's indices, a negative one, and a column's;
- a gather after a descending sort, summed;
- refused: an index out of range, one index, and a gather outside an aggregation.

| tooth | result |
| --- | --- |
| a list literal loses its elements | **bites** |
| a list literal reads without its elements | **bites** |
| every list literal is a List of Int64 | **bites** |
| a literal's indices are taken for one | **bites** |
| a column's indices are the first | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
