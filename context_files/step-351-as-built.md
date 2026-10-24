# Step 351 — as built

**The `Categorical` type,** Polars' Categorical, from `polars-parity.md` §3: the type
`dtype.TypeCategorical` had reserved since step 1, built over a String's storage.

## 1. How

**`dtype.Categorical`** is a type of no parameters, as Polars' is. It is stored as its
text: `Physical()` is String, and `HasStringStorage` and `IsString` take it, so every
kernel that reads a String's offsets and characters reads it: comparison, sort, Take,
Concat, the group-by's raw keys, Min, Max, Mode, the spill format. It is ordered, as
its text, as Polars' has been since 1.32, and so hashable, a key. With a String or an
Enum it meets at String, as Polars' does in a fill with a String column and a relaxed
concatenation; a weak string literal fits it, so `FillNullWith("z")` keeps it, as
Polars' does.

**Casts** (`castCategorical`, `dtype.CanCast`): to a String and from one a relabel; to an
Enum, its text encoded; from one, the Enum's text relabelled. A number is refused both
ways, with the codes and the text as the hint: Polars reads a number as a category's
code, the order of its process's strings, and deprecates it.

**Codes** are what a Categorical has that a String has not, and they are the frame's:
the order each value first appears in among the non-null values over every batch, as
Polars numbers them in a fresh process. `WinCatCodes` is a window function that numbers
them by the group-by's keys, a null null, and the root writes each `ToPhysical()` and
`Cat().Physical()` of a Categorical out as it over the frame when the expression meets
its frame's schema (`categoryCodes`, beside step 273's `writeCategories`). A group-by's
aggregation and a window over partitions refuse them, as they would number a group's;
the kernel refuses a Categorical's `to_physical` that reaches a batch unrewritten.

**The rest:** `Cat()`'s text functions take it, and `Str()`'s read it as its text, as an
Enum's, the kernels relabelling it a String; `GetCategories` is refused with Polars'
deprecation's `Unique()`. The row, JSON, CSV-writing and dummies outputs, the group
keys, the spill format, the CSV reader (by a schema) and the Arrow and Parquet writers,
as its text for now, were taught it; NDJSON reads it by a schema already, its natural
type a String. `selector.Categorical` selects it, `selector.String` not, as Polars'
`cs.string()` by default.

| Polars 1.44.1 | ursus |
| --- | --- |
| `cast(pl.Categorical)`, `.cast(pl.String)`, to and from an Enum | as Polars |
| `sort`, `min`, `max`, `rank`, `== "a"`, `< "b"`, `==` and `<` a String column | as its text |
| `to_physical()`, `cat.physical()` of `["b", "a", "b", None]` | `[0, 1, 0, None]` |
| `fill_null("z")`, `is_in`, `mode`, `unique`, `group_by`, a join of two | as Polars, a Categorical |
| `concat` of two | a Categorical |
| `cat.starts_with`, `ends_with`, `len_bytes`, `len_chars`, `slice` | as Polars; `slice` a String |
| `str.to_uppercase()` of one | refused in Polars; its text's here (feature-gaps §9) |
| a join on a String | refused in Polars; by the text here |
| `cast(pl.Int64)` | Polars' codes, deprecated; refused here |
| `read_csv`, `read_ndjson` with a Categorical schema | a Categorical |

## 2. Tests

**`TestCategorical`,** Polars 1.44.1's answers: the type; sorted; the least; compared
with text and with a String column; its codes, by `ToPhysical` and `Cat().Physical`, a
null's before a new value too; as text, as an Enum and from one; a null filled;
`IsIn`; its rank; its mode; `Cat()`'s functions; grouped, unique and concatenated; a
String's function; a join of two and on a String; refused, to a number and
`GetCategories`. **`TestCategoricalAcrossBatches`:** codes over 5,000 rows in batches
of 64 equal to their first appearances' order; a group-by of 20,000 keys spilling under
32 KiB, its keys Categorical; CSV and NDJSON by a schema; codes in an aggregation and in
a window refused; `selector.Categorical`.

**Teeth (10 of 11 bite):** no string storage; unordered; no meeting with a String; no
cast to an Enum; codes not written out; codes in an aggregation; codes in a window; a
null coded (silent at first, the test's only null last; a null before a new value
added); a fill's text not fitting; a CSV's Categorical a String. Silent: `Physical()`
not String, which nothing routes a Categorical by, every storage question
`HasStringStorage`'s; it states the storage. One more, codes one table across
partitions, went with the window's refusal: no codes are numbered by partition.

## 3. Performance

A million rows of 1000 values, in memory, the median of seven, the machine shared:

| | ursus | Polars |
| --- | --- | --- |
| a cast to Categorical | 3.4 ms | 14.7 |
| `group_by` it, a sum | 7.6 | 2.6 |
| `group_by` the String, a sum | 7.6 | 13.6 |
| its codes | 41.0 | 0.0 |
| `sort` it | 616 | 39.9 |
| `sort` the String | 841 | — |

A cast is a relabel, where Polars builds its codes. Stored as its text, it groups as a
String does, where Polars groups its codes; its codes are computed, where Polars' are
stored; and it sorts as a String does, the String sort's own gap (§8). Dictionary
storage, codes and text, is the 0.7 performance step's.

**Gate:** test-all 140 ok, race 28 ok (the root package 622 s here), levels, vet ×3 and
the bench engine tests clean; the 22 PDS-H answers match DuckDB.
