# Step 342 — as built

**The `Arr()` namespace,** Polars' `arr`, after step 341's `Array` type: all of its 32
functions, `as_array` aside. `reshape` and `concat_arr` are what is left of §3's first
row.

## 1. How

An Array is held as a List each of whose rows has its size, so its functions are a
List's of the same elements. Three ways in:

- **A List's, by the API.** `Sum`, `Min`, `Max`, `Mean`, `Median`, `Std`, `Var`,
  `NUnique`, `ArgMin`, `ArgMax`, `First`, `Last`, `Get` (a constant or an expression),
  `Contains`, `CountMatches`, `Any`, `All`, `Len`, `Head`, `Tail`, `Slice`, `Unique`,
  `Join` and `Agg` are `ToList().List()`'s, written so in `expr_arr.go`: the same
  typing, operands and kernels, nothing new to agree with them. `Explode` is the
  expression's, which step 341 taught an Array.
- **A List's, its Array kept.** `Reverse` and `Sort`/`SortDesc`/`SortWith` are calls
  of the `arr` family (`arr.reverse`, `arr.sort`, list.sort's arguments), typed as
  the List's function of `List(inner)` and answering the receiver's Array; the kernel
  relabels the column a List, runs `list.reverse` or `list.sort`, and relabels the
  answer. `Shift` takes a value a row, as `List().Shift` does, so `arr.shift` is a
  call of a row's operands beside `list.shift`, its answer relabelled.
- **`Eval`** lowers the body over the Array itself: a body of an element's value, or
  a constant, is a `ListMap`, whose type is now an Array of the receiver's size over
  an Array (`ListMap.Field`; `Scope` takes either, and the evaluator builds it so).
  Any other body is refused, as Polars refuses it ("not allowed with non-length
  preserving expressions"); `Eval(body, true)` is `List().Eval`, a List of any
  body's, Polars' `as_list`.

Two read the size: `ToStruct` names its fields `field_0` on, or by the names given,
as many as the size (`arr.to_struct`, `list.to_struct`'s kernel), and `Dot` (`arr.dot`,
an operand) gathers the two arrays' elements side by side for the rows where both are
there, multiplies them as columns with `kernel.Binary`, and sums each row with
`list.sum`: a null element's product passes, a null array is a null row, and the type
is list.sum's of the product's (`expr.ArrDotType`), an Int128 of integers.

**Where ursus differs** (§9), as `List()` does: an integer sum is an Int128 (Polars:
Int64), `NUnique` passes a null over (Polars counts it), a null array's struct is a
null struct (Polars: a struct of nulls), `Std` and `Var` agree to the last few bits.
`Head`, `Tail` and `Slice` give a List, Polars' default; `as_array`, which panics on a
null row in Polars 1.44.1, is `List().ToArray(n)`.

## 2. Tests

**`TestArrayNamespace`,** Polars 1.44.1's answers over three Arrays of three, a null
element in each, a null array: 35 functions — the reductions, positions, `Get` of a
constant and of a column, `Contains`, `CountMatches`, `Len`, `Head`, `Tail`, `Slice`,
`Reverse`, `Shift` both ways, `Sort` and `SortWith`, `Unique`, `Eval` as an Array and
as a List, `Agg`, `Dot` of itself and of an array with a null row, `ToList`, `Join`,
`Any` and `All` — each value and type; `Std` and `Var` to a relative 1e-12; `Explode`;
`ToStruct` named and not; refused: an eval of another length and of an aggregate,
`ToStruct` of two names, `Dot` of another size and of text, and a List's.

**Updated:** the call families' count, 221.

**Teeth (11 of 11 bite):** a reversed Array a List; a sort's arguments dropped; a
shifted Array a List; a dot's second null row passed; a dot's second array not
aligned; to_struct's fields not named field_i; to_struct's names not counted; an
Array's eval a List; an Array's eval built as a List; eval of another length let
through; a dot of two sizes. The dot of an array with a null row of its own was added
for the fourth: against itself, the two arrays' nulls are one.

## 3. Performance

A million arrays of three integers, in memory, the median of seven, against Polars:

| | ursus | Polars |
| --- | --- | --- |
| the array itself, selected | 13.0 ms | — |
| `Arr().Sum()` | 11.2 | 2.27 |
| `Arr().Max()` | 7.0 | 12.7 |
| `Arr().Sort()` | 23.7 | 8788 |
| `Arr().Dot(a)` | 27.1 | 5.23 |
| `Arr().Eval(Element().Mul(2), false)` | 18.8 | 3.61 |

Polars' `arr.sort` of a million arrays takes nine seconds. The rest is mostly the
list's own pass through a query (§8, step 341).

**Gate:** test-all 135 ok, race 27 ok (the root package 591 s here), levels, vet ×3
and the bench engine tests clean; the 22 PDS-H answers match DuckDB.
