# Step 83 — as built

**Object stores: the seam ships in 0.3, the stores move to 0.4.** This is
`v0.3-scope.md` §3 item 4. That section called cloud object stores the one item
"that unlocks users — most Parquet lives in S3". §6 sized it as the largest item and
the one most likely to be a milestone of its own.

It was the tenth step of the road to 0.3.

Five commits and this document.

Authoritative where it disagrees with the vision docs and [`design/`](./design/).

Each of these was asserted on its own exit code:

- `make test-all` exit 0, with **105** package-ok lines: 21 packages × 5 SIMD
  configurations.
- `make race` exit 0 (21).
- `make levels` and `go vet` clean in all three modules.
- PDS-H SF=0.1 exit 0, with **22/22** matching the duckdb reference.

No benchmarks.

**The suite is 2890 passing tests and subtests** (2867 at step 82).

---

## 1. What was measured

- **No object-store code existed.** `ursus-api.md` §9 specified an
  `objstore.Store`, and `design/foundation.md` made it a stdlib-only leaf; neither
  was built.
- **Both sources already read through an internal opener**, a function returning a
  fresh handle per open. Nothing public exposed either, so a caller with Parquet in
  S3 had to download whole files into `ScanParquetBytes`.
- **The Parquet reader is already object-store shaped.** It finds the size with a
  `Seek` to the end, reads the 8-byte trailer and then the footer, prunes row groups
  by statistics, and makes one `ReadAt` per projected column chunk. arrow-go's
  buffered stream is off, so each chunk is read whole, never the file.
- **No context reached an opener.** Both sources had the query's ctx at `Schema` and
  `Open`, and dropped it before opening.
- **The dependency policy rules out in-tree SDK stores.** The README says "pure Go,
  `go get` is the whole install". `foundation.md` says "one direct dependency".
  pqarrow was dropped for pulling in gRPC.

## 2. What was built

1. **A context reaches every opener.** Both internal `Opener` types take the query's
   ctx. The local-file and in-memory openers ignore it. A CSV stream that fails to
   open is named, as a Parquet file is, rather than as "part 2 of" the first.
2. **`ScanParquetFrom([]ParquetFile)`.** A `ParquetFile` is a name and
   `Open func(ctx) (io.ReaderAt, int64, error)`.
   - Open is called once for the schema and once per execution.
   - A reader that is an `io.Closer` is closed however the query ends.
   - Several files follow `ScanParquetFiles`' rules.
   - **A read that failed inside a column chunk named the column and not the file**,
     for local files too. It names both now.
3. **`ScanCSVFrom([]CSVFile)`.** A `CSVFile` is a name and
   `Open func(ctx) (io.ReadCloser, error)`, under `ScanCSVFiles`' rules. **A read
   that failed mid-stream named the whole source**; it now names the file it failed
   in.
4. **Writing needs nothing new.** `WriteParquet` and `WriteCSV` already took any
   `io.Writer`.
5. **The deferral, written down:**
   - `v0.3-scope.md` §3 item 4 and a §4 entry give the reason and what 0.4 needs:
     URI schemes, globbing through a `List`, retries, a sink that cannot rename, and
     a cached footer, since a scan reads each footer twice;
   - a note in `ursus-api.md` §9;
   - a README paragraph showing the adapter.
   - The README also stopped saying nested columns cannot be written to Parquet,
     which step 81 made false.

This was a feature, not a defect, so there was no ratchet: tests against an API
that does not exist cannot compile before it does. The tests landed with each
commit, and the teeth below are what keep them honest.

**`source_seam_test.go`: 13 Parquet and 8 CSV cases.**

- **Parquet:**
  - It answers as `ScanParquetBytes` does.
  - One column of eight reads under a quarter of the file.
  - A filter that statistics prune reads nothing beyond the footers.
  - Open sees the query's context, and cancelling the query reaches a reader that
    kept it.
  - Every reader is closed, after an early break and after a failure.
  - A mid-scan failure, a failed Open, and a size past the end are each I/O errors
    naming the file.
  - Several files answer as `ScanParquetFiles` does, and files that differ are
    refused naming both.
  - No files, or a nil Open, is refused.
  - Explain names the files.
- **CSV:** the same, plus a later file failing to open, or mid-stream, being named.

## 3. Teeth

Every patch was checked to have applied, and every one ran against a green baseline.
**10 of 11 bite.**

| reintroduce | fails |
| --- | --- |
| Parquet's Open given a background context / CSV inference likewise | the context case, each |
| the reader's closer dropped | five cases that count closes |
| the size ignored | eight cases |
| the file dropped from a column's open error | the mid-scan failure |
| CSV naming the source, or "part N", instead of the file | the later-file cases |
| only the first file's Open used, in either | the several-files cases |
| a nil Open not refused | its refusal case |

**Silent, and removed:** the commit that added `ScanParquetFrom` also annotated a
column *read's* error with its file. No case could reach that path:

- a chunk is fetched whole when it is opened, so a failing `ReadAt` surfaces at the
  open, which names the file;
- a corrupt page reads as a short column, which the row-count check reports — and
  that check names the file itself. This was measured by flipping 16 bytes at four
  offsets under Snappy, Zstd and no compression.

A separate commit removed the annotation.

## 4. Behaviour changes

- **New:** `ParquetFile`, `ScanParquetFrom`, `CSVFile` and `ScanCSVFrom`.
- **Error messages:**
  - a Parquet column chunk that fails to open names its file;
  - a CSV stream that fails to open, or fails mid-read, names its file.

## 5. Still open

- **The stores themselves**, in 0.4, with everything `v0.3-scope.md` §4 lists.
- **Each footer is read twice per query**, once to plan and once to execute. That
  is free on a disk and a round trip per file over a network.
- **arrow-go swallows a corrupt page's decode error**, and the column reads as
  short. ursus's row-count check is what turns it into an error, so the message
  says rows are missing rather than that a page is corrupt.
- **The rest of the road to 0.3:** release readiness.
