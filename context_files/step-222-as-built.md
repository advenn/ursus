# Step 222 — as built

**F1 of `v0.6.3-scope.md`, first part: `List().NUnique()`, `List().ArgMin()` and
`List().ArgMax()`.**

## 1. Why

The list namespace reduced a list with Min, Max, Sum and Mean, each the column
aggregate run over a list's elements (`listReduce`). Polars' list namespace also
counts distinct elements and finds where the smallest and largest are. Those are
three more column aggregates, so they cost a constant each and the type rule they
already have.

## 2. What changed

- **`internal/expr/call.go`:**
  - `FnListNUnique`, `FnListArgMin` and `FnListArgMax` end the list family;
  - they are named `list.n_unique`, `list.arg_min` and `list.arg_max`;
  - their type is the aggregate's (`ResolveAggBinding`): a Uint64 each, and
    arg_min and arg_max refuse an element type with no order.
- **`internal/kernel/listfn.go`:**
  - each runs through `listReduce` with its aggregate;
  - a count of no elements is 0, owed to an empty list and not to a null list, so
    a counting aggregate's answer takes the list's validity. The others answer null
    for no elements already.
- **`expr_list.go`:** the three methods. Their docs give the two places ursus
  answers differently from Polars, each because its column aggregate does:
  - n_unique skips a null element, where Polars counts it;
  - NaN is the largest value, where Polars' arg_max skips it.

  Polars also answers UInt32s.

## 3. Tests and teeth

- **`TestAListsNUniqueAndArgExtremaAnswerAsPolars`:**
  - integers with nulls, an empty list, a null list and a list of nulls; floats
    with NaN and ±0; strings;
  - against Polars 1.44.1's answers, asked of it in the bench's environment. The
    differing cells are marked with Polars' value beside them.
- **`TestAListsAggregatesAreItsElementsAggregates`:**
  - 300 random lists of floats with nulls, NaN, ±0, infinities and repeats, read in
    batches of 17;
  - each present list answers as `NUnique`, `ArgMin` and `ArgMax` over its
    elements, exploded and grouped by row.

- **`TestCallFnFamiliesDoNotOverlap`** counts 90 classified functions. The first
  gate found it still at 87: the filtered test run before it had not matched its
  name.

| tooth | result |
| --- | --- |
| a null list counted as empty | **bites:** the Polars test |
| arg_min read as arg_max | **bites:** both |
| arg_max read as arg_min | **bites:** both |
| n_unique read as count | **bites:** both |
| the type resolver gives arg_min the element type | **bites:** both |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
