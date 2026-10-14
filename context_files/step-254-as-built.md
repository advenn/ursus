# Step 254 — as built

**E5 of `v0.6.5-scope.md`, first part: a list's argument as an expression.**
`IsIn` against each row's own list, and `List().Get` and `List().Contains` with a
per-row index and value.

## 1. What changed

**Three call functions in the horizontal block,** where an operand is a column:

- **`is_in_expr`:** a value in its row's list;
- **`list.get_expr`:** the element at the row's index;
- **`list.contains_expr`:** the row's value among its list's elements.

The constant forms stay in their own families, with their fast paths.

**Their typing (`ResolveHorizontal`):**

- **`is_in`:**
  - needs a List;
  - a column of the value's own type is refused, as Polars 1.44 deprecates it for
    being ambiguous, with a hint naming `Implode`;
  - the value and the elements meet as `==` would compare them, and a pair `==`
    refuses is refused.
- **`list.contains`:** the same meeting, the other way round.
- **`list.get`:** needs an integer index. Polars also takes a float one; that is
  refused here, with a hint to cast.

**The kernels (`internal/kernel/listexpr.go`):**

- each casts both sides to the meet type and compares their group-key encodings,
  so NaN matches NaN and -0.0 matches +0.0, as the constant forms do;
- **`is_in`** is null where the value or the list is, and false for a value its
  list does not hold, a null element too: Polars' `is_in` with `nulls_equal=False`;
- **`list.contains`** finds a null value where the list holds a null, as Polars'
  `list.contains` does;
- **`list.get`** counts a negative index from the end, and is null out of range or
  where the index is null. Polars' `list.get(expr)` errs out of range unless
  `null_on_oob`; ursus's `Get` is null either way, as for a constant;
- **one list, one row long** (a literal) against every row goes into a hash set the
  first time it is longer than 16 elements, rather than being scanned for each row.

**Public:**

- **`IsIn[T Operand](vs ...T)`** takes values as before, or one expression, a List
  column;
- **`List().Get[I int | Expr](i I)`** takes an index or an expression;
- **`List().Contains(v any)`** takes an expression as well as a value.

Calls with constants compile as before. `feature-gaps.md` §9 records `Get`'s two
differences from Polars: null out of range, and a float index refused. `IsIn(Col("b").Implode())`, membership among
a whole column's values, is refused. As a scalar it would be broadcast to every row
as a list of the whole column, a copy a row. It needs the list computed once, as a
literal one row long: after E6's list literals.

## 2. Tests and teeth

**`TestListArgumentsAreExpressions`.** Eight rows of a List(Int64), with null lists,
empty ones and null elements, beside a value, an index and a needle each. Every
answer is Polars 1.44.1's:

- plain;
- cast: an Int8 value, an Int8 index and a Float64 needle;
- as strings;
- at batch sizes 1, 3 and 8192;
- summed in an aggregation;
- a literal index.

**`TestIsInAListMatchesNaNAndZeroes`:** NaN and -0.0 against Polars.

**`TestListArgumentsRefuse`:** what `==` cannot compare, a float index, a column of
the value's own type, two expressions and an aggregate.

**`TestOneListAgainstEveryRow`** (kernel): the hash set and the scan answer alike.

| tooth | result |
| --- | --- |
| is_in a null value is false, not null | **bites** |
| list.contains never finds a null | **bites** |
| list.get counts a negative index from the start | **bites** |
| is_in leaves the value at its own type | **bites** |
| the set misses every value | **bites** (the kernel test) |
| is_in takes a column of its own type | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
