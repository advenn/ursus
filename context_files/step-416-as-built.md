# Step 416 — as built

**A Parquet footer whose row counts disagree is refused as corrupt.** H2 of
`v0.7.2-scope.md`, confirmed by its prober, the plan's 423.

## 1. What was wrong

A read takes a row group's `num_rows` for its rows, and a count of a file sums them
without reading a column. H2's byte-flip sweep found a footer whose row group said
257 rows where its columns held 300: ursus read 257 rows and no error, and counted
257, where DuckDB 1.5.5 and pyarrow 25 read 300. A count past the columns' was
refused by a read, which ran out of values, but `Count` answered it, rows that are
not there.

## 2. The change

`checkRowCounts`, at each file's footer, in `openFile`, so a read, a schema and a
count each meet it: a row group's count against each column chunk's values, equal
where the column is not repeated and at most as many where it is, and the file's
count against its row groups' sum. A disagreement is an `ErrIO`, "corrupt: row group
G says R rows, and its column C holds V values".

**Left:** a count short of the rows in a file whose columns are all repeated, Lists
alone: a repeated column holds at least a value a row, so its values cannot say how
many rows it has without its repetition levels decoded.

## 3. Tests

- **`TestARowGroupsCountIsCheckedAgainstItsColumns`** (root), over a file of 300 rows
  ursus writes, its footer patched by a Thrift compact walker in the test: a row
  group's 257 and 301, the file's 257, both made 257 and both 301, and a file of
  Lists whose counts both say 601 against 600 values: each refused as corrupt by
  `Collect`, `Count` and a head. The file as written reads 300 rows. Before the change
  257 read and counted 257, and 301 counted 301.

**Teeth: 4 of 4 bite:** the check gone; a repeated column's at-most unchecked, which
the Lists' 601 reads; a flat column's equality unchecked, which both 257 reads; the
file's total unchecked, which the file's 257 reads. The last two bit once the cases
made both counts agree, so that one check does not stand in for another.

**Gate:** the Parquet package's tests; the root package's Parquet, dataset, pruning,
count, partition and sink tests; vet. CI's every width, the scalar build and the race
detector on the pull request.
