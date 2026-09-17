# Step 60 — as built

**Arrow import.** `ScanArrow(open)` reads any `array.RecordReader` — an IPC stream,
Flight, the C Data Interface — and `ScanArrowRecords(recs...)` reads records already in
memory. Both **copy**. With step 58's export, Arrow now goes both ways.

Seven commits, the last of which carries this document. The first fixed the test
helper every import test would have leaned on, because it could not tell two nested
frames apart.

Authoritative where it disagrees with [`step-58-as-built.md`](./step-58-as-built.md)
(whose §4 and §6 sketch an `Adopt` with `Retain` and `runtime.AddCleanup`), the vision
docs and [`design/`](./design/).

`make test-all` exit 0 (**100** package-ok lines = 20 packages × 5 SIMD configurations),
`make race` exit 0 (20), `make levels` and `go vet` clean in all three modules, PDS-H
SF=0.1 exit 0 with **22/22** — each asserted on its own exit code.

---

## 1. `AssertFrameEqual` could not see inside a List or a Struct

`renderValue` ended in `return "<unrenderable " + type + ">"`, and List and Struct
both reached it. The placeholder is the same string for every row, so **two nested
frames holding different values compared equal**. With the placeholder restored, 7 of
the 9 cases in `ursustest/nested_test.go` pass that must fail: `[1,2]` against `[1,3]`,
elements moved across a row boundary, a different field, a list inside a struct. Only
the two cases involving a null row were caught, because `"null"` differs from the
placeholder.

List now renders through `Get`'s absolute ranges (so a `Tail`-sliced List prints only
its own rows), Struct names each field, elements go through `cell`, and an unrenderable
type is a fatal error rather than a string that equals itself.

Every existing call site that compares List columns now reaches the new arm — **140
renders** across the implode, split and reshaping tests — and **all of them still
pass**. None was hiding a wrong answer; they were simply not checking.

## 2. What shipped

```go
func ScanArrow(open func() (array.RecordReader, error)) *LazyFrame
func ScanArrowRecords(recs ...arrow.RecordBatch) *LazyFrame
```

| package | level | what |
| --- | --- | --- |
| `internal/arrowin` | 25 | `Type`/`Schema` (the map), builders (the copy), `Checker`, `Batch`, `Column` |
| `internal/source/arrowsrc` | 45 | the stream source: schema once, a window at a time |
| `internal/arrowx` | 0 | `FieldTypeKey = "ursus:type"`, shared by export and import |
| `internal/arrowout` | 25 | `Field`, which marks an Int128 at every depth |

- **A factory, not a reader.** A scan is opened to plan the query and once per scan per
  execution — a self-join opens it twice in one query. The rule `ScanCSVReader` states
  for byte streams.
- **Always nullable.** `arrow.Field.Nullable` defaults to `false`, producers leave it
  unset while writing nulls, and arrow-go never checks it.
- **Int128** exports as `Decimal128(38, 0)`, exactly like `Decimal(38, 0)`. The field
  mark tells them apart; it survives IPC, and is honoured only on exactly
  `Decimal(38, 0)`.

## 3. Why a copy

Zero-copy is unsafe with today's types. `bitmap.View`, `StringAccessor` and
`ListAccessor` hold raw `[]byte` and cannot keep an owner alive. Strings read through
`Get` alias the buffer and reach user code. An IPC record is valid only until the
reader's next `Next`, and C Data memory is freed through the producer's release callback.

`design/foundation.md` §4.2's `runtime.AddCleanup(b, …)` on a `*Batch` fails for a
structural reason: `Batch.Slice` and every rename build objects that never reference
`b`, so the cleanup could release a record while a slice of it is still being read.
The superseding notes are in place in both design docs.

## 4. Arrow is looser than ursus, and each gap is a silent wrong answer

The design review found these before any code was written, and each was confirmed by
reading arrow-go and ursus:

