# Step 549 — the guards' comments and the fuzz seeds trimmed

**Comments, records, test names and test inputs; no program logic.** At the
maintainer's request of 2026-11-28, step 547's trim reaches the code. The guards'
comments, the hunt's records (steps 506 to 547), the CHANGELOG's v0.8 sections and the
index now say what ursus checks on crafted Parquet and Arrow IPC input, and why in
general terms, and no more.

## 1. What changed

- **Comments**, in `internal/arrowmeta`, `internal/source/parquet`, `internal/readfuzz`,
  `internal/arrowin`, `internal/source/arrowsrc`, `internal/zstdframe`, `internal/uerr`,
  `internal/fuzzcheck` and the root package's IPC and Parquet files. Each guard is
  described as validation of untrusted input:
  - a count is a claim its bytes must hold;
  - nesting is capped;
  - an offset points forward and within its bytes;
  - a page holds what its header and its chunk say.
- **Five runtime messages**, reworded to say only what is wrong:
  - `internal/arrowmeta`: "fields nest past 2000 levels";
  - `internal/arrowmeta`: corruptInput's hint, "ursus checks every count and offset of
    Arrow IPC metadata against its bytes before reading it";
  - `internal/source/parquet`: a dictionary index of 0 bits followed by a byte its run
    does not hold;
  - `internal/source/parquet`: checkEncodings' two hints, "a column chunk's encodings
    are checked before any of its pages is read".
- **The CHANGELOG's v0.8.0 section** lists the hunt's seventeen guard entries as three:
  - Arrow IPC input checked against its bytes before it is read;
  - a Parquet footer checked against its file;
  - a Parquet page checked against its header and its chunk.
- **59 fuzz seed inputs left the repository:**
  - the `finding-*` inputs of FuzzScanIPCFile and FuzzScanIPCStream;
  - all twelve of FuzzScanParquet's;
  - all of FuzzOwnPagesMatchArrow's except `forged-honest-dictionary`,
    `forged-honest-int64` and `time-past-its-day`.

  The targets keep their Go-built seeds and the inputs `forge_test.go` builds.
- **Tests renamed**, each to the check it makes:
  - TestADictionaryCountInALongVarintIsCountedAsThriftReadsIt;
  - TestAMalformedDeltaPageIsRefused;
  - TestReadIPCSchemaRefusesACorruptFooter;
  - TestForgedIPCMetadataIsRefused;
  - TestASchemaTreeNotClosingIsRefusedAsCorrupt;
  - TestCheckedPagesRefuseAnIndexPastTheDictionary.

## 2. What it costs

The tests that read a removed seed by name lose that case. Each keeps its Go-built
cases of the same shape:
- **pqfooter_test.go:** four corpus cases.
- **codecbound_test.go:** CI's file; the forged 2^30 Zstandard frame stays.
- **ipccut_test.go:** the findings loop; the buffer-cut test stays.
- **ipcwalk_test.go:** six inputs; the patchMeta cases stay.
- **nullrows_test.go:** the seed's subtest; the generated inputs stay.

Three internal tests lose the cases that measured another decoder's allocation, beside
ursus's refusal:
- the footer walk's list test;
- the schema-tree test;
- the dictionary-index test.

One test went whole, TestARowGroupMissingAColumnChunkIsRefused. Its check is still held
by internal/source/parquet's TestARowGroupOfAnotherCountOfChunksIsRefused, though no
longer through the public API.

Two checks now have no case of their own:
- **the page cut short:** its error not wrapping `io.EOF` was asserted only on the
  removed seed;
- **the IPC walk's field nodes:** its refusal of a record whose field nodes point past
  its metadata. TestTheWalkReadsOnlyItsBytes mutates every byte, but asserts only no
  panic and no read past the bytes.

Nothing was built or run locally, to keep the laptop quiet. gofmt is clean, and CI
runs the suite.
