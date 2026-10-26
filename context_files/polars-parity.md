# Polars parity: what ursus still lacks

**Taken on 2026-10-22, after step 312, by a diff of the two APIs.** Polars 1.44.1's
public names were listed from the library itself, per namespace, its deprecated ones
left out. ursus's exported functions and methods were listed from the source. Each
Polars name with no ursus name alike was then checked by hand: many are renamed
(`str.to_lowercase` is `Str().ToLower()`), Python-only (`to_pandas`, `plot`, `style`),
or an alias of something ursus has.

What remains is below, in the order it is built. `feature-gaps.md` stays the record of
why each gap exists and of where ursus differs on purpose (§9). This file is the list
the maintainer asked for on 2026-10-22: "match ursus features to polars … implement
what we are lacking".

**How each is built** is how 0.6.7's items were:

- answers equal to Polars 1.44.1's on the same inputs, or a difference recorded in §9;
- teeth that bite;
- the gate, and the 22 PDS-H answers;
- no slower plans for what exists: the golden plans and the benchmark queries' plans
  unchanged;
- each new operation streams or spills where its pieces do, and is measured against
  Polars by a micro benchmark where speed is in question.

## 1. 0.6.7's scope, left

| item | what |
| --- | --- |
| ~~Rolling's offset~~ | built in step 313: `RollingOptions.Offset` placed as Polars' `offset`, and `NoOffset()`. A break |
| ~~J1~~ | built: `maintain_order`, every order, in step 314; an as-of tolerance on a numeric key, as-of `coalesce` and `JoinWhere`'s suffix in 315 |
| ~~J2~~ | built in step 316: `Update` |
| ~~V1~~ | built in step 317: `FromRows` |
| ~~V2~~ | built in step 318: `RowsByKey`, `ToMaps`, `IterRows` |
| ~~V3~~ | built in step 319: a `Series` with operations |
| ~~tier 2~~ | built in steps 320 and 321 |

## 2. Expressions

| Polars | ursus today |
| --- | --- |
| ~~`polars.selectors`~~ | built in step 322: package `selector` |
| ~~the `name` namespace~~ | built in step 323: `Expr.Name()` |
| ~~`top_k_by`, `bottom_k_by`~~ | built in step 330: `TopKBy`, `BottomKBy`, a List as `TopK`'s; ties by the value |
| ~~`interpolate_by`~~ | built in step 329: `InterpolateBy`, over one huge partition too, in a sorted pass by its by |
| ~~`rolling` of an expression, `rolling_map`, `rolling_rank_by`~~ | built in steps 337 and 338: `Expr.Rolling`, `RollingMap`, `RollingRankBy`; `rolling_map`'s weights not |
| ~~`item`, `lower_bound`, `upper_bound`, `is_sorted`, `limit`, `truediv`~~ | built in step 324 |
| `reinterpret`, `to_physical`; `shrink_dtype` | built in step 324; `shrink_dtype` not, a no-op on an expression in Polars 1.44, its Series' only |
| `pipe`; `inspect` | `Expr.Pipe` built in step 324; `inspect`, a print, not |
| ~~`str.to_time`, `str.to_decimal`~~ | built in step 325: `Str().ToTime`, `Str().ToDecimal` |
| ~~`dt.cast_time_unit`~~ | built in step 325: `Dt().CastTimeUnit` |
| ~~`struct.drop`~~ | built in step 323: `Struct().Drop` |
| ~~`pl.arctan2`~~ | built in step 324: `Arctan2` |
| ~~`meta.*`~~ | built in step 326: `Expr.Meta()`; `serialize`, `as_selector` and `show_graph` not |
| ~~a window inside a window; an aggregate inside an aggregate's input~~ | built in step 336: each computed beneath in the outer's scope; an order-dependent aggregate under an order refused |
| ~~a non-aggregate in an aggregation; a window in one~~ | built in step 331: a list a group, a window a group at a time |
| ~~`Mode` and Spearman `Corr` spilling in a group-by~~ | built in steps 339 and 340: Spearman's pairs spill, ranked as they are read back; `Mode` an aggregate of its own, its counts spilling |

## 3. Types

| Polars | ursus today |
| --- | --- |
| ~~`Array`, fixed-size lists, and the whole `.arr` namespace (32 methods), `list.to_array`, `reshape`, `concat_arr`~~ | the type built in step 341: casts, `List().ToArray`, `Arr().ToList`, Go arrays, IPC, Parquet and JSON; the `.arr` namespace in step 342, `as_array` of `head`, `tail` and `slice` left to `List().ToArray`; `reshape` and `concat_arr` in step 343 |
| ~~`Categorical`~~ | built in step 351, stored as its text; in and out of Arrow, IPC and Parquet as Polars writes one, and `qcut` and `hist` answering one, in step 352; `cut` answers an Enum, as Polars' does, since step 350 |
| ~~a List or a Struct as a key: group-by, unique, join, over~~ | built in step 344: a List, an Array or a Struct, element by element and field by field |
| ~~large strings, over 2 GiB a column~~ | built in step 368: 64-bit offsets past 2 GiB, a LargeString out and in; a batch a source reads keeps 2 GiB a column |

## 4. Frames

