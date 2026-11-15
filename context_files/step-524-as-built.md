# Step 524 — as built

**The CSV fuzz targets skip the open Zstandard-window finding, as the JSON ones do.**
A test-only change.

## What it was

Step 511 left a finding for the maintainer (`step-511-as-built.md` §4): klauspost's
Zstandard decoder allocates the whole window a frame declares, up to 512 MiB, before
it decodes a block, so a few bytes make any reader of `internal/source.Decompressed`
allocate that much. The NDJSON and JSON targets skip such inputs with
`skipZstdWindow`; the CSV targets, which share the decoder, did not. CI's fuzzing of
PR #74 then failed `FuzzScanCSV` on a 74-byte input, a frame beginning
`28 b5 2f fd`, allocating 105.1 MiB against its 97.5 MiB bound (run 43660955637),
and once ended a worker while minimising another.

## What it is

`FuzzScanCSV` and `FuzzScanCSVChunked` call `skipZstdWindow` after `inflated`. The
input is kept as `testdata/fuzz/FuzzScanCSV/zstd-window-past-the-bound` and runs as
a skipped seed, naming the finding. When the maintainer decides it, for example with
`zstd.WithDecoderMaxWindow` as the reference decoder's 128 MiB default, the skip goes
and this seed becomes the test.

## Checks

`go test ./internal/readfuzz`, `go vet`, `go run ./internal/gen/testinventory -check`.
