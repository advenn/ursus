# Step 522 — as built

**An Arrow IPC file or stream with a Null column reads.** The first finding of §6 of
[`step-519-as-built.md`](./step-519-as-built.md): every Arrow IPC file and stream
holding a column of the Null type, top-level or nested, was refused, whoever wrote it.
Polars writes an all-null column as Null, so the files of the most common writer of
ursus's inputs were refused often; pyarrow's were, and so were ursus's own `WriteIPC`
and `WriteIPCStream` of a frame with a Null column. A Null array of no buffers is now
given the nil buffer before arrow-go's `array.Validate`, and every other array is
validated as it was.

```
Polars 2.1: pl.DataFrame({'a': [1, 2, 3], 'n': [None, None, None]}).write_ipc(...)
ursus ScanIPC(...).Collect -> ursus: arrow: column "n": the Arrow data is malformed
                               caused by: arrow/array: expected at least 1 buffers for null, got 0
```

## 1. The cause

The IPC format writes a Null column as a field node, its length and null count, and no
buffer; Polars 2.1, pyarrow 25.0.1 and arrow-go all write it so (each file's record
batch walked: `nodes=[(3, 3)] buffers=0`). arrow-go v18.7.0's IPC reader builds it as
a Null array of no buffers (`loadNull`, `arrow/ipc/file_reader.go`), and an extension
stored as Null the same. Its builders, `array.NewNull` and `array.Validate` give a
Null array the one buffer the Null layout declares (`NullType.Layout`, one
`SpecAlwaysNull`), which must be nil. `internal/arrowin`'s `validate`, which every
array ursus imports passes, IPC's, `ScanArrowRecords`' and `ScanArrow`'s, called
`array.Validate`, which counts a layout's buffers first: "expected at least 1 buffers
for null, got 0". Validate recurses into children and a dictionary, so a List, a
LargeList, a ListView or a FixedSizeList of Null, a Struct with a Null field, a Map
with Null values and a Dictionary of Null values were refused as well.

Only an input of no record batch read: a schema alone, as Polars writes a file of no
rows, pyarrow a file or a stream of none, and ursus's writers a frame of none. Polars'
stream of no rows holds a batch of no rows, and was refused.

## 2. The fix (`internal/arrowin/record.go`)

`validate` hands `array.Validate` the array itself, unless a Null array of no buffer is
in it; then a copy (`nullShaped`):

- `shapeNulls` walks the array's data, its children and its dictionary. Where it finds
  an array whose layout is Null's (`alwaysNull`: the Null type, or an extension stored
  as it) holding no buffer, it copies that array with the one nil buffer, and copies
  each array on the path above it with the copied child or dictionary in place. A
  copy keeps the type, length, null count (as stored, not computed), offset, buffers,
  other children and dictionary of the array it copies; a sibling is shared, not
  copied.
- Where there is none, `shapeNulls` returns the data itself and allocates nothing
  (`TestShapeNullsCopiesOnlyThePathToANullOfNoBuffer`, `testing.AllocsPerRun` 0), so
  every other array is validated exactly as before, the same `*array.Data` handed to
  the same call.
- The copy is built with `array.MakeFromData` and released after Validate. arrow-go
  built the original from the same buffers, so the copy's construction cannot panic
  where the original's did not; a panic is still recovered as the data's error.

Only a Null array of *no* buffer is reshaped: one with a buffer that is not nil, with
two, or with a child is refused by Validate as before, and an array whose own shape is
wrong above a Null child of no buffer still is (`TestOtherwiseMalformedNullsAreRefused`:
offsets too few, an offset past the child, a fixed-size list longer than its child, a
struct of more nulls than rows). Validate itself normalises a Null array's null count
to its length when it wraps it (`Null.setData`), as it always did, so a declared count
is not checked, before or after.

The builders read a Null array as its row count (`nullBuilder`) and never its
buffers, so nothing past validation changed.

## 3. The tests