| Polars | ursus today |
| --- | --- |
| ~~a frame's `corr`~~ | built in step 327: `DataFrame.Corr`, NumPy's, as Polars' |
| ~~`gather`, `row`, `item`, `get_column_index`, `replace_column`, `drop_in_place`~~ | built in step 327 |
| ~~a frame's `sum_horizontal`, `mean_horizontal`, `min_horizontal`, `max_horizontal`, `fold`~~ | built in step 327 |
| ~~`map_rows`, `map_columns`, `to_series`, `to_struct`~~ | built in steps 327 and 328 |
| `serialize` | missing; `cache` and `show` built in step 328, lazy `columns`, `dtypes` and `width` in 327 |

## 5. I/O

| Polars | ursus today |
| --- | --- |
| ~~JSON: `read_json`, a file of one array of objects~~ | built in step 345: `ScanJSON`, `ScanJSONBytes`, and `WithNDJSONSchemaOverrides`, Polars' `schema_overrides`; NDJSON read and scanned in step 334: `ScanNDJSON`, `ScanNDJSONFiles`, `ScanNDJSONGlob`, `ScanNDJSONFrom`, `ScanNDJSONBytes`; JSON and NDJSON written and sunk in step 333: `WriteNDJSON`, `WriteJSON`, `SinkNDJSON` |
| ~~Arrow IPC: read, scan, write, sink; `read_ipc_schema`, `read_parquet_schema`~~ | built in step 332: `ScanIPC`, `ScanIPCStream`, `WriteIPC`, `WriteIPCStream`, `SinkIPC`, `ReadIPCSchema`, `ReadParquetSchema` |
| ~~hive partitions~~ | built in step 347 for Parquet: `ScanParquet(dir)`, `WithHivePartitioning`, `WithHiveSchema`, `WithHiveDates`, `WithIncludeFilePaths`; `**` globs and CSV and NDJSON directories in step 349; `include_file_paths` for CSV and NDJSON in step 359, `WithCSVIncludeFilePaths`, `WithNDJSONIncludeFilePaths`; an IPC directory's, globs and lists in step 360, `ScanIPC(dir)`, `ScanIPCFiles`, `ScanIPCGlob`, `WithIPCHivePartitioning`, `WithIPCHiveSchema`, `WithIPCHiveDates`, `WithIPCIncludeFilePaths`; written in step 367, `WithPartitionBy`, `WithPartitionIncludeKey`, `WithMaxRowsPerFile` for the four sinks |
| ~~multi-file schema evolution~~ | built for Parquet in step 348: `WithParquetSchema`, `WithInsertMissingColumns`, `WithIgnoreExtraColumns`, `WithUpcastIntegers`, `WithUpcastFloats`; `cast_options`' datetime casts and struct fields left |
| ~~compressed CSV~~ | built in step 346: a CSV, NDJSON or JSON file of gzip, zlib or Zstandard, by its first bytes, streamed |
| ~~`read_csv(try_parse_dates=True)`~~ | built in step 355: `WithTryParseDates`, Polars' patterns, and a schema's temporal columns read by them; `str.to_date()`, `to_datetime()` and `to_time()` of no format by them in step 356, `ToDate`, `ToDatetime`, `ToTime`, `Strptime(dt, "", strict)` |
| ~~`read_csv`'s options~~ | built in step 357: `WithIgnoreErrors`, `WithInferSchema`, `WithRaiseIfEmpty`, `WithDecimalComma`, `WithEncoding(UTF8Lossy)`; text checked as UTF-8, a short record padded and a blank line a record of nulls, as Polars reads them; `WithSkipLines`, `WithSkipRowsAfterHeader`, `WithQuoting(false)` in step 358; `eol_char` left |
| ~~`write_csv`'s options~~ | built in step 354: `WithQuoteStyle`, `WithWriteQuote`, `WithDateFormat`, `WithDatetimeFormat`, `WithTimeFormat`, `WithFloatPrecision`, `WithFloatScientific`, `WithWriteDecimalComma`, `WithIncludeBOM`, `WithCSVCompression`, `WithCSVCompressionLevel`, `WithCSVCheckExtension`; an instant's and a float's text Polars' |
| ~~databases: `read_database`, `write_database`~~ | built in step 353: `ReadDatabase`, `ReadDatabaseRows`, `WithDatabaseSchemaOverrides`, through `database/sql`; written in step 366: `WriteDatabase`, `WithIfTableExists`, `WithDialect`; `iter_batches` left |
| Excel, Avro, Delta, Iceberg | missing (P2/P3) |

## 6. Left out on purpose

- **Python-only:** `to_pandas`, `to_numpy`, `to_torch`, `to_jax`, `plot`, `style`,
  `write_clipboard`, `to_init_repr`, `from_pandas`, extension types.
- **Memory layout:** `rechunk`, `n_chunks`, `shrink_to_fit`, `set_sorted`. ursus
  checks sortedness rather than trusting a hint (§9 of `feature-gaps.md`).
- **SQL:** `sql`, `sql_expr`, `SQLContext`, by an earlier decision.
- **Polars Cloud:** `remote`.
- **Python's frame `count()`:** ursus's `Count` stays the row count (0.6.7's §7.1).
