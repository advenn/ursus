# Step 346 — as built

**Compressed input,** `polars-parity.md` §5's third item: a CSV, NDJSON or JSON file
compressed by gzip, zlib or Zstandard is read as itself, as Polars reads one, known by
its first bytes whatever its name.

## 1. How

**`source.Decompressed`** reads a stream's first four bytes and gives the stream
decompressed, or itself: gzip's `1f 8b` and Zstandard's `28 b5 2f fd`, which begin no
text, are decompressed as they are read; the file is never held whole. Every stream a
CSV, NDJSON or JSON scan opens goes through it: the CSV source's inference and runs
(`Source.open`), and the NDJSON source's, before a JSON document is made lines. So
`ScanCSV`, its several-file, in-memory and caller-opened forms, and every NDJSON and
JSON scan read compressed files, a gzip file of several members too.

**zlib's header can begin a text:** `78` and one of `01`, `5e`, `9c` and `da`, and `78
5e` is "x^", which may begin a CSV header. Polars trusts it, and reads a CSV of
`x^2,b` as no rows. Here it is trusted only where the stream is zlib's beyond doubt
(`isZlib`): a stream of 64 KiB or less must decompress to its end, its checksum met;
a longer one's first 64 KiB must decompress without an error to a quarter more than
they are. Text read so decompresses, deflate's fixed codes taking most of its bytes for
a byte each, to about as much as it is, until an error; a compressed text, to more.
Bytes after a zlib stream's end are refused, as a gzip stream's are, which Go's zlib
reader would drop.

**The decompressors are klauspost's** (`github.com/klauspost/compress`, which arrow-go's
Parquet already uses, now a direct dependency): its gzip inflates 315 MB/s here, the
standard library's 240, and its Zstandard decoder decodes ahead on goroutines of its
own, 680 MB/s, where one goroutine decodes 435. A goroutine inflating gzip ahead of
the reader was tried and made nothing faster: the CSV reader already parses blocks
while it cuts the next, and the bound is the one goroutine that inflates.

| Polars 1.44.1 | ursus |
| --- | --- |
| `read_csv`, `scan_csv` of a gzip, zlib or Zstandard file, `.csv.gz` or `.csv` | read as the plain file |
| a gzip file of two members | read whole |
| `read_ndjson`, `read_json` of one | read as the plain file |
| a gzip stream cut short, or followed by text | refused: "unexpected end of file" |
| a CSV whose header begins `x^` | no rows in Polars; read as text (feature-gaps §9) |
| `x` then the byte `01` | refused in Polars, "corrupt deflate stream"; read as text |

## 2. Tests

**`TestCompressedInput`,** Polars 1.44.1's answers: a CSV by gzip, by two gzip
members, by zlib at each of its four headers, and by Zstandard, each from memory, from
a file named `.csv`, and past a given schema, so that the run's stream is read as well
as the inference's; NDJSON and JSON by each; a CSV header of `x^`, short and past the
64 KiB tried; refused, a gzip stream cut short or followed by text, a zlib stream
followed by text, and a Zstandard stream cut off; and 150,000 rows, three of the
reader's blocks, by gzip, Zstandard and a zlib stream longer than the 64 KiB tried,
equal to the plain file's frame on one thread and on eight, and its first three rows.
Under the race detector, 80,000 rows.

**Teeth (10 of 10 bite):** gzip not known; Zstandard not known; zlib's `78 5e` not
known; zlib trusted where its prefix decompresses to as much as it is (first drawn so
that it did not build, and redrawn); a whole stream trusted as zlib unchecked; bytes
after a zlib stream let through; a plain stream's first bytes lost; a CSV's inference
not decompressed; a CSV's run not decompressed; NDJSON not decompressed.

## 3. Performance

A million rows of four columns, 34 MB, in memory's page cache, the median of seven,
the machine shared (a load of 4):

| | ursus | Polars `read_csv` | Polars `scan_csv` |
| --- | --- | --- | --- |
| plain | 84–97 ms | 29.4 | 44.3 |
| gzip, 9.0 MB | 191–207 | 92.8 | 83.1 |
| Zstandard, 9.8 MB | 129–152 | 86.4 | 76.8 |

Decompression is the read's serial part: the 34 MB inflate alone in 108 ms, more than
half the gzip read's time, the parse running beside it. A compressed file is read in
about twice Polars' time, as a plain one is (§8): Go's inflate is slower than the one
Polars links, and the CSV parse's own gap stands before it.

**Gate:** test-all 135 ok, race 27 ok (the root package 823 s here, the machine shared),
levels, vet ×3 and the bench engine tests clean; the 22 PDS-H answers match DuckDB.
