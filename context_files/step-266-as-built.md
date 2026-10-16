# Step 266 — as built

**F1 of `v0.6.6-scope.md`: folds and constructors.** These are Polars'
`concat_list`, `fold`, `reduce`, `cum_fold`, `cum_reduce`, `cum_sum_horizontal`
and `format`.

## 1. What changed

**`concat_list`, in the horizontal block** (`listsOf`, `internal/kernel/listset.go`):

- each row's operands end to end, a List operand's elements and another's value;
- a null value is a null element, and a null List makes the row's list null, as
  Polars makes it;
- the elements meet as a conditional's branches meet (`HorizontalInput`). Where
  Polars casts a number beside a string to text, this refuses the pair, naming the
  types.

**`expr.Fold`** (`internal/expr/fold.go`), one node for fold, reduce, cum_fold,
cum_reduce and cum_sum_horizontal:

- it holds the accumulator (none for a reduce, whose first expression is its
  accumulator), the expressions, and `Fn`, which builds `fn(acc, x)`;
- **`Expand` writes it out first** (`buildFolds`), innermost first, after expanding
  its expressions over the input's schema. A fold over `All()` folds every column, as
  Polars' does, where building it from the selection unexpanded would have made it
  several columns of one addition each;
- **fold and reduce** are the last accumulator, named after the first: Polars names
  `fold` after its accumulator, `literal` for a constant, and `reduce` after its
  first expression;
- **the running forms** are a Struct of the running values, `struct_of` of each
  aliased after the expression folded in, the Struct named `cum_fold`, `cum_reduce`
  or `cum_sum`;
- **cum_sum_horizontal** casts each field to the total's type, which `buildFolds`
  reads from the schema, as Polars' fields are of one type.

**Polars runs its function over Series per batch;** here `Fn` builds an expression
once, so the fold is one the optimizer sees through, and no function of the caller's
runs per row. `Rebuild` takes the node.

**`Format(f, args...)`** parses `{}` and the doubled braces when the expression is
built, and refuses a count of arguments that is not the placeholders'. It is a
`ConcatStr` of its pieces, so a null argument makes the row null, and it is named
after its first argument, `literal` with none, as Polars names it.

**Public (`fold.go`):** `ConcatList`, `Fold`, `Reduce`, `CumFold`, `CumReduce`,
`CumSumHorizontal` and `Format`. The functions are `func(acc, x Expr) Expr`.
`CumFold` has no `include_init`: Polars 1.44.1's errs on a literal accumulator.

**A difference from Polars,** in `feature-gaps.md` §9: `ConcatList` refuses a
number beside a string, which Polars casts to text.

## 2. Tests and teeth

**`TestConcatList`:** two values, a list and a value, an integer and a float, a
literal, two lists, each against Polars 1.44.1, and a number beside a string
refused.

**`TestFolds`:** every answer is Polars':

- a fold from a literal and from a column;
- a fold over `All()`;
- a reduce;
- cum_fold and cum_reduce by their fields;
- cum_sum_horizontal of an integer and a float, both fields Float64;
- a fold of two aggregates, one value;
- cum_reduce's field names in its schema.

**`TestFormat`:** two placeholders, text around one, escaped braces, and none,
against Polars, and too few arguments refused.

| tooth | result |
| --- | --- |
| a null list keeps its row | **bites** |
| a fold is named after its last column | **bites** |
| a reduce's first field is dropped | **bites** |
| running sums keep their own types | **bites** |
| selections fold unexpanded | **bites** |
| doubled braces are placeholders | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
