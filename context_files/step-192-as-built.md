# Step 192 — as built

**P1 of `v0.6.1-scope.md`: the Parquet reader decodes its own pages.**

## 1. Why

Step 191's profiles of v0.6.0 found two costs in arrow-go's typed
`ColumnChunkReader`:

- **A dictionary string was copied twice.** arrow-go's `dictConverter` copied each
  value's slice header out of the dictionary, then `byteArrayCol` copied its bytes.
  That was 25 to 31% of q19, q12, and h2o's j2 and j3.
- **The definition levels were decoded into an `int16` a row,** even in a column
  without a null. That was 3 to 10% of every query.

## 2. What changed

**Which chunks it reads.** A column chunk is read from its pages when all of these
hold:

- it is a top-level leaf, not nested, with a definition level of at most 1;
- its physical type is INT32, INT64, FLOAT, DOUBLE or BYTE_ARRAY;
- its ursus type is one `newColReader` maps that physical type to;
- every encoding its metadata lists is PLAIN, PLAIN_DICTIONARY, RLE_DICTIONARY or RLE.

`ownable` decides before the first page. Every other chunk reads through arrow-go
as before.

**How.** arrow-go's public `RowGroupReader.GetColumnPageReader` yields the pages,
decompressed, and ursus decodes them (`pages.go`):

- **`hybrid`** (`rle.go`) decodes Parquet's RLE and bit-packed hybrid, the encoding
  of levels and of dictionary indices. It hands an RLE run out as one value and a
  length, so a page of present rows costs one run. A bit-packed group of eight
  values of up to eight bits is read with one 64-bit load.
- **`pageChunk`** walks a chunk's pages:
  - it copies the dictionary page, since a page's bytes are reused;
  - it finds V1 levels behind their four-byte length, and V2 levels by their stated
    length;
  - it reads a V2 page that writes no level bytes, and declares no nulls, as all
    present.
- **`pageFixed[P]`** reads a fixed-width chunk straight into the column's values,
  with validity appended directly and no level a row:
  - a PLAIN page is one copy;
  - a dictionary page is a lookup a value;
  - with nulls, the values are spread into place from the back.
- **`pageStrings`** appends into `byteArrayCol`'s offsets and characters:
  - each dictionary value is copied once, the characters counted first so they grow
    once;
  - a value of up to 16 bytes, most of a dictionary's, is copied as two 8-byte
    words. The dictionary is padded with 16 bytes, and the column keeps 16 bytes of
    slack.
- **`fixedCol` and `byteArrayCol`** gain `own` and `pages`, set instead of `cr`.
  `ownFixed` builds a `fixedCol` with the same conversions `newColReader` uses.

**A struct's field is left to arrow-go even at level 1.** A struct's first field
also answers the struct's own validity, through `trackParent`, and only the
level-reading paths record it.

**Found on the way:**

- **The first version grew a PLAIN page's characters by `append`.**
  `TestReadingAllocatesAboutWhatItProduces` failed at 1.86× allocated per byte
  produced. The characters are now counted first, as the old path counted them,
  which reads 1.20×.
- **An RLE run's value could carry more bits than its width.** The value is stored
  in whole bytes, so `"000"` at width 2 decoded to 48. FuzzHybrid found it in two
  seconds. Downstream checks would have refused it as a level over 1 or an index past
  the dictionary; `hybrid` now refuses it itself. The input is kept in
  `testdata/fuzz/FuzzHybrid` as a regression seed.

## 3. Tests and teeth

- **`TestOwnPagesReadAsArrowGoDoes`:**
  - **the columns:** twenty, every type read from its pages: Int8 to Uint64, both
    floats, Date, a zoned Datetime, Time in ms and µs, Decimal on INT32 and INT64,
    String, a required String, a wide String, and Binary;
  - **the layouts:** nine, with V1 and V2 pages, dictionary-encoded and PLAIN, pages
    of a hundred values, ZSTD, and a dictionary limit of 300 bytes;
  - **the nulls:** four patterns: none, scattered, runs of 60, and every row;
  - **the reads:** batches of 1,024 and of 333 rows, read both ways, own pages and
    arrow-go, and compared batch for batch as printed Arrow records;
  - **two checks on the fixtures:** that all twenty columns are read from their
    pages, and that under the 300-byte limit the wide column's chunk holds both
    dictionary and PLAIN data pages. The latter counts its data pages, since the
    dictionary page is PLAIN itself and the chunk's list of encodings cannot say.
- **`TestHybridDecodesWhatItWasGiven`:** widths 0 to 32, repeats and noise, encoded
  by a reference encoder, then decoded at reads of random sizes.
- **`TestHybridRefusesTruncatedData`:** encoded data cut at every byte is an error.
  A bit-packed run cut short gives the values its bytes hold and refuses the rest.
- **`FuzzHybrid`:** any bytes at any width decode or fail, never panic, and never
  give a value wider than the width. It ran 869,000 inputs in a minute after the fix.

| tooth | result |
| --- | --- |
| a run of nulls taken as present | **bites:** the comparison |
| a dictionary value's end off by one | **bites:** the comparison |
| a null in a PLAIN string page given no offset | **bites:** the comparison, the allocation |
| a gathered null given no offset | **bites:** the comparison |
| values not spread past the nulls | **bites:** the comparison |
| validity bits a place off | **bites:** the comparison, the allocation |
| V1 levels read past their length prefix | **bites:** the comparison, the allocation |
| V2 values starting at the levels | **bites:** the comparison |
| bit-packed values a bit off | **bites:** the hybrid tests, the comparison |
| an RLE run's value a byte short | **bites:** the hybrid tests, the comparison |
| a truncated bit-packed run taken whole | **bites:** the truncation test. It was silent until the cut-run case was added |
| PLAIN characters not counted first | **bites:** the allocation |
| nothing read from its pages | **bites:** the comparison's ownership check |

## 4. Measured

v0.6.0's runner against this step's, both built as the bench builds them, alternated
three rounds a query in one session:

| query | CPU, v0.6.0 → now | wall, v0.6.0 → now |
| --- | --: | --: |
| q19 | 973 → 877 ms, −10% | 277 → 213 ms, **−23%** |
| q12 | 631 → 558 ms, −12% | 195 → 152 ms, **−22%** |
| q14 | 725 → 717 ms, −1% | 147 → 140 ms, −5% |
| q7 | 2,261 → 2,306 ms, +2% | 430 → 438 ms, +2% |
| h2o j3, Parquet | 8,688 → 8,608 ms, −1% | 1,380 → 1,175 ms, −15% |
| h2o j2, Parquet | 8,506 → 9,186 ms, +8% | 1,364 → 1,222 ms, −10% |
| h2o gb1, Parquet | 1,317 → 1,249 ms, −5% | 286 → 247 ms, −14% |

**Reading the table:**

- **q19 and q12, the dictionary-string queries,** fall by about a fifth of their
  wall time.
- **q7 and q14 read fixed-width columns.** They gain only the levels, which their
  noise hides: v0.6.0's own q7 rounds spread from 1,961 to 2,276 ms.
- **j2's CPU spread from 8,275 to 9,283 ms** within its rounds, so its +8% is not a
  measurement. Its wall time fell in every round.
- **Before the word copies,** a first version gave q19 −7% and q12 −3% CPU, with
  `gather` 25% of q19's profile.

**What is left in q19** is its string filter (`InStrings` and `memberBits`, about 18%)
and ZSTD: P2's work, and a fixed cost.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's, every Parquet column read
from its pages.
