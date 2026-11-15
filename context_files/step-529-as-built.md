# Step 529 — as built

**A Null column, and a batch of no columns, hold no memory a row.** The maintainer's
decision of 2026-11-15, on step 511's finding of 2^40 rows of no data, which step 522
moved (§5 of [`step-522-as-built.md`](./step-522-as-built.md)): as in Polars, a column
of the Null type is a type and a length, and a frame of no columns a row count, so a
well-formed Arrow IPC stream of a few hundred bytes declaring 2^40 rows of either is
read, counted and collected in constant space, where it allocated until the process
ended. Nothing is refused that was read, and no answer changes: memory only.

```
arrow-go: a stream of one Null column, one record of 2^27 rows, 232 bytes
before:  Count 22 ms, 25 MB allocated;  Collect 93 ms, 66 MB
after:   Count 61 µs, 10 kB;            Collect 43 µs, 10 kB
2^40 rows (by the rate): Count about 2 minutes and 200 GB, Collect 128 GiB; now as at 2^27
```

## 1. Where the memory and the work came from

Every place a Null column, or a batch of no columns, cost something a row, found by
reading the path of `ScanIPC*`, `ScanArrowRecords`, `ScanArrow` and a frame in memory
through `Count` and `Collect`, and through the bitmap view's API:

| where | what it did a row | at 2^40 rows |
| --- | --- | --- |
| `data.NewNull`, `bitmap.Zeros(n)` | a column of n nulls held a materialised bitmap of zeros, a bit a row: every Null column read (arrowin's `nullBuilder`, the NDJSON and Parquet readers), and every typed null `NewNull` makes | 128 GiB |
| `kernel.concatColumnN` (`Collect`'s concatenation, a join's build side) | built the parts' validity, a bit a row, before it looked at the type and answered `NewNull` | 128 GiB |
| `arrowsrc` (every Arrow and IPC scan), `memsrc` (a frame in memory), Parquet's rows-only reader | cut each record, frame or file into batches of 8,192 rows: a `Batch`, a `Column` and a builder each | 2^27 batches |
| `exec.Collect` (`drain`) | held every batch until the end, then concatenated them | 2^27 batches held, about 40 GB |
| `Count` of `ScanIPC*` | `files.Source`, which every IPC reader goes through, did not answer a scan of no columns (`source.RowsOnly`, step 370), so the planner gave it its cheapest column: a Null column counts as no width, so a file of only Null columns had its first read | a bit a row, as above |
| `kernel.Take` of a Null column | gathered the selection's validity, a bit a row, then threw it away | per selection |
| `kernel.NullColumn` of Null | made `Zeros(n)` before it answered `NewNull` | as `NewNull` |
| `arrowout.columnData` (`DataFrame.Record`, the IPC writers) | made a Null column's validity, `Buffer()`, before its Null arm, which uses none | 128 GiB |

What already cost nothing a row: arrow-go's IPC reader builds a Null array of no
buffers, and a record of no columns, from the record's metadata alone; arrowin's
`Checker` validates a record's columns, not its rows; `nullBuilder` counts rows;
arrowmeta's walk (step 519) reads a record's nodes and buffers, not its rows.

## 2. The design

**A column of nulls is a length** (`internal/bitmap`). A `View` with no buffer was
all set; it may now be all clear too, at offset −1 (`clearOffset`), so `View` stays
three words. `Zeros(n)` returns that form and allocates nothing at any n. Every reader
of a view is a method or a function of the bitmap package, since its fields are
unexported and `RawBits` is the one way to the bytes, so each was taught the form:

- `Get`, `CountSet`, `CountUnset`, `Slice` (the form survives a slice), `Words`
  (words of zeros), `PopCount`, `IsByteAligned`, `wordRel`;
- `IsAllSet` is the all-set form alone, and `IsAllClear` is new;
- `And`, `Or`, `AndNot`, `Not`, `AndMulti`, `AppendSetPositions` and a `Builder`'s
  `AppendView` answer it, the combinators without a loop where the forms decide
  (`And` of all clear is all clear; `Not` of all set is all clear and of all clear
  all set; `AndNot(x, AllSet)` is all clear), and `zip` reads either operand through
  `wordRel`;
- `RawBits` returns `(nil, 0)` for either form with no storage; its one caller,
  `data`'s accounting (`viewID`), counts no bytes for it, which is right;
- `Buffer` makes the bits, zeroed, for the all-clear form: the one reader that must,
  since Arrow has no all-null bitmap of no buffer but the Null type's. Its callers are
  arrowout's export and the spill writer, which wrote and still write a typed column
  of nulls' validity as bits.

So `data.NewNull` holds no bitmap, of any type, and nor does every other column of
nulls built with `Zeros`: `NullColumn`'s validity (a typed null operand: a literal, an
outer join's padding, a cast of nulls), a few kernels' all-false Booleans, `Not` of an
all-set view. That widening is deliberate: it is memory only, like the rest, and it
puts the form under the whole suite, every operator's oracle tests running over it.

The types' own arms come first now: `Take` and `NullColumn` answer `NewNull` before
they gather or make a validity, `concatColumnN` before it reads the parts' validity
(and parts all clear concatenate to all clear), `arrowout` before it shares one.

**A batch of only Null columns, or of none, is handed whole where its consumer keeps
it so** (`source.ScanSpec.Whole`, `physical.Options.Whole`). The batch size is a
contract other operators rely on: a filter's and a join's selection is an `[]int32`,
an expression allocates its answer a batch at a time, and a stream (`CollectBatches`,
a sink) holds a batch at a time. So a scan is told it may hand such a batch whole only
where every operator above it, up to the root, keeps a Null column a length whatever
the batch's rows, and the root holds the result as a count or a frame:

- `Collect` and `Count` set `Options.Whole`; nothing else does: `CollectBatches`, the
  sinks and writers, `Profile`.
- `planNode` hands it to a node's children only through `passesWhole`: a scan, a
  `Project` of columns by name (renamed or not), a `Limit`, a `Slice` from the start
  and a `Union` (a vertical concatenation). Any other node — a filter, an expression,
  a group-by, a sort, a join, a row index, a tail, a reverse, a diagonal concatenation's
  projection of nulls — plans its children without it, and their scans cut at
  `BatchSize` as before.
- A source honours it only for a batch whose columns are all Null, or none
  (`source.NoData`): `arrowsrc` hands the rest of its current record, `memsrc` a
  frame's batch, Parquet's rows-only reader a file's rows; `files.Source` passes it to
  its files' sources unless a shared column (a hive key, the path column) is
  projected, which is repeated a row at a time. Every other source ignores it.

How far it goes, then: to the end of a record, a frame's batch or a file, never past
what the input itself holds in one piece, and only into the five node kinds above.
Nothing that computes on a batch's rows sees a batch past `BatchSize`.

`files.Source` answers `RowsOnly` now where each file's source does (every IPC
file's, `arrowsrc`), so `Count` of IPC files reads no column, of any type: Count of a
2^20-row IPC file of three columns allocated 34 MB, its cheapest column copied, and
allocates 24.7 MB now, arrow-go's reading of the record's body.

**What a Null column costs, operator by operator.** In constant space, whatever its
rows: the scan of an Arrow IPC file or stream or of arrow-go's records, `Count`,
`Collect`, `Head` and `Limit`, `Slice` from the start, `Select` of columns by name,
`Concat` of such frames with each other or a frame in memory, a frame in memory
(`DataFrame.Lazy`) read so, and `DataFrame.Record`. Correct, and per row where they
must be, as before: `Filter` (its mask and selection), `WithColumns` and `Select` of
any expression over a Null column (a cast to a typed column holds a payload; `IsNull`
a Boolean), aggregates, a window, `WithRowIndex`, `Tail` and a `Slice` from the end,
`Reverse`, `Unpivot`, a diagonal `Concat` (its other frames' columns made null a row
at a time), joins, and `CollectBatches` and every sink, IPC's included: a record a
batch of 8,192 rows, 2^27 of them for 2^40. A Null column as a group key, a sort key,
a join key, a window's partition or `Unique`'s key, and `Min` or `Max` of one, are
refused, as before.

**What is left of step 511's finding.** Other columns that cost an IPC input nothing a
row: a Struct of only Null fields, or of none, a FixedSizeList of Null or of size 0,
an extension stored as one. arrowin builds a validity bit a row for each, so a record
of 2^40 rows of one allocates still. The IPC targets keep skipping a record declaring
more rows than eight a byte of its input unless its schema's columns are all Null, or
none (`oversized`, `nullOnly`), and a dictionary batch so. The maintainer's.

## 3. The tests

**The bitmap form** (`internal/bitmap/allclear_test.go`), against a model of the bits
one by one, as the package's other tests: `TestZerosReadsAsStoredZeros`, the
all-clear form, whole and sliced at a bit offset, at 17 lengths across every byte
and word boundary, read by every reader as stored zeros are, its `Words` the same
words, its `Buffer` zeros; `TestCombinatorsOverEveryForm`, `And`, `Or`, `AndNot`,
`Not`, `AndMulti`, `AppendSetPositions` and `AppendView` of every pair of eight
operands (both forms with no storage and three stored, each also sliced), the answer's
form where the operands' decide it; `TestZerosAllocatesNothing`, 2^40 bits sliced,
counted, read and combined, 0 allocations (`testing.AllocsPerRun`).

**2^20 to 2^40 rows in constant space** (`internal/readfuzz/nullrows_test.go`,
`TestNullRecordsReadInConstantSpace`). Thirteen inputs: the record the 2^40 input
declares, as arrow-go writes it, and twelve generated files and streams of up to three
Null columns, or none, in one to four records of 2^20 to 2^40 rows, two ending in
exactly 2^40, uncompressed, LZ4 and Zstandard. Each is read by `ScanIPCBytes` and
`ScanIPC`, or `ScanIPCStreamBytes` and `ScanIPCStream`, and as arrow-go's records by
`ScanArrowRecords`; each read is counted, counted after a `Head`, collected, its head
taken, a column selected and renamed, a slice taken and the frame concatenated with
itself, and the frame collected is counted in memory, concatenated with its head and
the scan, and exported by `Record` and counted again; all inside `fuzzcheck.Run` at
the IPC targets' bounds (64 MiB and 1 KiB a byte of input allocated, ten seconds,
`ipcLimits`), under `prlimit --as=4294967296`. The oracle is arrow-go's reading of the
same bytes: the sum of its records' rows, its fields' names, Null. Every frame's
columns are Null and all null (`allNull`, a column's null count against its length).

The 2^40 input as the fuzzer saved it (`finding-record-of-2e40-rows-of-no-data`) is a
record of no columns and 2^40 + 18,432 rows whose field nodes' offset points past its
metadata: arrow-go, reading no column, never follows it, and step 519's walk refuses
the record as corrupt before a row is read ("in its message 2, a record batch's nodes
at byte 60 points to byte 5300, past the 168 bytes"). The targets skipped it, with
every record of so many rows, so the refusal went unseen since step 519; the test
holds `Count`, `Collect` and `Head` of it to that refusal, an `ErrIO`, inside the
bounds, and reads the record it declares as arrow-go writes it.

**The fuzz targets run the class.** `oversized` no longer skips a record whose
schema is only Null columns, or none (above). The targets' oracle compared a frame
with arrow-go's reading a row at a time; for a frame whose columns are all Null, or
none, it compares each column's null count with its length, and arrow-go's columns
are held as null without a cell a row (`checkIPC`, `sameRead`, `arrowRead`, `nullStored`
for an extension stored as Null). The seeds gain a file and a stream of two Null
columns of 2^40 rows and of no column of 3 and 2^40, and `TestOversizedPassesCleanInputs`
holds the walk to skip none of the two of each.

**The answers are as before** (`nullcolumns_test.go`, `TestNullColumnsAnswerAsBefore`).
qcheck's generator draws its tables from six base types and has no Null; adding one
means its typing rules for every operation, the DuckDB SQL and its answers'
canonical cells, so its self check cannot draw a Null column. This differential is
its operations', over one: frames of 0, 1, 3, 17, 300 and 1,000 rows of an Int64
key, Float64 quarters (exact sums in any order), text and a Boolean, each with nulls,
and two Null columns, in an order drawn, as one record or several, scanned from
arrow-go's records and from a `DataFrame` in memory, through 42 queries: qcheck's
operations (Scan, Select, Filter, WithColumns with FillNull, Cast, When, arithmetic,
a comparison and IsNull, GroupBy keyed and keyless with Count, NullCount, Len, First,
Last, Sum and Mean, Sort, Head, Slice, Join inner, left, semi and anti, Count) and
more (a window, Shift, Tail, a Slice from the end, Reverse, WithRowIndex, Unique,
DropNulls, Unpivot, a full join, Concat by position and by name), each a Null column
an operand, a passenger or the whole frame, and six that ursus refuses (a Null key
of a group-by, a sort, a join, a window and `Unique`, and `Min` of one). Each runs at
the reference configuration and one more drawn from the self check's: threads {1, 4,
8}, batches of {1, 2, 3, 7, 64, 1024, 65536} rows (one under 64 rows nine times as
long for a frame past 17 rows),
the optimizer on and off. Before and after: the same query with
`bitmap.MaterialiseZeros` set and `ursus.CutWholeBatches()`, the bits stored and every
batch cut as before step 529, two switches for this differential, never set in a
query. The frame `Collect` answers, `Count` and `CollectBatches` stacked must be
identical, in order where the query promises one, the same refusal where it refuses,
and agree with each other. 1,008 answers; every query answers rows somewhere, but
the two whose rows need a Null column to hold a value.

**The batch-size contract** (`TestNullBatchesAreCutForOtherOperators`): three
records of 50,000 rows of two Null columns, scanned from arrow-go's records, an IPC
stream and a frame in memory, under a row index and a udf over it that records the
batches it is handed, with nothing below the row index, or a Select, a Head, a Slice,
a Concat, a Filter or a WithColumns, on one thread and four; and a udf in a `Select`
over the scan itself, a projection of an expression: no batch is longer than the batch
size, 1,024.

**Changed:** `TestNBytesCountsEveryPayloadSlot` pinned `NewNull`'s bitmap ("its
validity bitmap is materialised, not all-set"); it holds the opposite now, no bytes
at 1, 1,000 and 2^40 rows of Null and of Int64, and a column of nulls charged its
payload alone; its nullable column's bitmap is built.

**qcheck self**, 20 s from seed 529000, under the lock: 661 cases, 6,631 runs, 1,983
comparisons, 0 findings, the reference refusing none; every operation fed rows and
answered some. Its tables hold no Null column (above); its typed null literals,
`IsNull` and `FillNull` run over the all-clear form, which `NullColumn`'s validity is
now. qcheck's own tests pass (`go vet ./...`, `go test ./...`), as it imports the root.

## 4. Measurements

One targeted run each, six runs of which the first is dropped, the median and the
allocation of one, on the branch and on `origin/master` (`73f9863`), the same test
file, `GOEXPERIMENT=simd GOAMD64=v3 GOMAXPROCS=4`:

| | `origin/master` | this branch |
| --- | --- | --- |
| `Count`, a stream of 2^27 Null rows (232 bytes) | 22.3 ms, 25.4 MB | 61 µs, 0.01 MB |
| `Collect`, the same | 93.1 ms, 65.9 MB | 43 µs, 0.01 MB |
| `Count`, an IPC file of 2^20 rows of Int64, Float64 and String, no Null column | 7.9 to 10.9 ms, 34.4 MB | 5.5 to 5.8 ms, 24.7 MB |
| an ordinary query on a frame with no Null column: 2^20 rows from arrow-go's records, a filter on a Float64 with a tenth null, a group-by of five keys, Sum, Max, Len | 24.4 ms, 70.20 MB | 26.1 ms, 70.20 MB |

The first two rows and the last were measured first, on a quieter laptop; the IPC Count row is from
a later pair of runs, master and branch back to back, twice, with other agents' tests
loading the laptop (load average 10 to 28): there master's Count of 2^27 Null rows took
79 to 142 ms and its Collect 221 to 396, the branch's 127 to 187 µs and 92 to 169 µs;
the ordinary query 74 to 108 ms on master and 61 to 130 on the branch. The ordinary
query allocates the same bytes to the byte; its time is within the noise either way,
and nothing in it reaches the new form but `IsAllSet`'s extra comparison and the
combinators' extra cases.

## 5. Fuzz runs

Each target alone, under the shared lock, four workers, `-fuzzminimizetime=3s`, 20 s,
from a binary built with `go test -c -fuzz=FuzzScanIPC`, run under `prlimit
--as=4294967296`, with the four new seeds of 2^40 rows among the corpus and the
`oversized` skip narrowed, so they ran:

| target | baseline | inputs executed | new interesting | |
| --- | --- | --- | --- | --- |
| `FuzzScanIPCFile` | 46 | 805 | 23 | clean |
| `FuzzScanIPCStream` | 40 | 255 | 21 | clean |

Few executions: the laptop was loaded by four other steps' tests (load average 10 to
28). A first run, from a binary built without `-fuzz` (no coverage guidance), stopped
each target within 5 s on the finding of §6, on master too; the run above sets that
class aside (a script moves such an input out of `testdata` and resumes the target), and
met none.
Nothing it saved was kept.

