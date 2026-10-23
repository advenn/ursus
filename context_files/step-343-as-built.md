# Step 343 — as built

**`Reshape` and `ConcatArr`,** Polars' `reshape` and `concat_arr`: the last of
`polars-parity.md` §3's first row, after steps 341 and 342. The row is built.

## 1. What they are

| Polars 1.44.1 | ursus |
| --- | --- |
| `pl.col("x").reshape((-1, 3))`, `(2, 3)` | `Col("x").Reshape(-1, 3)`, `Reshape(2, 3)`: `Array(T, 3)` rows |
| `.reshape((-1, 1, 3))` | `Reshape(-1, 1, 3)`: `Array(Array(T, 3), 1)` |
| `.reshape((-1,))` of an Array or a List | `Reshape(-1)`: the values flattened |
| an Array's null row in a reshape | its size of nulls, as Polars lays it out |
| a List's null or empty row | no value |
| `group_by(g).agg(x.reshape((-1, 2)))` | `List(Array(T, 2))` a group |
| `pl.concat_arr(a, b, s)` | `ConcatArr(a, b, s)`: an Array of 2 and of 1 and a value an Array of 4 |
| `reshape((-1, 4))` of six values | refused: "cannot reshape 6 values into the shape [-1 4]" |
| `reshape((3, -1))` | refused: "can only infer the first dimension" |
| `concat_arr` of an Int64 and a Float64 | refused: one element type |

**Reshape** is a reshape of its own, `ReshapeArr`, its shape in `Args`. Its type is the
leaves', what a List's or an Array's elements hold, in an Array for each dimension
after the first (`expr.ReshapeType`). In a `Select`, a column of its own length, the
zipper implodes the column, reshapes the one list with `list.reshape`, keeps a list of
any Array, and explodes it; in an aggregation it lowers to `list.reshape` of the group's
values, a list of Arrays a group.

**`kernel.listReshape`** takes each list's leaves, checks the shape divides them and
that a given first dimension is theirs, and builds the Arrays innermost first, each a
List's layout of offsets a size apart. The leaves are one slice of the deepest column
where no Array below holds a null row (`leafRuns`), and gathered otherwise
(`leavesOf`), an Array's null row a `NullIndex` for each value it would hold.

**`ConcatArr`** is a call of a row's operands, `concat_arr`, typed strictly, as Polars
types it (`expr.ConcatArrType`): an Array operand's element type or another's, all one,
a List refused (Polars makes an Array of Lists, which is not built). Its kernel is
`concat_list`'s with the Arrays relabelled Lists, and the answer relabelled an Array of
the summed width: a null Array makes the row null, and a null value is a null element,
as Polars answers.

## 2. Tests

**`TestReshapeAndConcatArr`,** Polars 1.44.1's answers: `(-1, 3)` and `(2, 3)`;
`(-1,)`; a null value; three dimensions; an Array flattened, by two and by three, its
null row two nulls; a List flattened and by two, its null and empty rows none; no
value; in an aggregation; `concat_arr` of two Arrays, of an Array and a value with a
null, and of one value. Refused: six values by four, and in three rows of three; a
later dimension inferred; a zero dimension of values; a group of four by three; a
reshape beside a column of another length; `concat_arr` of a float and of a List.

**Updated:** the call families' count, 223; the evaluator contract and the
sliced-list sweep run `list.reshape` of `(-1)`, each list flattened.

**Teeth (10 of 10 bite):** an Array's null row no values; values the shape does not
divide; the rows not checked; a nested Array's type one off; an Array's rows one value
apart; a later dimension inferred; concat_arr of two element types; concat_arr's
Arrays as values; no value a null row; a group's reshape not lowered.

## 3. Performance

A million values, in memory, the median of seven, against Polars:

| | ursus | Polars |
| --- | --- | --- |
| `Reshape(-1, 4)` of a million values | 27.9 ms | 0.03 ms |
| `Reshape(-1)` of a million Arrays of three | 98.2 | 0.02 |
| `ConcatArr(a, a)` of a million Arrays of three | 49.7 | 24.3 |

Polars' reshape is a view of the column. Here the column is imploded, reshaped and
exploded, three copies; slicing the leaves where they are one run, not gathering them,
took 108 ms to 98 (§8).

**Gate:** test-all 135 ok, race 27 ok (the root package 1037 s here, the machine loaded by
other work: a build and a test suite beside it; CI's was 723 s the push before), levels,
vet ×3 and the bench engine tests clean; the 22 PDS-H answers match DuckDB.
