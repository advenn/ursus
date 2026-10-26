# Step 368 — as built

**Large strings:** a String or Binary column past 2 GiB of characters, its offsets
64-bit, where it was refused; L3 of `v0.6.8-scope.md`.

## 1. How

**A second representation** (`internal/data/column.go`): a column holds `offs`, 32-bit
offsets in a buffer, or `offs64`, an `[]int64` of its length and one, past
`MaxStringBytes`, never both. It is a Go slice, not a buffer, so that `Strings` stays
inlinable (cost 67 of 80) with one more branch: `Buffers` reports it by `sliceID`, and
`RawOffsets64` gives it as bytes.

**Made only by the combiners,** which count first:

- `NewString`, `ConcatStrings`, `TakeStrings` and `TakeStringsChunked` choose the
  width by the characters they will hold (`stringBuffers`), whatever their inputs'
  widths, through generic writers over `offsetWidth` (`~int32 | ~int64`): a part or a
  source of either width, an answer of either;
- these are what every Collect, join build side, sort, window and aggregation ends in,
  so all of them now answer past 2 GiB: the probe of ten such queries under a lowered
  limit found none that refused;
- a constructor of a reader's 32-bit offsets, `NewStringParts` and `NewStringOwned`,
  still refuses past them, so a source's batch past 2 GiB is a resource error that
  says "read it in smaller batches"; `Slice` keeps a column's width.

**Read through `StringAccessor`:** `Get`, `Bytes` and `Len` take one branch on
`offsets64`, and still inline. A row-at-a-time `Strings().Get(i)` costs 4% more
(3.81 → 3.95 ms a million rows); `ConcatStrings`, `TakeStrings` and its chunked form
are as fast as at v0.6.7.

**The few that read offsets themselves:**

- **The Arrow export** (`arrowout`): a column of 64-bit offsets whose rows' characters
  fit 32-bit ones, as a slice of a collected frame's does, is rebased to them and is a
  String; past them it is a LargeString or a LargeBinary, sharing its buffers. A
  List, an Array or a Struct holding one says so in its type (`heldType`), and a
  record's schema in its field (`heldSchema`). `RecordLarge` makes every text large,
  a 32-bit column's offsets widened; a Categorical's dictionary past 2 GiB is refused.
- **`DataFrame.Equals`** compares records, so where the two frames' types differ it
  compares both as `RecordLarge` makes them: the same text, one frame past 2 GiB and
  one not, is equal.
- **The IPC writers** (`writeRecords`): a batch whose record would be a LargeString
  the file's schema does not say is written as its halves, each so, down to a row.
- **`ScanArrowRecords`** copies a record of LargeString or LargeBinary past 2 GiB in
  row ranges that each fit (`recordSpans`), so a frame's own export reads back.
- **The spill format:** `kindString64`, a column's rows past 2 GiB, its offsets
  rebased; a column of 64-bit offsets whose rows fit spills as `kindString`.

## 2. Tests

The limit is lowered so each path meets a column past it at a unit test's size.

- **`TestStringColumnsOfSixtyFourBitOffsets`** (limit 64): `NewString`, a rename, a
  Binary; the buffers counted once; slices; `ConcatStrings` of 32-bit parts, one a
  slice, past the limit, of mixed parts, of 64-bit parts that fit, and with a part of
  no payload; `TakeStrings` both ways and with a negative index; the chunked take in
  chunks of 1, 3, 64 and 1000.
- **`TestStringColumnsRefusePastTheirOffsets`:** `NewString` past it builds; the
  readers' constructors refuse, saying "smaller batches".
- **`TestTextPastItsOffsetsExportsLarge`:** a LargeString, a LargeBinary, a slice past
  the limit, a slice that fits rebased to a String, a list's and a struct's types, a
  record's schema, `RecordLarge` widening a 32-bit column and a column of no payload.
- **`TestSpillRoundTripsSixtyFourBitOffsets`:** a String and a Binary column whole, a
  slice past the limit from row 5, and a slice that fits, each read back as written at
  the width it fits.
- **`TestTextPastItsOffsets`** (the root, limit 4 KiB, 200 rows of 64 bytes), which
  replaces `TestCollectRefusesAStringColumnPastItsOffsets`: a collect, a sort by
  another column and by the text, a group-by, a join, a unique, a window, an implode
  and an explode, a maximum, each on 1 and 4 threads, equal to its answer under the
  real limit; the collected frame exported as a LargeString and read back; written as
  Parquet, CSV, NDJSON and IPC and read back; sorted spilling under a 4 KiB budget; a
  Parquet batch past the limit a resource error.
- **`TestConcatStringsPastItsOffsetsHoldsThem`**, where the kernel test refused.
- **`TestTextPastTwoGiB`:** a real 3 GiB column, two copies of a 1.5 GiB one, its
  rows read on both sides of 2^31, sliced, taken and exported. It holds about 4.6 GB,
  so it runs with `URSUS_BIG_STRINGS=1` only, in CI's new `bigstrings` job, and not on
  the gate's laptop; `URSUS_BIG_STRINGS=small` runs it at 3 MiB, which passed here.

**Teeth: 21 of 21 bite:** no 64-bit offsets made; the accessor blind to them; `Get`
reading from the first row; a slice of them not windowed; a 64-bit part concatenated as
no payload; a part's offsets not rebased; a 64-bit take or chunked take counted as no
characters; a chunk's last offset dropped; the offsets not counted in `Buffers`; a
reader's offsets past the limit built; never a LargeString; a slice that fits not
rebased; a record's schema, or a list's type, as its dtype says; `RecordLarge` not
widening; a spill always of 32-bit offsets, or of 64-bit ones not rebased; `Equals`
blind to large text; a large record copied whole; an IPC batch never split.

**Gate:** the light gate, one package at a time on four procs: the tests at v3, 28
packages ok; levels, vet ×3 and the bench engine tests clean; the 22 PDS-H answers
match DuckDB. CI's widths, scalar build and race detector after the push, and its new
`bigstrings` job the real 3 GiB column.