**The differential** (`TestIPCNullColumnsRoundTrip`, `ipcnull_test.go`). Six seeds;
each draws a frame of 0, 1 and 300–2,300 rows: the row `i` and a Null column `n` in
every frame, and, each one time in two, a second Null column, Int64, Float64, String
and Boolean columns with nulls, a List(Null), a List(List(Null)), a Struct(z: Null,
y: Int64), a Struct(z: Null), an Array(Null, 2) and a Map(String, Null), in an order
drawn too; built by arrow-go's `FromJSON` and read by `ScanArrowRecords`, whose Null
arrays hold the nil buffer. Each is written by `WriteIPC` and `WriteIPCStream` in each
codec, at the default batch size and at 1–97 rows a batch, and each file and stream
is read back through every entry point (`ipcEntryReads`): `ScanIPCBytes`, `ScanIPC`,
`ScanIPCFiles`, `ScanIPCGlob`, `ScanIPCFS` and `ScanIPCFrom` of the file, its
`ReadIPCSchema`, and a projection of its Null column; `ScanIPCStreamBytes`,
`ScanIPCStream`, `ScanIPCStreamFS`, `ScanIPCStreamFrom` and `ScanArrow` of the
stream; and arrow-go's records of either handed to `ScanArrowRecords`. Every read is
the frame written (`ursustest.AssertFrameEqual`), 108 subtests. Before the fix every
frame of rows was refused; a frame of no rows read, ursus writing it as a schema and
no record batch.

**Polars' and pyarrow's files** (the `ipcnull` group). `bench/scripts/oracle/record_ipcnull.py`
writes 21 files into `testdata/ipc` (42 KB), the same bytes on every run (each writer
run twice, every SHA-256 the same):

| writer | files | what |
| --- | --- | --- |
| Polars 2.1.0 | `polars_null{,_lz4,_zstd}.arrow{,s}` | five rows: `a` Int64, `n` Null, `s` String, `l` List(Null), `st` Struct(z: Null, y: Int64), `arr` Array(Null, 2), a file and a stream in each codec |
| | `polars_null_empty.arrow{,s}`, `polars_null_only.arrow{,s}` | the frame of no rows; a frame of only a Null column, four rows, whose record batch holds no buffer at all |
| | `polars_null_batches.arrow` | the frame in record batches of two rows; `write_ipc_stream` writes a frame holding a Null column as one batch, whatever its chunks |
| pyarrow 25.0.1 | `pyarrow_null{,_lz4,_zstd}.arrow{,s}` | three rows: `a` int64, `n` null, `l` list<null>, `ll` large_list<null>, `st` struct<z: null, y: int64>, `fsl` fixed_size_list<null>[2], `m` map<string, null>, `d` dictionary<int32, null> |
| | `pyarrow_null_empty.arrow{,s}`, `pyarrow_null_batches.arrow{,s}` | no rows (the stream holds its schema alone); batches of one row |

pyarrow writes a Null column as Polars does, a node and no buffer: no writer gives it
a buffer. `bench/scripts/oracle/ipcnull.py` records Polars 2.1.0's `read_ipc` or
`read_ipc_stream` of each, 21 cases and no refusal, in `testdata/oracle/ipcnull.json`
(`make oracle ORACLE_PYTHON=… GROUPS=ipcnull`). Polars 2.1 reads pyarrow's map as its
Map type, where ursus reads an Arrow map as the `List(Struct(key, value))` it is on
disk (`feature-gaps.md`); those cases cast it so, and say why. `oracle_ipcnull_test.go`
reads each file through every entry point and arrow-go's records (`ipcEntryReads`),
each the same frame as `ScanIPC` or `ScanIPCStream`, which is compared with the
recording. Before the fix 18 of the 21 failed, "refused where Polars answers"; the
three that passed hold no record batch.

