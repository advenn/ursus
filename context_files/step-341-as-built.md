# Step 341 — as built

**The `Array` type,** the first part of `polars-parity.md` §3's first row: Polars'
fixed-size list, `pl.Array(T, n)`, held, cast, read and written. The rest of the
`.arr` namespace, `reshape` and `concat_arr` come next.

## 1. What it is

`dtype.Array(inner, size)` existed with no column. An Array column is now a List's
layout under the Array's type: offsets over a child, each valid row of `size`
elements, each null row of none, as a List's null row is (`dtype.IsListLike`, the
storage question). So `Lists()`, slicing, `Take`, concatenation, explode and the
spill read an Array as they read a List, and keep its type; and Arrow's layout, where
a null row owns its slots, is met at the edges.

| Polars 1.44.1 | ursus |
| --- | --- |
| `pl.Array(pl.Int64, 3)` | `ursus.Array(ursus.Int64, 3)` |
| `.list.to_array(3)`, a cast to `pl.Array` | `.List().ToArray(3)`, a cast; "not all elements have the specified width 3" |
| `.arr.to_list()`, a cast to `pl.List` | `.Arr().ToList()`, a cast |
| an Array cast to another inner type | the same; to another size refused, as Polars refuses it |
| a Go `[3]float64` field | `FromRows` makes an `Array(Float64, 3)`, `Rows` reads one back, or into a slice; a `*[n]T` is nullable |
| `df.explode("a")`, filter, sort, join, `pl.concat` | the same |
| `cs.array()` | `selector.Array()` |
| `write_ipc`, `read_ipc` | an Arrow `FixedSizeList` both ways |
| `write_parquet`, `read_parquet` | a Parquet LIST, its size in the footer's `ARROW:schema` |
| `write_ndjson`, `read_ndjson(schema=...)` | a JSON list; read by a schema, each row's width checked |
| `group_by` an Array, sort by one | refused, as a List is (§3's next rows) |

**Casts.** `CanCast` takes a List to an Array and back, of castable elements, and an
Array to another of its size. `kernel.Cast` gathers and casts the elements as for a
List, after `checkWidths` refuses a valid row of another width. `List().ToArray` is a
call, `list.to_array`, whose type is the list's element at the constant width:
checked and relabelled, no copy. `Arr().ToList()` is the first of the `arr` family, a
block of its own at 1400 (`IsArr`), and a relabel.

**Arrow.** `arrowin` types a `FIXED_SIZE_LIST` as an Array, read by the list builder,
which already gave a null row no element. `arrowout` writes a `FixedSizeList`: its
child is shared where every row holds the size, and otherwise built from slices of it
and a null array of the size for each null row (`arrayChild`, Arrow's own
concatenate: the kernel is above this package).

**Parquet.** Polars writes an Array as a LIST and keeps its size in the footer's
`ARROW:schema`, the Arrow schema as an IPC stream of no batch, in base64. ursus does
the same when a column is an Array, and only then, so every other file's footer is as
it was; and reads it, a top-level LIST that schema calls a FixedSizeList an Array,
each row's width checked as the level machine closes it (`listCol.closeRow`). The key
is read and written with the IPC package alone: arrow-go's helpers are in its flight
package, which the reader does not take on.

**NDJSON** reads an Array in its natural type, a List, and the closing cast checks
each width; the JSON writers write an Array as a list.

## 2. Tests

**`TestArrayType`,** Polars 1.44.1's answers: a List of three with a null element and
a null list cast to an Array; back by a cast and by `ToList`; `ToArray(3)`; a cast of
the elements; an Array of none; exploded by the frame and by an expression; filtered,
sorted, stacked, joined, and selected by `selector.Array`; refused: a List of another
width, by `ToArray` and by a cast; an Array to another size and to text; `ToList` of
a List; an Array as a key and as a sort key; and a sort carrying an Array through its
spill under 16 KiB.

**`TestArrayRows`:** a `[3]int64` and a `*[2]float64` field, `FromRows`, `Rows` back,
as slices, `ToMaps`, a nil pointer's row exploded to one null, and a Go array of
another size refused.

**`TestArrayIO`:** Polars' IPC file and Parquet file of one frame (`testdata/ipc/
polars_array.arrow`, `testdata/parquet/polars_array.parquet`), read as Arrays to the
value, `-0.0` kept; a footer that calls a list of two an Array of three refused
(`pyarrow_bad_array.parquet`, which PyArrow refuses and Polars panics on); ursus's IPC
and Parquet round trips; the footer's key present only with an Array; NDJSON written
and read back by a schema, a row of another width refused; CSV refused.

Polars reads ursus's IPC and Parquet files back as Arrays, checked by hand. PyArrow 25
refuses a Parquet null Array row of either writer's, Polars' own file too ("Expected
all lists to be of size=3"); Polars reads both.

**Updated:** `TestEveryArrowTypeIsMappedOrRefused` (a FixedSizeList is an Array),
`TestEveryTypeIDIsMappedOrNamed` (an Array exports),
`TestSpillRoundTripsNestedTypes` (an Array round-trips, sliced too, where it was
refused), the call families' count, 216, with the `arr` family; the evaluator
contract and the sliced-list sweep pass `list.to_array` over, as `item`: it refuses a
list of another length, which the fixtures' are.

**Teeth (16 of 17 bite):**

- a List not castable to an Array;
- a List's widths not checked;
- an Array not taken;
- a taken Array a List;
- a stacked Array a List;
- Arrow's fixed-size list a List;
- an Array's null rows not padded;
- no Arrow schema in a Parquet footer;
- a footer's Array not read;
- a Parquet Array's widths not checked;
- a spilled Array's size dropped;
- a Go array of any size decoded;
- an Array not written as JSON;
- an NDJSON Array read as itself;
- an Array not exploded;
- an Array not rendered.

**Silent:** a null Go array holding its size of zeros. Every reader skips a null
row's range: explode gives it one null, a list kernel a null, Parquet writes its
level, and Arrow's export finds every row whole. A null row of none is kept as the
List's rule, which `arrayChild` relies on to pad. Two first drafts failed to build
and were redrawn.

## 3. Performance

A million rows of three integers, in memory, the median of seven, against Polars:

| | ursus | Polars |
| --- | --- | --- |
| the list itself, selected | 11.6 ms | — |
| `List().ToArray(3)` | 12.1 | 0.64 |
| `Arr().ToList()` | 11.5 | 0.67 |
| a cast to `Array(Float64, 3)` | 20.2 | 3.76 |

`ToArray` and `ToList` cost 0.4 ms over the list's own pass through the query, a
width check and a relabel; what is left is the query's, a frame cut into batches and
its lists concatenated again (§8).

**Gate:** test-all 135 ok, race 27 ok (the root package 576 s here), levels, vet ×3
and the bench engine tests clean; the 22 PDS-H answers match DuckDB.
