# Step 509 — as built

**Fuzz targets for the text parsers and the CSV reader.** Steps 445 (H4.1, parsers
against strconv, math/big and time) and 446 (H4.3, CSV against encoding/csv, vector
against word scan, parallel against serial, streamed against whole) of
[`v0.7.2-plan.md`](./v0.7.2-plan.md), M3, and strftime's half of 464 (H4.2, M4), on
master's code before v0.8.0, as step 506's plan says. They run under step 508's
harness (`internal/fuzzcheck`), and each compares its answer with an oracle; "did not
crash" alone is the judgement of none. Four findings, each a bug with a small fix,
are fixed (§3). Steps 510 and 511 fuzz Parquet, and NDJSON, JSON and Arrow IPC, beside
this one.

## 1. The targets

18 targets in 6 packages. A public reader's target is in `internal/readfuzz`; a
parser's is in its package's tests, an internal test where it reads unexported code.
Each runs its body under `fuzzcheck.Run`: a parser's with `Limits{Wall: 5 s, Alloc:
64 MiB}`, the CSV package's and readfuzz's with 10 s and 64 MiB plus 1 KiB a byte of
input, as step 508 bounds a read; readfuzz's with `API: true`. The CSV package's,
the kernel's and readfuzz's tests import `internal/fuzzcheck/invariants`. No reader
here takes an allocator, so none has a bounded `Allocator`. Seeds are `f.Add`s,
and corpus files under `testdata/fuzz/<Target>/` where named below; the seed time is
the target's whole seed run, `go test -run '^Target$'`, under the gate's flags.

| package | target | oracle | seeds | seed run |
| --- | --- | --- | --- | --- |
| dtype | `FuzzParseFloat(s, f32)` | `ParseFloat`'s documented grammar as a regexp: a match is `strconv.ParseFloat`'s bits and ErrRange at either width, a signed NaN NaN (Polars' cast reads "-nan"; strconv reads only an unsigned one); anything else ErrSyntax, which encodes the refusals of `1_000` and `0x1p3` | 101, 1 corpus | 0.03 s |
| dtype | `FuzzNearestFloat(man, exp, neg)` | where it settles, `strconv.ParseFloat` of the number written out, to the bit and sign; an overflow is strconv's to report | 808, every powers-of-ten entry and both sides | 0.10 s |
| dtype | `FuzzTemporalRoundTrip(kind, unit, zone, ticks)` | a Date, a Datetime in no zone, UTC, Dublin, Casablanca, Lord Howe or St John's, a Time or a Duration, at each unit: `FormatTemporal` then `ParseTemporal` (Go's `time.Parse`) is the ticks; an instant in the years 1–9998, a time of day and a Duration `time.Duration` holds must read; `AppendISO` writes what `FormatTemporal` does for the type without its zone. No IsDST is named, since CI's tzdata differs | 134, 1 corpus | 0.03 s |
| dtype | `FuzzTemporalText(s)` | a kind `ClassifyTemporalText` gives, `ParseTemporalText` reads as that kind's inferred type; where Polars' patterns (the hand scanner) and `time.Parse` (ParseTemporal's layouts) both read s, the same ticks | 35 | 0.00 s |
| i128 | `FuzzParse(s)` | `^[+-]?[0-9]+$` and `big.Int` in the type's range, the same value, read from Hi and Lo apart from `String` | 25 | 0.00 s |
| i128 | `FuzzArith(ahi, alo, bhi, blo)` | `big.Int`: Add, Sub, Neg modulo 2^128; Add/Sub/MulChecked exact or false; DivMod's floor and remainder, Min / −1 refused; DivUint64 truncated; Cmp, Sign, String, Int64, Uint64; Float64 and Float32 against `big.Float`'s one rounding; FromFloat64 of b's bits against `big.Float.Int` | 225 | 0.02 s |
| i128 | `FuzzParseDecimal(s, p, sc)` | a reference of its own on `math/big`: the digits times 10^(exponent − fraction digits + scale), half away from zero, at most precision digits; a shift past the precision is out of range and one below minus the digits rounds to 0, decided by size, so 10^e is never computed for a huge e (the plan's "\|exp\| > 80 analytically"); an exponent past 2^40 is out of range, the documented rule | 296, 2 corpus | 0.04 s |
| internal/source/csv | `FuzzParseNumbers(b, bits)` | `parseInt` and `parseUint` at 8, 16, 32 and 64 bits: `strconv.ParseInt`'s and `ParseUint`'s value and error kind | 160 | 0.02 s |
| internal/source/csv | `FuzzClassifyAgreesWithParse(f, opts)` | infer.go's lattice: each type `classify`'s kind widens to (an int to Int128 and Float64, a word to Float64), its builder reads; a String reads unless it is not UTF-8 and not lossy; and the Bool, Int64, Int128 and Float64 builders read only what `classify` calls that kind or a narrower number. Options: dates tried, a decimal comma, lossy, null values, no inference | 287 | 0.01 s |
| internal/source/csv | `FuzzScannerMatchesEncodingCSV(data, size, sep)` | `encoding/csv` (`FieldsPerRecord` −1, `Comma` sep where it takes it, else a comma), records field for field or both refuse, but where `deviatesFromEncodingCSV` allows; the scanner over the whole input, through a buffer of 1–512 bytes and a few bytes a read (empty reads among them) must agree first; a byte-order mark is dropped at a stream's start and the rest scanned as without it | 49, 2 corpus | 0.01 s |
| internal/source/csv | `FuzzEndsMask(b, sep, off)` | a byte loop over the sixty-four bytes at any offset: the vector compare where the simd experiment builds one, itself in the scalar build | 5 | 0.00 s |
| internal/source/csv | `FuzzSplitMatchesSerial(data, block, opts)` | the serial read: `splitBlockSize` 1–64, restored after, four threads against one, the same rows and schema, or the same error text naming the same row and line after rows of the file's start; options: no header, a comment prefix, no quoting, ragged lines truncated, errors ignored, all String, `;`, dates | 223, 174 corpus | 0.06 s |
| internal/source/csv | `FuzzVectorFields(data, size, sep)` | `//go:build goexperiment.simd && amd64`: the same input with `vectorFields` true and false, buffers 63–300, whole and piecemeal, both paths in one binary; skipped where there is one path | 48 | 0.02 s |
| internal/kernel | `FuzzCastFromString(s, to, p, sc)` | `Cast` of the String column [s, s, null] to each integer width (strconv, a sign before an unsigned as documented), Int128 (`big.Int`), Decimal(p, sc), both floats, Bool (`strconv.ParseBool`), Date, Datetime (no zone, UTC, St John's), Time and Duration at each unit (the parsers the targets above hold to math/big, strconv and time); a strict error iff the non-strict cast gives a null, and otherwise the same column; the null row null | 882 | 0.03 s |
| internal/strftime | `FuzzAppend(format, sec, nsec, zone)` | `time.Format` a directive at a time, for every directive with a Go layout (a year in 0000–9999, %y of a year from 0, all but %.f); a zone's directive refused only for a naive instant; where the format names each part once and unambiguously (`determined`), `Parse` reads back the instant's fields. Zones: none, UTC, the four above, and Amsterdam's and Monrovia's offsets with seconds | 252 | 0.01 s |
| internal/strftime | `FuzzParse(format, text)` | what `Parse` reads, `Append` writes back as text `Parse` reads to the same fields | 28 | 0.00 s |
| internal/readfuzz | `FuzzScanCSV(data, opts)` | step 508's target extended: options from opts' 32 bits (any separator byte, no quoting, `'`, a comment, no header, SkipRows and SkipLines 0–3, NA, decimal comma, dates, truncate ragged, ignore errors, MaxRecordSize 64, 1 KiB or the default, InferRows 0, 1 or 100, lossy, batch size 1–7), the stream gzip, zlib or Zstandard by two bits; each read its own `Run`. One thread and four, with `WithVerify`, the same frame or error; then `CollectSchema` is the frame's schema, `Count` its height, `Head(1)` its first row, `Select` of its last column that column | 219: 45, and 174 corpus from Polars 2.1's recorded CSV cases | 0.21 s |
| internal/readfuzz | `FuzzScanCSVChunked(data, schedule)` | `ScanCSVReader` of the bytes whole: `ScanCSVFrom` whose reads take sizes from schedule, 0 among them (at most 99 in a row, which `source.Decompressed` allows) and a last one with `io.EOF`, reads the same frame or error | 25 | 0.01 s |

The corpus files of `FuzzScanCSV` and `FuzzSplitMatchesSerial` are the 174 texts
Polars 2.1's recordings read (`testdata/oracle/csvread.json`, `csvskip.json`,
`csvdates.json`) and wrote (`csvwrite.json`, the kitchen-sink frames), each with the
options its case names that the targets' bits can say. The plan's other seed lists
are `f.Add`s: `TestScannerMatchesEncodingCSV`'s and
`TestScannerRejectsMalformedQuotes`' corpora, step 424's exponents, 2^127 and 2^127−1
written out, 19- and 20-digit mantissas, "-0", ".5", "5.". Every seed run is well
under a second; the 18 together take 0.6 s.