## 6. A new finding, not this step's: an IPC buffer cut short reads as the end

The first fuzz run of each target found, in 4 to 5 s, an input that ursus reads as an
empty frame, no error, where arrow-go fails; both fail so on `origin/master`, and
neither is of this step's class:

```
FuzzScanIPCFile:   ursus reads 0 rows of 2160 bytes arrow-go reads as: arrow-go panicked: EOF
FuzzScanIPCStream: ursus reads 0 rows of 1608 bytes arrow-go reads as: arrow/ipc: unknown error while reading: EOF
```

The file is one of Polars' compressed `polars_null` files, mutated: a buffer whose body
is shorter than it declares, so arrow-go's `ipcSource.buffer` panics with `io.EOF`
from its `io.ReadFull`. The stream's reader returns an error wrapping `io.EOF`. Each
reaches the engine as an error that `errors.Is(err, io.EOF)`, which it takes for the
end of the stream: `arrowsrc` checks `!errors.Is(readErr, io.EOF)` on the reader's
error, and the file's panic is recovered as an error wrapping its value. `Collect`
and `Count` of either answer 0 rows and no error. It is the IPC twin of step 521's
F12, a Parquet page short of its bytes, fixed there by `truncated` making such an
error `io.ErrUnexpectedEOF`; the same few lines in `arrowsrc`, its reader's error and
a recovered panic, would fix it here. Not this step's scope, so left: the inputs are
at `/home/beck/ursus-scratch-archive/step529-findings/`, not committed, and the fuzz
runs below set this class aside to fuzz past it.

