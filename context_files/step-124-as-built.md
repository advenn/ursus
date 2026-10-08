# Step 124 — as built

**A byte-array column is copied once, not twice.** Also: a ZSTD change that measured
nothing and was not kept.

## 1. The profile

PDS-H q1 at SF=1 after step 123 ran in about 540 ms. Its two string keys,
`l_returnflag` and `l_linestatus`, took about 20% of its CPU to read.
`cloneByteArray` was 5.5% of it; GC's work, from zeroing to scanning to write
barriers, about 20% in all.

arrow-go's `ReadBatch` on a byte-array chunk copies every value it decodes into a
fresh buffer, one per call, so that values outlive their page. `byteArrayCol` then
copies them again, into its own characters.

## 2. The change

`byteArrayCol` reads with `ReadBatchInPage`, which returns values that alias the
current page, and copies each call's values into its characters before the next
call, which may replace the page. One copy and no per-batch buffer.

- **A batch now spans several calls,** one per page it crosses. Its buffers grow
  per call, by `growExact`: append's doubling, across the pages of one batch, left
  spare capacity in buffers the column then keeps.
- **`flbaAsBytes`,** a fixed-length byte array read as Binary, keeps `ReadBatch`: it
  has no in-page form. It is rare.

## 3. Measured

The step 123 runner against this one, alternated, three rounds each, median of five:

| query | before | after | |
| --- | --: | --: | --- |
| PDS-H q1, SF=1 | 536–557 ms | 474–480 ms | −11% |
| h2o gb1, 10M | 335–338 ms | 285–295 ms | −14% |
| h2o j4, 10M (string keys) | 1,454–1,507 ms | 1,318–1,368 ms | about −10% |
| PDS-H q3, SF=1 | 257–259 ms | 251–261 ms | unchanged |

j4's peak memory varies from 3.1 to 4.2 GB between rounds of the same binary, which
is the collector's timing; no difference between the two shows through that.

## 4. What was tried and not kept: more ZSTD decoders

arrow-go decodes every ZSTD page through one process-wide klauspost decoder made with
default options, and klauspost caps a default decoder at four concurrent decodes,
while step 102's reader decodes on every thread. A codec registered through
arrow-go's `RegisterCodec`, decoding on GOMAXPROCS decoders, was built and measured:

| query | step 123 | with the codec |
| --- | --: | --: |
| PDS-H q6, SF=1 | 124–126 ms | 120–124 ms |
| PDS-H q1, SF=1 | 416–556 ms | 540–551 ms |
| h2o gb4 | 333–341 ms | 328–339 ms |
| h2o j1 | 1,388–1,422 ms | 1,362–1,396 ms |

Nothing outside the noise. It would have made klauspost a direct import, against
the one-dependency rule, for no measured gain, so it is not in the tree.

## 5. Tests and teeth

- **`TestStringsAcrossManySmallPages`** reads a PyArrow fixture,
  `pyarrow_small_pages.parquet`, with 256-byte data pages, in batches of 97. A batch
  spans dozens of pages. It has a dictionary-encoded and a plain string column, with
  nulls, and checks every row against the formula that wrote it.
- **`TestParquetReaderRoundTripsNulls`** (step 123) covers the null paths.

| tooth | result |
| --- | --- |
| the pages' values copied only after the last page, aliasing pages since replaced | **bites** |
| `growExact` loses what it held | **bites** |

Two teeth were silent, and correctly so. Passing each page's levels from the start
of the buffer, rather than from the row reached, is not a bug, since `take` reads the
slice it was given. Appending to a full-capacity slice still copies the bytes. Both
were teeth that did not model a fault.

**Gate:** test-all 110 ok, race 22 ok, levels, vet ×3 and the bench engine tests clean; PDS-H SF=0.1 22/22 against DuckDB.
