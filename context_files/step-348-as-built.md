# Step 348 — as built

**Parquet files whose columns differ, read as one,** `polars-parity.md` §5's schema
evolution row: Polars' `scan_parquet` `schema`, `missing_columns`, `extra_columns` and
`cast_options`' integer and float upcasts, as `WithParquetSchema`,
`WithInsertMissingColumns`, `WithIgnoreExtraColumns`, `WithUpcastIntegers` and
`WithUpcastFloats`.

## 1. How

**`parquet.Evolution`** holds them. Where none is set, the files are read as before:
every file has the first file's columns, by name, of its types, or is refused
(`sameColumns`). Where any is, `Source.fits` takes its place, at plan time over every
footer (`readSchemas`) and again over the footer read (`layoutFor`): the schema is the
given one or the first file's; a column a file lacks is refused unless
`InsertMissing`; one it has beyond the schema is refused unless `IgnoreExtra`; and one
of another type is refused unless it upcasts: an integer to one that holds every value
of it, signed to wider signed, unsigned to wider unsigned or to strictly wider signed,
never signed to unsigned; a Float32 to a Float64. Each refusal names the file and the
column, and the option that would read it, as Polars' hints do.

**A file's layout** marks a column it lacks (`missing`), read by a `constReader` of one
null row, its nulls now `kernel.NullColumn`'s, an operand's with a payload, where
`data.NewNull` has none to gather; and one it holds narrower (`from`), read as the file
holds it, so the decoders see their own type, and widened by a `castReader` with
`kernel.Cast`. A row group's statistics prune in the file's own type, and a missing
column has none to prune by. A column's nullability is the files' and the missing
columns' together, a given schema's too.

| Polars 1.44.1 | ursus |
| --- | --- |
| a later file with a column `c` | refused: `"c"`, naming the file |
| `missing_columns="insert"` alone | refused still, for `c` |
| `missing_columns="insert", extra_columns="ignore"` | `{a, b}`, `b` null in the second file's row |
| a later file's columns in another order | read by name |
| `Int32` against the first file's `Int64` | refused; `integer_cast="upcast"` reads it |
| `Int64` against the first file's `Int32`, upcast | refused: "cannot safely cast" |
| `schema={a, b, c}, missing_columns="insert"` | each file's lacking columns null |

## 2. Tests

**`TestParquetSchemaEvolution`,** Polars 1.44.1's answers: refused, an extra column,
with missing columns inserted too, an Int32 against an Int64, an Int64 against an
Int32 even upcast, a Float32 against a Float64; missing and extra columns read; columns
in another order; Int32 to Int64 and Float32 to Float64; a UInt16 into an Int32, on
one thread too, and a UInt32 not; a given schema; a filter on a column a file lacks
and on one it widens; and a hive directory whose files' columns differ.

**Teeth (9 of 10 bite):** a missing column refused with `InsertMissing`; an extra
column refused with `IgnoreExtra`; no upcast; an unsigned integer into a signed one as
wide; a narrower integer let through; a missing column read from the file; a narrower
column read as the schema's type; a file's layout checked by `sameColumns`; a null
repeated by gather. Silent: a given schema's columns kept as declared rather than made
nullable, which changed nothing, every column's nullability being the files' and the
missing columns' already; the forcing was removed.

## 3. Performance

A file of a million rows of an Int64, a Float64 and a String, and one of a million of
an Int32 and a Float64, the second's `a` widened and its `s` null; the median of seven,
the machine shared (a load of 5):

| | ursus | Polars |
| --- | --- | --- |
| both, upcast and missing | 53.1 ms | 17.2 |
| the first alone | 24.3 | 9.2 |

The evolved file costs about what the plain one does; the gap is the Parquet reader's
own (§8, step 347).

**Gate:** test-all 140 ok, race 28 ok (the root package 645 s here), levels, vet ×3 and
the bench engine tests clean; the 22 PDS-H answers match DuckDB.