`FuzzScanCSV`'s signature grew an opts argument, so the corpus fuzz.yml cached for
step 508's one-argument target no longer fits it; Go's fuzzer passes over a cached
entry it cannot read.

## 2. Allowed differences

One predicate holds the scanner's one deviation from encoding/csv,
`deviatesFromEncodingCSV`: encoding/csv refuses a quote inside an unquoted field
(ErrBareQuote), where ursus reads it as data, as `split.go` documents and as Polars
2.1 reads `a,b"c"` and `  "a",b`. Polars refuses `a"b,c\n1,2\n`, by its quote parity,
so it is not a model of the rule, only its witness that a quote in a field's middle
can be data. Each input encoding/csv refuses so is checked by the scanner's three
ways agreeing alone.

`FuzzAppend`'s `determined` leaves out the formats a round trip cannot read back,
each by Parse's documented reading: %y outside 1970–2069, %I without %p (an hour on
a 12-hour clock with no half of the day) and %p without %I, %Z (which Parse refuses),
a part named twice, a date that is not whole, and a day by %e or a year outside
0000–9999 followed by a digit, which Parse's longest run of digits takes, as chrono's
does: the plan's predicted first counterexample, "%Y past year 9999", is this, and
Polars 2.1 refuses `+1000000102` by `%Y%m%d` too.

