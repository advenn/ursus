# Step 347 — as built

**Hive partitions on read,** `polars-parity.md` §5's next row, for Parquet: a directory
read whole, as Polars' `scan_parquet` reads one, its `key=value` directories its files'
partition columns; and each file's path as a column, Polars' `include_file_paths`.

## 1. How

**`ScanParquet(dir)`** lists every file below the directory, sorted by path, passing
over a name beginning '.' or '_' (`datasetFiles`), and reads them as `ScanParquetFiles`
does, every file of the same columns. Hive partitioning is on by default for a
directory, its own name and what lies above it not counted, and off for a file, a list
and a glob, which `WithHivePartitioning(true)` reads the whole of each path of, as
Polars does.

**`internal/source/hive`** (level 42) reads the partitions: each path's `key=value`
directories, percent-decoded, `__HIVE_DEFAULT_PARTITION__` and an empty value null,
every path's the same keys in the same order or refused (`Parse`); each key's type a
given one's (`WithHiveSchema`), a file column's of the same name, or the one its values
infer (`Infer`), as Polars infers a key's: `-?\d+` an Int64, or an Int128 past it;
Polars' float, with a point, an exponent, `inf` or `NaN`, a Float64; `true` and `false`
in any case a Boolean; with dates, year-month-day or day-month-year apart by `-`, `/`
or `.` a Date, and with a time after `T` or a space a Datetime in microseconds, in UTC
where it has an offset; anything else, and a key with no value, text. Each key's values
are then cast from their text, strictly (`Values`).

**The Parquet source** keeps the files' own schema (`files`) beside the whole one, the
keys after the files' columns and the path column last; a key that names a file column
keeps its place and type, and its values replace the file's, as Polars reads them. A
file's partition values and path are columns of one row (`consts`), read by a
`constReader` that repeats its row as often as a row group's other columns read, so
both readers, serial and parallel, read them as any column. A conjunct reading only
keys or the path is claimed (`onFixed`), and before a file is opened it is judged on
the file's values by the engine's own evaluator (`ScanSpec.Eval`); a file none of whose
rows it keeps, false or null, is never opened (`keepFile`, `nextKept`), and
`FileStats` counts them.

| Polars 1.44.1 | ursus |
| --- | --- |
| `scan_parquet(dir)` of `a=1/b=x`, `a=2/b=y%20z`, `a=__HIVE_DEFAULT_PARTITION__/b=w` | `{v, a: Int64, b: String}`, `y z`, a null |
| a key's types | as Polars: `007` 7, `2.5e3` and `inf` floats, `TRUE` a Boolean, `2024/1/2` a Date, `2024-01-02T03:04:05.5+0530` an instant in UTC; `+5`, `nan`, `1_000` text |
| `filter(a == 2)` | one file read of three |
| `hive_schema={"a": pl.Int8}` | `WithHiveSchema`: `a: Int8` |
| `try_parse_hive_dates=False` | `WithHiveDates(false)`: text |
| `include_file_paths="path"` | `WithIncludeFilePaths("path")`, last |
| a key the file holds, `a=1` beside a column `a` of 9 | 1, in the file's place |
| `a=x` beside an Int64 column `a` | refused |
| keys apart, depths apart, an empty directory | refused |
| a glob, a file | no keys unless asked; then the whole path's |
| `hive_schema` naming some keys | refused in Polars; the rest inferred here (feature-gaps §9) |
| `_metadata` or a `.crc` file beside the data | refused in Polars; passed over here |
| a hidden directory | read in Polars; passed over here |
| `24-01-02` | a Date of the year 2 in Polars; text here |

## 2. Tests

**`TestHivePartitions`,** Polars 1.44.1's answers: a directory of three files, its keys
typed, percent-decoded and null; the keys alone and a count; `WithHiveSchema`, dates
and `WithHiveDates(false)`; a filter on a key reading one file of three, on one thread
and on eight, a key and a column together, a key's text, `a + 1 == 3`, which the
statistics' pruner reads no part of, and a null key; the path column, last, and one
named as a key refused; a key the files hold, and one of a value their type refuses;
globs, files and lists, with hive partitioning and without; a directory's bookkeeping
and a hidden directory passed over, a directory not `key=value` and the root's own
name no key; keys apart, depths apart and no file refused; forty files on one thread
and on eight, equal to their `Concat`. **`TestInfer`, `TestParse`, `TestValues`:** the
inference value by value, the parse of paths, and the values cast, an instant at an
offset in UTC.

**Teeth (13 of 13 bite):** a value not percent-decoded; Hive's default partition a
value; integers not known; no file ruled out; a null value keeps its file; a conjunct
of keys not claimed (silent at first, the key filters tested being claimed already by
the dictionary filter's rule; `a + 1 == 3` added); a key the files hold appended; a
replaced column's nullability the file's; a directory's bookkeeping read; a file's or a
list's path not read whole; the parallel reader's first file never ruled out; the path
column a wrong path; hive partitioning on by default for a file.

**Also:** a key replacing a file column takes its nullability from the keys' values,
not the file's, which the first draft kept.

## 3. Performance

Two million rows in forty files, `g=0..9/h=k0..7`, written by Polars'
`write_parquet(partition_by=...)`, which keeps `g` and `h` in the files too; the median
of seven, the machine shared (a load of 6):

| | ursus | Polars |
| --- | --- | --- |
| the whole dataset | 42–59 ms | 19–28 |
| the same files, no hive | 61 | 23 |
| `sum(x)` | 15–24 | 9–14 |
| `filter(g == 3)` | 6–12 | 4–5 |
| `group_by(h).agg(sum(x))` | 28–44 | 48–53 |

The keys cost nothing over the files' own columns: the dataset reads no slower with
them, its `g` and `h` given rather than decoded. The gap is the Parquet reader's own
(§8). Reading every file's footer when the query is planned, as ursus has since its
audit's row I1, takes 1.4 ms of the forty.

**Gate:** test-all 140 ok, race 28 ok (the root package 886 s here, the machine
suspended in the middle of it), levels, vet ×3 and the bench engine tests clean; the 22
PDS-H answers match DuckDB.
