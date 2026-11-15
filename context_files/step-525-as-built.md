# Step 525 — as built

**An Arrow time past its day is refused, not wrapped.** CI's fuzzing of
`FuzzScanIPCFile` on PR #77 (run 43665022761) found that Arrow import wrapped every
Time32 and Time64 into [0, 24h): an IPC file holding 26:00 read as 02:00, with no
error. Import now refuses a time outside its day as corrupt, at any depth, by the rule
the Parquet reader has used since step 510. The two readers share that rule now,
`data.FindOutsideDay`.

## 1. The finding

```
row 7, column "time64" (Time(ns)): ursus reads 24430692204799, and arrow-go holds 1751093230692204799
```

The input is an IPC file of 15,794 bytes, 33 columns in two record batches of 4 rows,
the readfuzz kitchen sink mutated. Row 3 of the second batch, row 7 of the frame,
holds the Time64(ns) 1,751,093,230,692,204,799, about 20,267 days. `internal/arrowin/builder.go`'s `times` read every Time32 and Time64 through
`wrap := func(v int64) int64 { return (v%perDay + perDay) % perDay }`, so ursus read
24,430,692,204,799 ns, 06:47:10.692204799. The comment said "as Cast does: nothing in
arrow-go range-checks a time". The wrap came in with the copy itself, in step 60,
where "no Time wrap" was one of that step's teeth. It reached every Arrow import:
`ScanArrowRecords`, `ScanArrow`, and the `ScanIPC*` family, whose records arrowin
copies.

## 2. Why it is a bug by ursus's own rules

- **A Time is a time of day,** [0, 24h) in its unit (`dtype`), and every kernel
  assumes it (`data.CheckTimeRange`). A wrapped value is a plausible wrong answer.
  It renders as a time of day, sorts after 23:00:00, and equals no genuine
  02:00:00.
- **The Parquet reader refuses one.** Step 510 made it refuse a TIME past its
  day as a corrupt value, at any nesting depth (`checkTimes`), for exactly that
  reason.
- **Arrow export refuses one.** `internal/arrowout` refuses a time outside
  [0, 24h).
- **"As Cast does" was stale.** A strict `Cast` of 90,000 s to Time refuses with
  "time of day", and has since step 86 (`castedges_byhand_test.go`, audit S18). The
  wrap was written before that and never revisited.
- **Arrow's columnar format** says a Time's values lie within a day.
- **ursus refuses rather than guesses** (`step-506-as-built.md` §1).

## 3. Did anything public promise the wrap? No

| where | what it says of an Arrow time |
| --- | --- |
| godoc of `ScanArrowRecords`, `ScanArrow` | types are mapped, every column nullable, unsupported types refused by name; nothing on a time's range |
| godoc of `ScanIPC`, `ScanIPCFiles`, `ScanIPCGlob`, `ScanIPCFrom`, `ScanIPCBytes`, `ScanIPCStream`, `ScanIPCStreamFrom`, `ScanIPCStreamBytes` | "each column as ScanArrow converts one"; nothing on a time's range |
| `docs/guide/io.md`, "Arrow" | 38 of 45 types map; nothing on a time's range |
| `docs/guide/dates-and-times.md`, `docs/concepts/types-and-nulls.md` | `TimeOf(unit)` is "a time of day" |
| `CHANGELOG.md` | "Time ± Duration wraps around midnight (57)", which is arithmetic, not import; "A Parquet TIME past its day is refused (510)" |
| `README.md` | nothing |

Only internal text described the wrap:

- the comment in `builder.go`;
- the test `TestTimeIsWrappedIntoTheDay`, which pinned it with hand-typed values;
- step 60's teeth table.

So the documented behaviour, a Time being a time of day, stands. The fix makes
import keep it, and is not a break under step 494's rule.

## 4. The fix

- **`data.FindOutsideDay(c)`, new in `internal/data/timeday.go`.** It finds a valid
  tick outside its day in a Time column, or in a List, Array or Struct holding one at
  any depth. It returns the Time column holding the tick, the row of `c` that holds
  it, the tick, and the day's ticks.
  - Order: the least row holding any such tick; within a row, its first element;
    within an element or a row, its first field.
  - A List's elements are read within its window (`ListAccessor.Window`), so a
    sliced List is right too. Each element maps to its row by a binary search over
    the offsets.
  - A type holding no Time costs a walk of the type and nothing more.
- **Arrow import refuses.** `arrowin.column`, which `Batch` and `Column` both use,
  calls `checkTimes(c, lo)` once the column is built. The refusal is `KindValue`,
  as Parquet's is:

  ```
  arrow: column "time64" holds the Time(ns) 1751093230692204799 in row 3 of its record, outside [0, 86400000000000)
    the data is corrupt: an Arrow time counts from midnight and is less than a day
  ```

  The row is the record's row, the array's row from the window's start. The stream
  readers do not number their records. `ScanArrowRecords` adds `record i:` in
  front, as it does for every refusal. Because the check runs on the built column,
  it covers every path into it: plain, dictionary-encoded and extension arrays, at
  the top level and nested.
- **`times` copies the ticks as they lie.** A Time64's are copied with `same`, a
  Time32's are widened. The stale comment is gone, replaced by one that says why.
  The package doc gains a paragraph: where arrow-go is looser than Arrow, a value
  the format rules out is refused, not normalised (Date64, and now a time).
- **Parquet's `checkTimes` calls `data.FindOutsideDay`.** Its message, kind and hint
  are byte-identical. A file holding times outside the day in two fields of one
  Struct may now name the field of the earlier row rather than the first field;
  either way the read is refused.
