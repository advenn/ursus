# Step 373 — as built

**A CSV file read faster:** a float parsed in one pass, an integer too, a batch's
validity built only where it has a null, and an unquoted field's end found in a mask of
sixty-four bytes. P5 of `v0.6.8-scope.md`.

## 1. How

Profiled on step 357's file, a million rows of an integer, two text columns and a
float, 43 MB, read on one thread: the float's parse was 30% of the read, the field
splitter 25%, the integer's parse 7% and the validity bitmaps 5%.

- **A float in one pass** (`dtype/numtext.go`): `ParseFloat` scanned a float twice,
  `floatSyntax` for the grammar and `strconv.ParseFloat` again. `exactFloat` now reads
  a plain decimal, `[+-] digits [. digits] [(e|E) [+-] digits]`, of at most 19
  significant digits whose whole-number mantissa is below 2^53 and whose power of ten
  is at most 22 either way: both are exact float64s, so one multiply or divide rounds
  once, and the answer is strconv's, which takes the same path for these (Clinger's).
  Anything else, `inf`, `1e23`, twenty digits, goes as it went. A cast from String
  asks the same function, so it gains too.
- **An integer in one pass** (`internal/source/csv/parse.go`, `parseInt`,
  `parseUint`): a sign and at most 18 digits, 19 unsigned, that fit the width,
  accumulated directly; anything else is `strconv`'s, its value and its error.
- **Validity in runs** (`validity`): a builder counts valid values and puts them in
  its bitmap only when a null comes, so a batch with no null builds none, and its
  column says it has no null without a bitmap of ones. It had one, which every kernel
  after the read took its slow path for: step 372's whole-column sums and extremes
  ask `IsAllSet`. On four threads a block is parsed and sliced into batches, which
  keep the block's bitmap where it has one.
- **A field's end from a mask** (`scanner.unquotedEnd`, `fieldmask_vector.go`): the
  scanner keeps a mask of the separators and newlines in sixty-four bytes of its
  buffer, two AVX2 compares a half, and an unquoted field's end is a shift and a count
  of trailing zeros; the next mask is made when a field runs past this one. A refill
  moves the buffer's bytes and starts it over. Where the vector is not there, under
  `GODEBUG=simd=128` or 0 and in the scalar build, and in a buffer's last 63 bytes, the
  end is found eight bytes at a time (`wordEnd`), as it now is there too.
- **Direct calls** (`int64Builder`, `float64Builder`): the types inference gives a
  file's numbers parse by a call, not a func value's; the null text, where no
  `WithNullValues` is given, is not asked about each field (`reader.nulls`).

The scope's "a column at a time", a block's fields located once and each column parsed
in a loop, is not built: what it would save, the builders' interface call a field and
`appendTo`'s loop, is about a seventh of the read now (`feature-gaps.md` §8).

## 2. Measured

The file of §1 collected, step 372 and this step alternating, three rounds of three
runs each, Polars 1.44.1 read between them, at a load of 4.0 to 5.7, the laptop busy
with other work:

| | step 372 | here | Polars 1.44.1 |
| --- | --- | --- | --- |
| one thread | 243–405 ms | 165–250 ms | 111–146 ms |
| four threads | 133–188 ms | 78–142 ms | 55–68 ms |

About 2.4 times Polars' time before, 1.4 after, on one thread and on four. Step 357
measured 76 ms against 22 on a quiet machine, each at its default threads.

**What is left,** the profile on one thread after: the splitter 19%, the float's
parse 16%, most of it its divide; the builders' interface call and `appendTo`'s loop
15%; the text columns' copies 10%; the reads 5%.

**The suites** read Parquet, and h2o reads CSV too, its `-io csv`. Its `gb1`, a sum
by a key of 100, over the 569 MB `g1.csv` of ten million rows, on four threads, step
372's runner and this step's alternating, three rounds of three iterations at a load
of 4.5 to 5.0: 1,136–1,930 ms, median 1,259, before; 958–1,154, median 986, after,
0.78 of the time; the answers byte for byte the same. (This record said at first that
no suite reads a CSV; the 0.6.8 report, step 374, found the h2o one.)

## 3. Tests

- **`TestParseFloatIsStrconvsToTheBit`** (`dtype`): `ParseFloat` against
  `strconv.ParseFloat`, to the bit, over the grammar's edges (`1e22` and `1e23`,
  2^53 and 2^53+1, 19 and 20 digits, `-0`, `.5`, `1.`, `inf`, `1e400`, exponents of
  four and five digits) and a million random decimals of 0 to 22 digits, a point
  anywhere or none, an exponent or none, a sign or none; a text strconv refuses is
  refused, and Go's own spellings, `1_000` and `0x1p3`, are refused on purpose.
- **`TestParseIntIsStrconvs`** (`csv`): `parseInt` and `parseUint` against strconv
  at 8, 16, 32 and 64 bits, value and error, over each width's edges, 18, 19 and 20
  digits, signs, leading zeros and text that is no number, and half a million random
  integers.
- **`TestABatchWithNoNullHasNoBitmap`:** an integer, a float, a text and a Boolean
  with nulls first, last, side by side and alone in a batch, on one thread and four:
  a column parsed with no null has no bitmap, one with a null has its nulls where the
  file has them, and the values are where they were.
- **`TestFieldEndsFoundSixtyFourBytesAtATime`:** 600 records of 1 to 14 fields,
  unquoted ones of 0 to 153 bytes, quoted ones holding separators, newlines and doubled
  quotes, CRLF line ends, split as `encoding/csv` splits them, by a comma, a semicolon
  and a tab, through buffers of 63 to 4096 bytes, read whole and 1 to 100 bytes at a
  time, and over a whole block.

**Teeth: 13 of 13 bite:** a mantissa past 2^53 taken; a divide made a multiply by the
reciprocal; a sign dropped; no digits a number; 19 digits in one pass; an int's width
unchecked; a uint's width unchecked; the last run dropped; a run kept past its null; a
refill that keeps the mask; a mask made past the bytes read; a mask of carriage
returns, not newlines; null text read only where two are given.

**Gate:** the light gate, one package at a time on four procs: the tests at v3, 28
packages ok; levels, vet ×3 and the bench engine tests clean; the 22 PDS-H answers
match DuckDB. The CSV package's tests also in the scalar build and at
`GODEBUG=simd=128`, where the eight-byte scan finds the ends.