## 7. Teeth

Each mutation made in the file named, the tests run, and the file put back from a
saved copy, its SHA-256 checked, never by git. The readfuzz test runs
from a binary built with `go test -c`, under `prlimit --as=4294967296`.

| mutation | what fails | |
| --- | --- | --- |
| T1 `Zeros` materialises its bits, as before | `TestZerosReadsAsStoredZeros` (`IsAllClear` false), `TestCombinatorsOverEveryForm` (the forms), `TestZerosAllocatesNothing` and data's nbytes test: out of memory at 2^40 bits; `TestNullRecordsReadInConstantSpace`: "cannot allocate 137443147776-byte block" | **bites** |
| T2 `Get` of the all-clear form answers set | the bitmap tests ("Zeros of 1 bits: bit 0 = true"); the differential, every query of a Null column | **bites** |
| T3 `And` takes the all-clear form for all set, as `buf == nil` did | `AndMulti` of `AllSet` with `Zeros`; the differential, "WithColumns of n" at every frame of rows | **bites** |
| T4 `AppendSetPositions` takes an all-clear b for all set | "AppendSetPositions of 1 bits, AllSet with Zeros: [0], want []" | **bites** |
| T5 `Buffer` of the all-clear form is nil, as the all-set form's | "Zeros of 0 bits: Buffer <nil>"; the differential passes, as no query of it spills or exports a typed column of nulls | **bites** (bitmap test) |
| T6 `Or` takes the all-clear form for all set | "Or of 1 bits, Zeros with Zeros: bit 0 = true"; the differential passes | **bites** (bitmap test) |
| T7 `Words` of the all-clear form yields ones | "PopCount = 1, want 0 (Words iterator is wrong)"; the differential: "Filter n is not null … Count 1 and 1 rows, and before Count 0", "Select null tests" | **bites** (a first spelling did not build, unused variable; redone) |
| T8 `concatColumnN` builds a Null column's validity | `TestNullRecordsReadInConstantSpace`: "cannot allocate 274882101248-byte block", the Concat | **bites** |
| T9 arrowout shares a Null column's validity | the same, at `Record`: 137443147776 bytes | **bites** |
| T10 the planner never sets `ScanSpec.Whole` | the same: "runaway allocation: 304.4 MiB and counting, 4 times the bound of 79323136", the 2^40 record's `Count` | **bites** |
| T11 every node passes `Whole` on | `TestNullBatchesAreCutForOtherOperators`: "records, a row index over a Head, 1 threads: the udf saw 120000 rows, in batches of up to 50000" | **bites** |
| T12 a projection of any expression passes `Whole` on | first: passed, the test held only operators above a row index; the case of a udf in a `Select` over the scan added, then: "memory: a udf in a Select saw batches of up to 150000 rows, at a batch size of 1024" | **bites** (after the test was strengthened) |
| T13 arrowsrc cuts at the batch size whatever it is asked | the readfuzz test: runaway allocation, 305.1 MiB, the 2^40 record's `Count` | **bites** |
| T14 memsrc cuts at the batch size whatever it is asked | the same, 302.9 MiB, `Count` of the frame in memory | **bites** |
| T15 `oversized` skips a record of Null columns again | `TestOversizedPassesCleanInputs`: the four 2^40 inputs "skipped as oversized" | **bites** |

