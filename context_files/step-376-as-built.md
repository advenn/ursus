# Step 376 — as built

**A Parquet file's pages decompressed into buffers that are reused.** R1 of
`v0.7-scope.md`.

## 1. How

arrow-go's page reader decompresses each page into buffers it allocates from its
reader properties' allocator, and frees them when it closes. ursus opened every file
with arrow-go's default properties, whose allocator makes a buffer afresh and frees
none, and makes a page reader for each column chunk. DuckDB's lineitem holds one page
of a megabyte a chunk, so a scan of q14's four columns allocated 270 MB of page
buffers a run, every one zeroed, and the collector ran after every few.

- **`pagePool`** (`internal/source/parquet/pool.go`): a `memory.Allocator` of
  power-of-two buffers from 4 KiB to 256 MiB, each size kept in a `sync.Pool`, which
  the collector empties when nothing takes from it; a smaller or a larger buffer is the
  runtime's, as before. A buffer comes back as it was left, not zeroed: arrow-go
  writes a page's bytes before it reads them.
- **Every file is opened with it** (`readProps`, `parquet.go:705`), so a row group's
  pages are decompressed into the previous one's buffers. ursus's own page readers
  close (`pages.go:402`, `:548`) and copy a page's values, its dictionary's too,
  before the next page, so no column holds a buffer the pool hands on.
- **Not taken: arrow-go's buffered stream,** which reads a page straight into its
  decompression buffer, where `GetStream` reads each chunk into a slice of its own
  first, 81 MB of q14's scan a run. With it, a file whose row group claims more rows
  than its chunks hold read as no rows and no error, and a read that failed mid-scan
  lost the file's name: `TestPanicsAreErrors` and `TestScanParquetFrom` caught them.

## 2. Measured

**What the scan allocates:** q14's four lineitem columns at SF=1, summed, on one
thread, seven scans: 3.89 GB before, 2.17 GB after, of which 1.31 GB is the columns'
values the scan hands on, 0.57 GB `GetStream`'s chunks, and 0.19 GB the pool's own,
26 MB a scan, where a collection has emptied it.

**The bare scan,** that one and q14's month filtered, on one thread, v0.6.8's code and
this step's alternating, three rounds of seven, at a load of 4 to 6 from other work:

| | v0.6.8 | here |
| --- | --- | --- |
| four columns, medians | 409–450 ms | 365–400 ms |
| filtered by q14's month | 462–510 ms | 406–445 ms |

On a quiet machine, with the buffered stream too, the four columns took 242 ms where
they took 306.

**PDS-H at SF=1,** the scope's six, on four threads, v0.6.8's runner and this step's
alternating, two rounds of five, at a load of 6.6 to 7.3:

| | v0.6.8 | here | |
| --- | --- | --- | --- |
| q14 | 197.8 ms | 184.3 | 0.93 |
| q17 | 302.8 | 275.9 | 0.91 |
| q19 | 296.2 | 279.0 | 0.94 |
| q12 | 216.5 | 202.0 | 0.93 |
| q15 | 153.3 | 145.8 | 0.95 |
| q10 | 324.6 | 329.6 | 1.02 |
| in all | 1,491 | 1,417 | 0.95 |

**PDS-H at SF=0.1,** all 22 on four threads, the same two runners alternating query by
query, their order swapped each round, three rounds of five at a load of 2.3 to 3.3:
**0.93 of v0.6.8's time in all** (600.2 ms to 560.2), q18, q21 and q12 0.86 to 0.88,
none slower beyond its noise, q9 the most at 1.05.

## 3. Tests

- **`TestPagePoolsSizes`:** a buffer is its size's power of two from 4 KiB; past 256
  MiB exactly its size; a grown one keeps its bytes, a shrunk one stays where it was;
  one the pool did not make is let go.
- **`TestPagesAreDecompressedIntoPooledBuffers`:** eight row groups of two Int64
  columns and a nullable String, Zstandard, a page a chunk as DuckDB writes lineitem,
  read on one thread and four. From the third row group on, one thread allocates 1.36
  times the bytes it produces, where it allocated 2.30 without the pool, the 0.36
  `GetStream`'s chunks; bounded at 1.6, and four threads, whose workers decode ahead
  of the count, at 1.5. And every batch reads as written after the whole file has
  been read.

**Teeth: 5 of 5 bite:** the files read without the pool; a freed buffer kept from the
pool; a freed buffer handed out twice, which on four threads two page readers
decompress into at once, an error from zstd; a size class one power too small; a grown
buffer that loses its bytes.

**Gate:** the light gate, one package at a time on four procs: the tests at v3, 28
packages ok; levels, vet ×3 and the bench engine tests clean; the 22 PDS-H answers
match DuckDB.
