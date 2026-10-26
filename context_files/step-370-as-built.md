# Step 370 — as built

**A count reads no column:** `Select(Len())` and `Count` over a scan read none of its
columns, where they read every one. P2 of `v0.6.8-scope.md`.

## 1. How

**The plan** (`rule_projection.go`): a scan nothing above requires is a scan of no
columns, an empty projection, where it was nil, which means every column. `Len()` is
`lit(1).len()` and names no column, so a count's scan reads none. The union's and the
hstack's first-column workaround, which asked each child for a column because a scan
asked for none read all, is gone: a union's children under a count read none, a
filtered child its predicate's column under a `PROJECT []`.

**The sources** (`source.RowsOnly`): a source says whether it answers a scan's spec
of no columns with batches of its rows and no columns:

- **Parquet**, with no predicate, from its files' footers, a footer read as its rows
  are reached and no column chunk opened (`rowsReader`); with a predicate it is asked
  for a column, and a scan of none with one reaching its readers is an internal error,
  where its parallel reader looped;
- **memory**, **CSV**, **NDJSON** and **Arrow**, IPC's reader, from what they read
  anyway: a memory batch's rows, a CSV's records, an NDJSON's lines, a record's
  length. A CSV counts its records through quoted newlines, blank lines, comments and
  skipped rows as a read does;
- any other source, as a glob's of CSV files, is asked for its cheapest column, a
  fixed-width one of the least width before text (`noColumns`), which the scan's trim
  drops keeping the rows.

**Rows kept where columns are none:** `trimOp`, the memory reader, `FilterBatch` and
the reverse build their batches with their row count (`NewBatchRows`), not from their
columns, which a batch of none has not.

**`Count`** plans a projection of no columns above its query, so its scans read none.

## 2. Measured

The PDS-H lineitem file at SF=1, six million rows of 16 columns, 154 MB, on four
threads:

| | before | after | Polars 1.44.1 |
| --- | --- | --- | --- |
| `Count` | 400–500 ms | 2.5–2.9 ms | — |
| `Select(Len())` | 400–500 ms | 27–39 ms | 0.4 ms |

`Profile` says where `Select(Len())`'s time went: the scan 4.3 ms, its 733 batches of
no columns, and the aggregation 22.9: a group id and a kept row a row, and `lit(1)`
evaluated to a whole column, for a count of no keys. That is P4's keyless fast path.

**The plans:** the 37 queries the suites run, every PDS-H query's at SF=0.1 and every
h2o query's at 2,000,000 rows, printed by the bench's own query code at HEAD and here:
**byte-identical**. None counts a whole scan. The 34 golden plans held;
`TestConcatUnderLenReadsOneColumn` became `TestConcatUnderLenReadsNoColumn`.

## 3. Tests

- **`TestACountReadsNoColumn`:** a count's scan has an empty projection; over memory,
  Parquet, CSV, NDJSON, IPC, Arrow and a hive dataset, a count, a group-by of no keys,
  a union, a diagonal union, a head, a slice, a tail, a reverse, a shift, every tenth
  row, a cache, an hstack, a cross join, a filter and a row index, and `Count`, on 1
  and 4 threads, in batches of 8,192 and 64, each its rows.
- **`TestAParquetCountReadsNoColumnChunk`:** a file of five row groups whose every
  page is overwritten with garbage, its footer whole: `Count` and `Select(Len())`
  answer 5,000 on 1 and 4 threads, and a read of a column refuses it.
- **`TestACSVCountIsItsRows`:** thirteen CSV shapes, counted as read, in batches of 1,
  2 and the default.
- **`TestAScanOfNoColumnsKeepsItsRows`:** a source that cannot answer a scan of no
  columns is asked for its Bool, before its Int64 and its String, and the scan keeps
  its 15 rows; one that can is asked for none.
- **`TestConcatUnderLenReadsNoColumn`**, rewritten: no column read but the filter's,
  and seven rows.

**Tests that drove a query with `Count`** to make an operator hold its input, under a
memory limit, held nothing once a count read no column: a tail and a reverse had
nothing to spill, and a join of one key too large held its keys alone. They, the
other memory and join-spill tests of the kind, and the pipeline benchmarks, which
would time a pipeline the optimizer had pruned, now drain every column
(`drained`, `memory_test.go`), as `Count` did.

**Teeth: 9 of 9 bite:** a scan asked for nothing reading everything; a trim, the
memory reader or the reverse losing the rows of a batch of no columns; Parquet asked
for a column to count; `Count` reading every column; a union under a count reading a
column; the fallback's column the first rather than the cheapest; a source that answers
a scan of no columns asked for a column.

**Gate:** the light gate, one package at a time on four procs: the tests at v3, 27
packages ok and the root's three budget cases failed, the ones that drove a tail, a
reverse and a join with `Count`; the root's rerun after they drain every column, ok.
Levels, vet ×3 and the bench engine tests clean; the 22 PDS-H answers match DuckDB.