**arrowin** (`internal/arrowin/nullarrays_test.go`). `TestANullArrayOfNoBuffersReads`:
a record of ten columns, a Null array in each (a Null column, a List's, a
LargeList's, a ListView's and a FixedSizeList's elements, a Struct's field, a Map's
values, a Struct's field in a List, a Dictionary's values and an Opaque extension's
storage), written by arrow-go as a file and a stream in each codec and read back by
it; every column read holds a Null array of no buffer, passes the Checker, and is read
as the record written is read (arrowout's export, `array.Equal`).
`TestOtherwiseMalformedNullsAreRefused` is above, §2.

**The fuzz targets' oracle** (`internal/readfuzz`). `checkIPC` holds a frame ursus
reads to arrow-go's own reading, which validated each column with `array.Validate`
and so refused what ursus now reads; it validates through `validated`, its own copy
of the rule, written apart from arrowin's, and `cellOf` reads a Null array's cell as
null, which arrow-go's `IsNull` does not say of an array of no bitmap. The 21 files
are seeds of `FuzzScanIPCFile` and `FuzzScanIPCStream` (`ipcSeeds` globs
`testdata/ipc`) and clean inputs of `TestTheWalkPassesCleanInputs`, now 26 files and
21 streams, each read as arrow-go reads it cell for cell; `everyType`, ursus's writing
of every type it has, holds a Null column and a struct of a Null field again. A file of
no rows by its name is let through that test's check that some rows were read.

## 4. The other entry points

- **`internal/arrowmeta`'s walk** passes a Null field of no buffer in files and
  streams. Its generator already made Null columns, at any depth, and
  `TestTheWalkPassesWhatArrowGoWrites` now counts them and fails on none: 177 files
  and 177 streams of the 3,600 hold one, each read through the walk record for record
  as arrow-go reads it alone. It never failed on them, since that test compares
  records with `array.RecordEqual`, not Validate.
- **`ScanArrowRecords` and `ScanArrow`** validate through the same `Checker`, so
  arrow-go's IPC records handed to either were refused the same way, and now read
  (the differential and the group read both).
- **The C data interface:** ursus has none.
- **The Parquet reader** reads its own pages and never passes arrowin's `validate`:
  Polars 2.1's and pyarrow 25.0.1's Parquet files of a Null column, a List(Null) and a
  Struct(z: Null, y: Int64), its footer's `ARROW:schema` among them, read before the
  fix and after, each the frame written.

## 5. A huge declared Null row count is costlier now

Step 511's finding of 2^40 rows of no data stays the maintainer's (§5 of
[`step-511-as-built.md`](./step-511-as-built.md)), but this fix moves it. Before, a
well-formed stream of a Null column declaring any number of rows was refused at its
first record batch, in under half a millisecond. Now it is read as its rows: ursus
holds a Null column as a validity bitmap of zeros, a bit a row, made a batch at a time.
A stream of one Null column of 2^27 rows, 232 bytes written by arrow-go:

| | before | after |
| --- | --- | --- |
| `Count` | refused, 0.4 ms | 2^27, 16.5 ms, 26 MB allocated |
| `Collect` | refused, 0.04 ms | 2^27 rows, 77–93 ms, 89 MB allocated |

At 2^40 rows, by the same rate, `Count` would take about two minutes and allocate
about 200 GB over its run, and `Collect` would need 128 GiB for the bitmap. A record
of no columns already read so (`Count` of 2^27 rows of no columns, 1.2 ms), which was
step 511's case; a Null column now joins it. The fuzz targets still skip a record
batch declaring more than eight rows a byte of input (`oversized`), and
`TestOversizedPassesCleanInputs` holds that skip to none of `testdata/ipc`'s files, the
21 among them.

## 6. Fuzz runs

Each target alone, under the shared lock, four workers, `-fuzzminimizetime=3s`, 20 s,
from a binary built with `go test -c -fuzz=FuzzScanIPC`, run under `prlimit
--as=4294967296`, the 21 files among the seeds.

| target | baseline (Go's cache and the seeds) | inputs executed | new interesting | |
| --- | --- | --- | --- | --- |
| `FuzzScanIPCFile` | 843, 11 s | 1,378 | 12 | clean |
| `FuzzScanIPCStream` | 794, 6 s | 4,102 | 15 | clean |

Nothing they saved was kept.

## 7. Teeth

Each mutation made in `internal/arrowin/record.go`, or in `internal/readfuzz/ipc_test.go`
for the last two, the tests run, and the file put back from a saved copy, its SHA-256
checked, never by git.

| mutation | what fails | |
| --- | --- | --- |
| the old `record.go`, restored whole | `TestIPCNullColumnsRoundTrip`: 72 of 108 subtests, every frame of rows ("column "n": the Arrow data is malformed … expected at least 1 buffers for null, got 0"); `TestOracle/ipcnull`: 18 of 21, "refused where Polars answers"; `TestTheWalkPassesCleanInputs`: "polars_null.arrow (file true) is refused"; arrowin's tests do not build, `shapeNulls` undefined | **bites** |
| `validate`'s old body, the helpers kept | `TestANullArrayOfNoBuffersReads`: column "null" malformed; `TestOtherwiseMalformedNullsAreRefused`: a Null array with a child refused for its buffers, not its child | **bites** |
| only a top-level Null array reshaped, no child or dictionary | the shape test, "a struct of a Null field of no buffer was not copied"; arrowin's column "list"; 72 of 108 differential subtests (fields `z`, list values, a map's values); 16 of 21 oracle cases (the two of only a Null column pass, and the three of no record batch); the clean inputs, column "l" | **bites** |
| a dictionary's values not reshaped | arrowin's column "dictionary"; 8 oracle cases, pyarrow's column "d" in each file of rows; the clean inputs, `pyarrow_null.arrow`. The differential passes: ursus writes no dictionary of Null | **bites** |
| an extension's storage not unwrapped | arrowin's column "extension" | **bites** |
| every Null array given the nil buffer, whatever it holds | "a Null array with a buffer: accepted", "… with two buffers: accepted"; the shape test, a Null array with the nil buffer copied, 3 allocations | **bites** |
| the copy's null count 0, not the array's | "a struct of more nulls than rows, over a Null field of no buffer: accepted" | **bites** |
| `cellOf` without its Null case | `TestTheWalkPassesCleanInputs` and `FuzzScanIPCFile`'s seeds: "column "n" (Null): ursus reads <nil>, and arrow-go holds uncompared" | **bites** |
| arrow-go's reading validated by `array.Validate` again | the same two: "ursus reads 5 rows of 2144 bytes arrow-go reads as: … expected at least 1 buffers for null, got 0" | **bites** |

9 of 9 bite.

## 8. release-0.7

`release-0.7` has the bug: its `internal/arrowin/record.go` is master's before this
step, byte for byte, and its build refuses `polars_null.arrow` and
`pyarrow_null.arrows` with the same error. A backport needs `record.go`'s change as it
is, `nullarrays_test.go` and `nullshape_internal_test.go` (their helpers are on the
branch), the 21 files, and the differential cut to the entry points 0.7 has
(`ScanIPC`, `ScanIPCFiles`, `ScanIPCGlob`, `ReadIPCSchema`, `ScanIPCStream`,
`ScanArrowRecords`, `ScanArrow`; no `ScanIPCBytes`, `ScanIPCFrom`, `ScanIPCFS` or
their stream kin). 0.7 has neither the recorded oracle of step 496 (its
`oracle_test.go` is step 184's expressions) nor `internal/readfuzz` nor
`internal/arrowmeta`, so the files' expected frames would come from arrow-go's reading
of each, cell for cell, or from `ipcnull.json` read by a loader of its own. Not done
here.

## 9. Checks

After the last edit, on the branch off `de826b9` (PR #72, step 519, merged), and again
after master was merged in (step 521, PR #75; a conflict in this index only):

- `GOFLAGS=-p=1 GOMAXPROCS=4 GOEXPERIMENT=simd GOAMD64=v3`: `go test ./internal/...`
  ok; `go test .` in full, under the lock, ok (207 s).
- `GOEXPERIMENT=simd go vet ./...`: clean. `go run ./internal/gen/levels`: ok.
- `GOEXPERIMENT=simd go run ./internal/gen/api -check`: 0 gone; no public API added.
- `go run ./internal/gen/testinventory -check`: as the baseline, 1,225; the new tests
  take their expected frames from the recording, the frame written, or arrow-go.
- `gofmt -l .`: empty.