`FuzzParse`'s round trip leaves out the same reading, by the text Append wrote
(`spaceDayRunsOn`): a day below 10 that %e wrote as " 1" to " 9" directly before a
digit, which Parse reads back as a two-digit day, as chrono and C's strptime do.
strftime's package doc now says so.

`FuzzScanCSVChunked` compares the two reads' errors with the line a read error is
reported near left aside (`sameReadError`). klauspost's Zstandard decoder decodes
blocks ahead on goroutines of its own, so whether the text before a corrupt block
reached the scanner before the decoder's error did depends on timing and on how the
bytes arrived. The error is the same; its "near line" is not.

## 3. Findings

Four, each found by the targets' seeds before any fuzzing, each a bug, each fixed:
the seed failed first, the fix is small, and nothing documented changes. Each input
is kept, minimised, as `testdata/fuzz/<Target>/finding-*`.

| # | finding | input | fix |
| --- | --- | --- | --- |
| 1 | **`dtype.ParseFloat` refused a signed NaN**, though its documented grammar is `[+-] (inf \| infinity \| nan)`: strconv reads a sign before inf and infinity only. So `Cast("-nan" → Float64)` was a null, a CSV Float64 column refused it, and inference read it as text. Polars 2.1's cast reads "-nan" and "+nan" as NaN. | `FuzzParseFloat/finding-signed-nan`: "-nan" | `numtext.go`: a sign and "nan" in any case is NaN. |
| 2 | **`DataType.FromTime` refused the least second of a fine unit's range**: it checked `sec*perSec` before adding the sub-second part, and at the least second the product is past int64 though the instant is not. So the least nanosecond instant, 1677-09-21T00:12:43.145224192Z, rendered and did not read back, through `ParseTemporal`, the CSV reader, a cast or a `time.Time`; 0.85 s of nanosecond instants, 0.78 s of microsecond and 0.81 s of millisecond ones. | `FuzzTemporalRoundTrip/finding-least-nanosecond-instant`: Datetime(ns), −2^63 | `temporal.go`: before the epoch, (sec+1)·perSec less what the second lacks, each step checked. |
| 3 | **`i128.ParseDecimal` called an exponent of more digits than an int holds no number** (ErrSyntax): `strconv.Atoi` fails with ErrRange. Its doc says a value past the precision is ErrRange, and the package reads a zero with any exponent as 0, `0e9223372036854775807` among them; `0e9223372036854775808` was refused. A strict cast's message said "cannot parse" for "not representable". | `FuzzParseDecimal/finding-exponent-past-int64` and `…-zero-…`: "1e99999999999999999999", "0e9223372036854775808" | `i128.go`: Atoi's ErrRange over a sign and digits is the int's extreme, which the 2^40 check refuses, or 0 for a zero. |
| 4 | **A `\r` that ends a CSV's input was data**: "a,b\r" read the field "b\r", `"a","b"\r` was refused ("unexpected '\r' after a closing quote"), and a last line of only "\r" was a value. encoding/csv and Polars 2.1 read each as a line end: `a,b`, `a,b`, and a blank line (Polars: a row of nulls once the width is known). | `FuzzScannerMatchesEncodingCSV/finding-cr-ends-input` and `…-after-closing-quote-…` | `scanner.go`: at the end of input a trailing `\r` ends an unquoted field, a quoted field and a blank line; `split.go`'s exact cut follows the scanner's rule. |

