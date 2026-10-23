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
| `name.keep`, `name.to_lowercase`, `name.to_uppercase`, `name.prefix_fields`, `name.suffix_fields`, `name.map_fields` | `Prefix`, `Suffix`, `MapName` |
| `top_k_by`, `bottom_k_by` | missing |
| `interpolate_by` | missing |
| `rolling` of an expression, `rolling_map`, `rolling_rank_by` | the frame's `Rolling`; the `_by` windows |
| `item`, `lower_bound`, `upper_bound`, `is_sorted`, `limit`, `truediv` | missing (`Head` for `limit`, `Div` for `truediv`) |
| `reinterpret`, `shrink_dtype`, `to_physical` | missing |
| `pipe`, `inspect` | `LazyFrame.Pipe` only |
| `str.to_time`, `str.to_decimal` | `Strptime(Time…)`; `CastLossy(Decimal)` |
| `dt.cast_time_unit` | `Cast(Datetime(unit))` |
| `struct` field drop | missing |
| `pl.arctan2` | missing |
| `meta.*`: `root_names`, `output_name`, `is_column`, `has_multiple_outputs`, `undo_aliases`, `tree_format` | `String()` |
| a window inside a window; an aggregate inside an aggregate's input | refused (§5 of `feature-gaps.md`) |
| `Mode` and Spearman `Corr` spilling in a group-by | refused past a limit |

## 3. Types

| Polars | ursus today |
| --- | --- |
| `Array`, fixed-size lists, and the whole `.arr` namespace (32 methods), `list.to_array`, `reshape`, `concat_arr` | the type's constructor only |
| `Categorical`, and `cut`/`qcut` returning one | Enum, and text from `cut` |
| a List or a Struct as a key: group-by, unique, join, over | refused |
| large strings, over 2 GiB a column | refused |

## 4. Frames

| Polars | ursus today |
| --- | --- |
| a frame's `corr` | missing |
| `gather`, `row`, `item`, `get_column_index`, `replace_column`, `drop_in_place` | `Column[T]`, `At[T]`, `Schema().IndexOf`, `WithColumns` |
| a frame's `sum_horizontal`, `mean_horizontal`, `min_horizontal`, `max_horizontal`, `fold` | the expressions |
| `map_rows`, `map_columns`, `to_series`, `to_struct` | missing |
| `cache`, `serialize`, `show`, lazy `columns`/`dtypes`/`width` | `CollectSchema` |

## 5. I/O

| Polars | ursus today |
| --- | --- |
| JSON and NDJSON: read, scan, write, sink | missing |
| Arrow IPC: read, scan, write, sink; `read_ipc_schema`, `read_parquet_schema` | `ScanArrow` over arrow-go, `Record()` |
| hive partitions; multi-file schema evolution; compressed CSV | missing |
| Excel, Avro, databases, Delta, Iceberg | missing (P2/P3) |

## 6. Left out on purpose

- **Python-only:** `to_pandas`, `to_numpy`, `to_torch`, `to_jax`, `plot`, `style`,
  `write_clipboard`, `to_init_repr`, `from_pandas`, extension types.
- **Memory layout:** `rechunk`, `n_chunks`, `shrink_to_fit`, `set_sorted`. ursus
  checks sortedness rather than trusting a hint (§9 of `feature-gaps.md`).
- **SQL:** `sql`, `sql_expr`, `SQLContext`, by an earlier decision.
- **Polars Cloud:** `remote`.
- **Python's frame `count()`:** ursus's `Count` stays the row count (0.6.7's §7.1).