15 of 15 bite, T7 in its second spelling, T12 once its test held the case. T5 and T6
are caught by the bitmap tests alone.

## 8. Checks

After the last edit of code, on the branch off `73f9863` (PR #80, step 514, merged):

- `GOFLAGS=-p=1 GOMAXPROCS=4 GOEXPERIMENT=simd GOAMD64=v3`: `go test ./internal/...`
  ok, 29 packages; `go test .` in full, under the lock, ok (554 s).
- `cd qcheck && go vet ./... && go test ./...`: ok; `qcheck self -duration 20s`, 0
  findings (§3).
- `GOEXPERIMENT=simd go vet ./...`: clean. `go run ./internal/gen/levels`: ok.
- `GOEXPERIMENT=simd go run ./internal/gen/api -check`: 2,481 features, 92 added and
  0 gone since v0.7.2, as on master: no public API added. `bitmap.View`'s
  `IsAllClear`, `bitmap.MaterialiseZeros`, `ScanSpec.Whole`, `source.NoData`,
  `physical.Options.Whole` and `files.Source.RowsOnly` are internal;
  `ursus.CutWholeBatches` is in `export_test.go`.
- `go run ./internal/gen/testinventory -check`: 1,223, the baseline; the new tests'
  expected values are arrow-go's reading, the model of the bits, or the answer before.
- `gofmt -l .`: empty.