Polars 2.1 was asked through `~/ursus-scratch-archive/polars-latest` (2.1.0): the
casts of "-nan", "+nan", the decimal exponents, and `read_csv` of the `\r` and quote
inputs above. Polars 2.1 refuses "0e9223372036854775807" as a Decimal; ursus reads a
zero with any exponent as 0 (`TestParseDecimalExponentDoesNotWrap`), and finding 3
extends that rule to the exponents past an int, not Polars'.

Local fuzzing found nothing more: 11.3 million inputs over the 18 targets (§4), none
failing. CI's first 30-second runs, on this step's pull request, found two failures,
both in a target's comparison and not in the code it guards; each is kept as a seed
and §2 describes the rule each now follows:

- `FuzzParse/ci-space-day-before-digit`: "%Y%Y%y%y%e%Y%f" on "0000000000000100000"
  reads day 1, which Append writes " 1" before the year "0000", read back as day 10.
- `FuzzScanCSVChunked/ci-zstd-error-line`: a Zstandard stream with a corrupt second
  block, refused by both reads with klauspost's "compressed size too big", near line
  2 read whole and near line 1 read a byte or two at a time.

No finding is left for the maintainer, and no seed is skipped.

**Not built:**

- **`ndjson.FuzzScanFloat`** (445): `internal/source/ndjson` is step 511's package,
  fuzzed beside this step; it is left to 511.
- **`readfuzz.FuzzStrptime`** (464): its oracle is chrono's verdict on zones and DST
  gaps, recorded through Polars by 457's oracle steps (496–504 on master), and no
  such recording exists yet. strftime's own two targets are built; the strptime
  verdicts wait for a recording step.