| Arrow allows | ursus assumes | without import normalising it |
| --- | --- | --- |
| a null list row covering elements (pyarrow's `from_arrays` with a mask) | `listReduce`: a null list owns nothing | `list.sum` over a null row is a number |
| a null struct with valid fields (`StructBuilder.AppendValues`, pyarrow) | `struct.field` returns the field's own validity | a null person has an age |
| anything in a null slot | every producer zeroes it | kernels that read without checking see garbage |
| columns longer than their record | `arr.Len()` is the row count | extra rows |

All nulls are appended through one canonical `appendNulls` path, so a null row is
empty, zeroed and null in every field beneath it. That is the shape the Parquet reader
and `kernel.Take` already produce. `TestANullListOwningElementsSumsToNull` and
`TestANullStructsFieldIsNull` assert it through the public API.

Four more refusals exist because accepting would be silent:

- **A time zone `time.LoadLocation` cannot load**, such as `"+09:00"`. Every `.dt`
  function and temporal grouping fall back to UTC, so `hour` would be nine hours off.
- **A decimal scale outside `0..precision`.** Arrow's scale is an int32, and a negative
  one wraps to 254 in a uint8.
- **A struct with no fields**, since `NewStruct` takes its length from field 0, and
  **duplicate field names**, since fields are looked up by name.
- **A Date64 that is not whole days**, or out of int32 range — judged on valid rows only.

## 5. arrow-go traps, recorded so nobody walks into them

- **`String.ValueBytes()` is windowed; `ValueOffsets()` values are absolute.** Indexing
  one with the other reads the wrong bytes on any sliced array. The whole buffer is
  read instead. The same holds for LargeString, Binary and LargeBinary.
- **`Dictionary.Dictionary()` and `NullN()` write lazily into shared state.** Two scans
  reading one record would race. Dictionary values are built with `MakeFromData` and
  released; validity comes from the bitmap.
- **`Data().Dictionary()` returns a typed nil** inside a non-nil interface.
- **A released arrow-go record keeps its schema and drops its columns**, and a released
  array keeps its Go value with nil data. `Checker` refuses both, with a hint to Retain.
- **`array.NewRecordReader`'s `Release` empties it.** A factory closing over one reader
  therefore yields *no records and no error* on the second call — an empty frame. The
  source refuses a reader it has already returned.
- **`array.Validate` checks child types against the parent's declared type** and the
  first and last offsets; it does not check the offsets in between. The builders do,
  row by row.

## 6. The stream source's lifecycle

- `Schema` opens a reader, converts its schema, and releases it — once per source.
  A cancelled context is not cached; CSV's `sync.Once` caches it forever.
- **`Open` does no I/O.** `planJoin` opens the left side and does not close it when the
  right side fails to plan, so a source that opened in `Open` would leak. The reader
  opens on the first `Next`.
- One window of the current record per `Next`, and **the reader is not advanced until
  the record is fully copied**, so its "valid until the next Next" contract holds
  without a Retain. The reader is released as soon as the stream ends, not at `Close`.
- `MaxRows` stops the source asking for records: `Head(2)` over ten-row records reads one.

## 7. Teeth

| instrument | tooth | result |
| --- | --- | --- |
| nested render | restore the placeholder | **7 of 9 must-fail cases pass** — the defect, measured |
| | List arm returns `"[]"`; render the whole child; descend into a null struct | **bite** |
| Int128 mark | drop it on the struct-child, list-element or schema arm; mark decimals too | **all bite** |
| type map | delete Duration; refuse Float16; trust `Nullable`; any zone; mark on any decimal; duplicates; negative scale; zero fields | **all bite** |
| copier, seven shapes | validity without the data offset; fixed-size list without it; `ValueBytes` with absolute offsets; Decimal128 in Arrow's word order; struct fields at the struct offset again; fixed-width values without the offset | **all bite** |
| | whole-word fast path ignoring which validity the run holds | **did not bite** — every fixture was shorter than 64 rows; arrays spanning several words were added, and it bites |
| normalisation | copy null list ranges; skip the struct merge; copy null slots; no Time wrap; no int32 day check; absolute offsets instead of span; read every dictionary index; no offset or range checks | **all bite** |
| checker | no column count; no schema comparison; no array type comparison | **all bite** |
| **poisoning, eager** | alias an Int64 column's values instead of copying | **bites** — every row reads `0xDBDB…` |
| | the same bug with an allocator that does not poison | **passes** — so the poison is the teeth |
| | alias only the validity bitmap | **bites** — nulls in the wrong rows |
| public import | rows from the array, not the record; trust `Nullable` | **bite** |
| stream source | no reuse check; remember no readers; no probe release; no reader release; cache a cancelled context; projection in schema order; `Next` after `Close` allowed; ignore `MaxRows` | **all bite** |
| | remove the schema re-check at open | **did not bite** — the per-record `Checker` catches the same swap one step later; removing both bites |
| | remove only the `emitted >= max` check | **hangs**: zero-row batches forever. Re-aimed at ignoring `MaxRows` entirely |
| **poisoning, stream** | release the record after its first window; advance the reader before the record is copied | **bite — as a panic**, because a released arrow-go record drops its columns before any poisoned byte is read |
| | never release the reader | **bites** — releases 1 of 2, 896 bytes still held |
| | the factory bypasses the test allocator | **anti-vacuity fires** — nothing was poisoned |
| | ignore `MaxRows`, at the root | **did not bite** — the Limit above the scan stops pulling first; the source-level test is the instrument |

## 8. Deviations from the plan

- **No `conforms` function.** `array.Validate` checks child types, `Checker` compares
  each array with its field, and the converted schema must equal the planned one. That
  covers what a recursive type walk would, without a second copy of the type map.
- **Strings and lists finish with one extra copy** of their offsets and bytes
  (`NewStringParts`, `NewList`). Fixed-width columns write straight into the final
  buffer as planned.
- **Dictionary decoding is per index**, merging runs of consecutive indices. Correct,
  and slower than a gather for a heavily dictionary-encoded string column.

## 9. Documentation corrected

- `design/foundation.md` §4.2 and `design/physical.md` §4.5 carry superseding notes;
  the `Adopt`/`AddCleanup` sketch, `TestAdoptCleanupReleases`, the zero-copy
  `ScanArrow(rdr)` signature and the agent contracts that depended on them are marked
  where they stand.
- `internal/arrowx/alloc.go` said it was *"the only package in ursus that imports
  arrow-go"* — false since the column layer existed — and that import would need
  `Adopt` with a cleanup. Both corrected.
- `internal/data/unsafe.go` said every buffer is 64-byte aligned, which a slice is not;
  `nbytes.go`'s `BufferID` claim now states the fact it rests on — every payload is Go
  heap because import copies.
- `ursus-api.md`'s scan list and interop sketch now show what shipped; the README
  gains Arrow in its sources row and a short section; `ExampleScanArrowRecords` runs
  the round trip under `go test`.

## 10. Still open

- `planJoin` leaks the left operator when the right side fails to plan — CSV file
  handles today. The Arrow source sidesteps it; the planner still has it.
- A reader blocked inside `Next` cannot be interrupted by the query's context.
- `ScanArrowRecords` explains as `MEMORY SCAN`, which is what it is after the copy.
- No C Data helpers; the factory accepts any `RecordReader`, cdata's included.
- Zero-copy import, and Decimal256, intervals, unions and run-end encoding.
- Carried: `listSort`/`listUnique` whole-child work; `Datetime ± Duration` int64 wrap; the
  byte-flip spill sweep; `Optimizer.Verify` in goldens and `Explain`; UDF name
  uniqueness; List/Struct absent from the contract fixture; `list.mean`'s element type;
  weak `errors.Is` assertions; `Writer.Write`'s unwrapped error; `callCache` eviction;
  `Pivot`; a stale benchmark suite.