- **Tests.** `TestTimeIsWrappedIntoTheDay` becomes `TestATimeOutsideItsDayIsRefused`.
  It covers a time before midnight, the next midnight's tick, 26:00, a Time64(ns)
  before midnight, and one in a List and in a Struct, each refused with its row and
  value. It also covers a window whose row is the array's, and a null slot, a null
  list's range and a null struct's field holding junk, each read. The testinventory
  baseline loses the old test's 2 lines (`-write`; 1,225 → 1,223).
- **The seed.** CI's input is kept as
  `internal/readfuzz/testdata/fuzz/FuzzScanIPCFile/time64-past-its-day`. With the
  fix, ursus refuses the file, so `checkIPC` has no frame to compare and the seed
  passes. With the wrap back, it fails as CI did (§7).

## 5. The differential

`internal/readfuzz/arrowtime_test.go`, `TestArrowTimesOutsideTheDay`. Its oracle is
arrow-go's arrays. Each case draws a fresh set of records, under seed 525 and a
per-case stream:

- **Types and shapes.** Time32(s), Time32(ms), Time64(us) and Time64(ns), each at
  the top level, in a List, in a Struct of two Time fields, and in a List of such
  Structs. 24 cases each, 384 cases in all.
- **Records.** 1 to 3 records per case, each of 1 to 12 rows. Each record is a slice
  of a larger one, with up to three rows before and after it.
- **Values.** In-day values are drawn from 0, the day's last tick and anywhere
  between. Out-of-day values are drawn from -1, the next midnight's tick, the
  integer's two ends and anywhere between.
- **Three modes, in turn:**
  - every value in the day, with junk where no value is;
  - every value in the day, and no junk;
  - 15% of values outside the day, plus junk.
- **Junk.** In half the places that hold no value, a time outside the day. Those
  places are: null slots, the ranges null list rows own, the valid fields of null
  structs, and the rows past a slice. Arrow allows all four.

Every case is read six ways: `ScanArrowRecords`, `ScanArrow` over
`array.NewRecordReader`, `ScanIPCBytes` and `ScanIPC` of an IPC file written by
arrow-go, and `ScanIPCStreamBytes` and `ScanIPCStream` of an IPC stream. Each read
is held to one of two answers:

- **No value outside the day:** a frame of arrow-go's cells (readfuzz's `cellOf`
  and `sameValue`), row for row across the records.
- **Otherwise:** an `ErrValue` naming column `"t"`, the first such value and its
  row in its record. "First" is the record order, then the row, then a row's
  elements, then a struct's fields. `ScanArrowRecords` must also name `record i:`.

Of the 384 cases, 132 read with no junk, 153 read past junk, and 99 are refused. An
anti-vacuity check needs every type and shape to have run all three kinds. The test
takes 0.11 s.

## 6. The fuzz runs

Each target ran alone, under the heavy lock, with these settings:

- `prlimit --as=4294967296`;
- `GOFLAGS=-p=1 GOMAXPROCS=4 GOEXPERIMENT=simd GOAMD64=v3`;
- `-fuzztime 20s -fuzzminimizetime=3s`.

| target | baseline | execs | new interesting | result |
| --- | --- | --- | --- | --- |
| `FuzzScanIPCFile` | 845 | 6,440 | 15 | PASS, no finding |
| `FuzzScanIPCStream` | 799 | 11,289 | 14 | PASS, no finding |

## 7. The teeth

Each mutation was put in from a saved copy and taken out the same way, never by git.

| mutation | what fails |
| --- | --- |
| the wrap put back in `builder.go` (master's file), with the check kept | the seed, exactly as CI saw it: `ursus reads 24430692204799, and arrow-go holds 1751093230692204799`. The differential: `time32[s] top level, case 5, ScanArrowRecords: record 0's row 3 holds -1, outside [0, 86400), and was read`. All seven of `TestATimeOutsideItsDayIsRefused`'s refusals: "accepted" |
| the check removed (master's `record.go`), no wrap | the seed and the differential: at the top level, data's boundary check, which is on in readfuzz, answers `ErrInternal` ("a Time is a time of day and 23:59:59 is not one"), where an `ErrValue` is wanted. All seven of `TestATimeOutsideItsDayIsRefused`'s refusals: "accepted" |
| a List's element index reported as its row | the differential: `time32[s] in a List, case 2`, want row 7, got row 11. The List case of `TestATimeOutsideItsDayIsRefused` fails too |
| a Struct's first field winning over its least row | the differential: `time32[s] in a Struct, case 8` names another value and row |
| a null slot's tick judged (no `IsValid`) | nothing. This is an equivalent mutant: `fixedBuilder.appendNulls` zeroes every null slot before the check sees the column, and the Parquet reader writes nulls the same way. The differential's 153 cases that read past junk hold the end-to-end behaviour |

## 8. Checks

After the last edit:

- `go test ./internal/...`;
- `go test .` in full, under the lock;
- `GOEXPERIMENT=simd go vet ./...`;
- `go run ./internal/gen/levels`;
- `GOEXPERIMENT=simd go run ./internal/gen/api -check`, 0 gone;
- `go run ./internal/gen/testinventory -check`;
- `gofmt -l .`, empty.

## 9. Left

- **The record's place in an error from a stream reader.** `ScanArrow` and the
  `ScanIPC*` family name the row within its record but not which record.
  `ScanArrowRecords` names both. Numbering records in `arrowsrc` would help every
  arrowin refusal, not only this one.
- **release-0.7 has the same wrap.** Backporting this fix needs `data.FindOutsideDay`
  and, there, step 510's `checkTimes`.
