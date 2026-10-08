# Step 123 — as built

**The Parquet scan and the filter, made cheaper where the profile said.** This is
`v0.4-scope.md` item 16's remainder and the first two refinements step 101's allocs
profile named for item 2.

## 1. What the profile said

PDS-H q6 at SF=1, one query, five iterations, on an otherwise idle machine. It ran in
255 ms, against Polars' 37 ms. Of its CPU:

| what | share |
| --- | --- |
| ZSTD decompression (`lineitem` is ZSTD, 51 row groups) | 14% |
| the reader's value loop and its copy | about 15% |
| validity bitmaps built a bit at a time | about 15% |
| `Take` and `SelectionFromMask` reading a bit at a time | about 10% |
| arrow-go's dictionary and level decoding | about 9% |

`lineitem` declares every column optional and holds no null in any of them. Most are
dictionary-encoded doubles and dates: physical types stored exactly as ursus stores
them.

## 2. What changed

**The reader:**

- **A direct read.** When the physical and the stored type are one type (Int32,
  Int64, Float32, Float64, Date, Datetime, Duration, Time), `fixedCol` reads straight
  into the column's buffer. No scratch buffer, no copy, no call through `conv` per
  value. A chunk with nulls is spread into place from the back, where no value is
  overwritten before it moves. `newFixed` decides this by type, and every same-type
  reader's `conv` is the identity, checked by listing them.
- **`lazyValid`.** A column's validity is built only once a null arrives. A batch
  with none finishes with the no-storage all-set view, which downstream kernels take
  as their fast path.
- **The byte-array reader** uses `lazyValid`, and grows its character and offset
  buffers once per read, counted first, instead of by append's doubling.

**The filter:**

- **`bitmap.AppendSetPositions`** turns a mask into a selection a word at a time, and
  `SelectionFromMask` allocates the selection once, at its counted size.
- **`View.word`** loads eight bytes at once where the buffer allows, instead of
  assembling every word a byte at a time. Every `Words` caller gains.
- **`Take`** builds no validity when the source has no nulls and the selection no
  `NullIndex`: every filter of a column without nulls appended a bit per row there,
  and threw them away.
- **`data.TakeStrings`** gathers String and Binary by offsets and bytes, as
  `ConcatStrings` has done since step 103. A Go string per row was an allocation per
  row, which `NewString` then copied again.

## 3. Measured

The runner built before this step against the one built after, alternated, three
rounds each, median of five iterations per round, with nothing else running:

| query | before | after | |
| --- | --: | --: | --- |
| PDS-H q6, SF=1 | 258–319 ms | 124–163 ms | about −45% |
| PDS-H q1, SF=1 | 825–846 ms | 529–544 ms | −36% |
| PDS-H q3, SF=1 | 454–463 ms | 252–257 ms | −44% |
| PDS-H q14, SF=1 | 347–350 ms | 201–207 ms | −41% |
| h2o gb4, 10M | 541–545 ms | 330–340 ms | −38% |
| h2o gb1, 10M | 373–477 ms | 266–346 ms | about −28% |
| h2o j1, 10M | 1,859–1,878 ms | 1,575–1,608 ms | −15% |

Peak memory is unchanged.

## 4. Step 117's report understated ursus

**The "before" column above is faster than step 117's report.** That report had q6 at
410 ms, q1 at 898, q14 at 385; through the same driver and the same 8 GB scope, the
same commit runs them at 272, 663 and 292 now. Polars re-timed now is about where the
report had it: q6 37 ms against 50, q1 233 against 237.

**What differed was the machine.** While ursus's PDS-H and h2o-Parquet runs went,
this session's four read-only reviewers were grepping the repository and arrow-go's
sources in the module cache. That contended with the eight threads ursus ran on.
Polars and the other engines ran after them, mostly when the reviewers had finished.

The report therefore understates ursus on those two suites. It is not corrected in
place: a report is one session, and stitching runs together is what the README says
it does not do. **The full report is re-run at the end of this round of performance
work, with the machine otherwise idle.**

## 5. Tests and teeth

- **`internal/bitmap/setpositions_test.go`:**
  - `TestAppendSetPositions` against a bit-by-bit loop, over sliced views at every
    offset, with and without a second view;
  - `TestWordsAcrossEveryOffset`, including the last words of a buffer, where the
    eight-byte load cannot reach.
- **`internal/data/takestrings_test.go`:** `TestTakeStrings`, from sliced columns, with
  nulls, empty strings, and negative indices.
- **`readerroundtrip_test.go`:** `TestParquetReaderRoundTripsNulls` writes eight
  column types with six null patterns, and reads them back in batches of 97 on one
  thread and four.

  Its first version passed with a tooth in: its random nulls never left a batch's
  first 64 rows all valid with a null after them, where validity starts being built
  part-way through. Its runs of nulls were also overwritten by the loop that followed.
  Two deterministic patterns now put nulls exactly there, and the runs are laid in a
  second pass.

| tooth | result |
| --- | --- |
| nulls spread from the front | **bites** |
| the rows before a batch's first null lost | **bites** (after the patterns were fixed) |
| validity always all set | **bites** |
| the byte-array reader's nulls read as valid | **bites** |
| the selection ignores the second view | **bites** |
| the word load drops the ninth byte | **bites** |
| strings gathered relative to the slice | **bites** |
| `Take` drops validity despite a `NullIndex` | **bites:** joins and as-of joins across the suite |

**Gate:** test-all 110 ok, race 22 ok, levels, vet ×3 and the bench engine tests clean; PDS-H SF=0.1 22/22 against DuckDB.