**Observed, not a finding:** the scanner restarts a record's scan after each refill
(scanner.go's `readRecord`), and a refill brings what one read gives, so a long
record read in small pieces is scanned quadratically: a quoted field of 2 MiB read 1
KiB a read took 0.27 s, 0.07 s at 1 MiB (16 KiB reads: 0.03 s). A 16 MiB record, the
default limit, at 1 KiB a read would take some 17 s. The comment's "the total stays
linear" assumes a refill fills the buffer.

## 4. The local fuzz runs

`-fuzztime=20s` each, one at a time under the shared lock, 4 workers,
`GOEXPERIMENT=simd GOAMD64=v3`:

| target | inputs | new interesting |
| --- | --- | --- |
| dtype `FuzzParseFloat` | 1,087,429 | 64 |
| dtype `FuzzNearestFloat` | 1,250,493 | 1 |
| dtype `FuzzTemporalRoundTrip` | 980,153 | 24 |
| dtype `FuzzTemporalText` | 1,260,528 | 29 |
| i128 `FuzzParse` | 422,604 | 33 |
| i128 `FuzzArith` | 970,408 | 13 |
| i128 `FuzzParseDecimal` | 774,819 | 60 |
| csv `FuzzParseNumbers` | 781,356 | 0 |
| csv `FuzzClassifyAgreesWithParse` | 157,071 | 97 |
| csv `FuzzScannerMatchesEncodingCSV` | 50,995 | 49 |
| csv `FuzzEndsMask` | 830,717 | 10 |
| csv `FuzzSplitMatchesSerial` | 14,418 | 46 |
| csv `FuzzVectorFields` | 93,759 | 43 |
| kernel `FuzzCastFromString` | 162,527 | 38 |
| strftime `FuzzAppend` | 1,250,142 | 105 |
| strftime `FuzzParse` | 1,090,597 | 282 |
| readfuzz `FuzzScanCSV` | 64,034 | 45 |
| readfuzz `FuzzScanCSVChunked` | 84,090 | 55 |

The slower counts are Go's fuzzer minimising each new interesting input, up to
`-fuzzminimizetime` (60 s by default), with the progress line at 0/sec meanwhile: a
second run of `FuzzScannerMatchesEncodingCSV` at `-fuzzminimizetime=100x` read
391,820 inputs in 15 s. `fuzzcheck.Run` is not the cost: timed over a run, 42 of
some 330,000 calls took over 2 ms, each its 5 ms wait for the watchdog's goroutine to
end.

## 5. Teeth

Each mutation was made in the worktree from a saved copy, the named seeds run (or a
short fuzz), and the file restored from the copy; a mutation that did not compile
was redone as one that does.

| mutation | target | outcome |
| --- | --- | --- |
| `parseInt`'s one-pass path takes 19 digits | `FuzzParseNumbers` | **bites:** "9223372036854775808" read as −2^63 with no error |
| the vector `endsMask` compares `\r` for `\n` | `FuzzEndsMask` | **bites,** seed 0 |
| a speculative block's error taken as the stream's (`nextParallel`) | `FuzzSplitMatchesSerial` | **bites:** "blocks of 9: the error differs", at several sizes |
| the parallel path drops a block's last batch | readfuzz `FuzzScanCSV` | **bites:** one thread and four differ, from seed 1 |
| a CRLF in a quoted field kept | `FuzzScannerMatchesEncodingCSV` | **bites:** `"a\r\nb"` against encoding/csv's `"a\nb"` |
| `MulChecked` misses one cross product's carry | `FuzzArith` | **bites:** Min × (2^64−1) ok |
| `exactFloat` takes 20 significant digits | `FuzzParseFloat` | the seeds pass (no seed's 20 digits pass 2^64); **a 20 s fuzz bites:** "18700000000000000000" read as 2.5e17 |
| the unsigned cast's negative check off | `FuzzCastFromString` | **bites:** "-1" to Uint8 valid |
| strftime's %e padded with 0 | `FuzzAppend` | **bites** against Go's `_2` |
| each of the four fixes undone | the finding's target | **bites,** each on its finding's seed |

Two mutations of the splitter did not bite, as its design says: a doubled quote taken
for a closing one, and a quote in a field's middle taken to open a quoted field, each
make the exact cut meet a record it does not follow, and it hands the rest of the
stream to the serial scanner (split.go: "it does not guess at").

## 6. Checks

The gate, after the last code edit, with `GOFLAGS=-p=1 GOMAXPROCS=4
GOEXPERIMENT=simd GOAMD64=v3`:

- the touched packages' tests (dtype, i128, internal/strftime, internal/source/csv,
  internal/kernel, internal/readfuzz, internal/fuzzcheck): ok; the six packages'
  targets' seeds also with `GOEXPERIMENT=` (scalar) and, for csv and readfuzz,
  `-race`: ok;
- `go test ./internal/...`: ok;
- `go test .` in full: ok, 211 s;
- `go vet ./...`: clean;
- `go run ./internal/gen/levels`: ok;
- `go run ./internal/gen/api -check`: 2,481 features, 92 added and 0 gone since
  v0.7.2, as on master;
- `go run ./internal/gen/testinventory -check`: 1,225 lines, as in the baseline;
- `gofmt -l .`: empty.
