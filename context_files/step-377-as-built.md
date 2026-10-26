# Step 377 — as built

**The columns beside a text predicate read at its kept rows.** R2 of `v0.7-scope.md`,
narrower than the scope wrote it: §3 says why.

## 1. How

Step 193 judges a String conjunct pushed to a Parquet scan on its column's
dictionary, once a value, marks each row by its index, and copies only the kept
strings. The batch's other columns were then read whole, every row decoded into a
column allocated for all of them, and taken to the kept rows (`compact`).

- **`keptReader`** (`column.go`): a column reader that can append only the rows a
  batch keeps, given as flags and as their positions. `readBatch` (`parquet.go`) asks
  it of every column beside a marked one, where the marking dropped a row; a reader
  that cannot, or a batch where nothing was dropped, reads whole as before, and
  `compact` takes only those.
- **A fixed-width column from its own pages** (`fixedCol.readKept`, `pageFixed.keptSegment`):
  where every row of a page is present, a PLAIN value is loaded from the page's bytes
  at a kept position and a dictionary index is looked up only for a kept row, so
  nothing is copied for the rest; a page with nulls has its present values packed
  into scratch and placed a row at a time, as `readSpaced` places them. Scratch is
  made only for such a page.
- **A text column from its own pages** (`byteArrayCol.readKept`): marked as a
  predicate's column is, by index, copying no string, with no table of its own, and
  emitted at the kept rows.
- **Struct fields, Booleans, decimals in fixed-length bytes, lists** and every column
  read through arrow-go read whole and are taken, as before.
- The kept rows' positions are computed once a batch, sized to them, and `compact`
  reuses them; `mark` sizes its pending indices once a batch, where they grew by
  doubling each row group.

## 2. Measured

**PDS-H at SF=1,** on four threads, step 376's runner and this step's alternating,
three rounds of five at a load of 2.7 to 5.6:

| | step 376 | here | |
| --- | --- | --- | --- |
| q10, `l_returnflag = 'R'` | 281.7 ms | 230.1 | 0.82 |
| q19, `l_shipmode`, `l_shipinstruct` | 253.4 | 212.3 | 0.84 |
| q9, `p_name` | 436.4 | 416.7 | 0.95 |
| q12, `l_shipmode` | 176.7 | 169.6 | 0.96 |
| q16, `p_brand`, `p_type` | 54.0 | 52.5 | 0.97 |
| q3, `c_mktsegment` | 209.8 | 209.7 | 1.00 |
| q14 and q6, no text predicate | 139.6, 102.4 | 141.5, 102.7 | 1.01, 1.00 |
| in all | 1,654 | 1,535 | 0.93 |

q12 read 0.84, 0.89 and 0.90 in the step's earlier runs, q19 0.87 and 0.99.

**PDS-H at SF=0.1,** all 22, the same alternation, three rounds of five at a load of
4.6 to 4.9: **0.98 in all** (549.6 ms to 537.0), q12 0.83, q19 0.89, q10 0.90. q22,
q11, q1, q3 and q16 read 1.07 to 1.12 and q6 0.87, the noise at that load: q22, q11
and q1 hold no text predicate that drops rows in the reader, so their path did not
change, and q3 and q16 read 1.00 and 0.97 at SF=1.

## 3. What was tried and left

The scope's R2 judged **every** single-column conjunct in the reader: a date's range,
a discount's, a quantity's, decoded and evaluated before the other columns, which
were then read at the kept rows. It was built, tested to the same answers, and
measured slower:

- PDS-H at SF=1 against step 376: q19 1.20, q6 1.13, q3 1.12, q14 1.05, in all 1.03;
- the bare scan of q14's four lineitem columns filtered by its month, on one thread:
  264–284 ms with step 376, 276–411 with it.

The Filter above already judged such a conjunct cheaply, a comparison over a decoded
column, and took the 1.2% of rows it kept for nearly nothing; DuckDB's lineitem holds
a page a column chunk, so no page's decompression, the cost that dominates, could be
skipped either. Judging in the reader added an evaluation, and turned on the read's
widening, batches of up to eight times the rows, for queries that had none. With
the numeric conjuncts left to the Filter, q19 read 0.87 and q12 0.90: the gain is in
not copying text, and the step keeps that.

## 4. Tests

- **`TestAParquetFilterReadsTheRestAtItsKeptRows`** (root): 300,000 rows of every
  scalar type Parquet holds, with and without nulls, converted and not, a list, a
  struct, a Boolean and a decimal, in row groups of 200,000, so that the columns of
  many values fall back from their dictionary's megabyte to PLAIN pages, which the
  test checks page by page; text filters that keep some rows, none and all, with
  numeric conjuncts beside them, and two numeric ones alone; on one thread and four,
  in batches of 1000 and the default. Each answer is the filter's over the whole file
  read.
- **`TestColumnsBesideATextConjunctAreReadAtItsKeptRows`:** a conjunct keeping one
  row in 500 of eight row groups, read with two Int64 columns beside its own and
  without: from the third row group the two add 0.71 MB where they are 4.58 MB whole,
  bounded at a quarter; and each kept row's values are its own.
- **`TestDictableIsOneStringColumnRowByRow`** stands as step 193 wrote it.

**Teeth: 7 of 7 bite:** a kept PLAIN value read from the row before; a kept
dictionary value looked up for the row before; a kept row on a page of nulls marked
null; a column read at its kept rows taken again; a page's kept rows counted one past
it; the columns beside read whole; text beside emitted whole.

**Gate:** the light gate, one package at a time on four procs: the tests at v3, 28
packages ok; levels, vet ×3 and the bench engine tests clean; the 22 PDS-H answers
match DuckDB.
