# Step 354 — as built

**The CSV writer's options,** Polars' `write_csv`'s, from `feature-gaps.md` §7: how a
field is quoted, how a temporal value and a float are written, a byte-order mark, and
compressed output. An instant's and a float's own text are Polars' now. One break.

## 1. How

**Quoting** (`QuoteStyle`, Polars' `quote_style`): `QuoteNecessary`, the default, as
before; `QuoteAlways`, every field, the header's and a null's; `QuoteNever`, none;
`QuoteNonNumeric`, every field but a number and a null, a Boolean and a temporal value
quoted, as Polars quotes them. `WithWriteQuote` is `quote_char`. The text helpers are
generic over a string and a byte slice (`textOf`, `appendQuotedOf`, `quotedOf`), so a
value formatted into a scratch buffer is quoted without a string a value; a number is
written in place unless the style or a decimal comma needs it whole.

**Temporal values** (`temporal`): in a strftime format given for the type, compiled
once by `internal/strftime`, `WithDateFormat`, `WithDatetimeFormat` and
`WithTimeFormat`; else Polars' CSV text: a date's ISO date; a time of day to its unit,
nanoseconds; an instant `2024-01-02T03:04:05.123456` to its unit, and a zoned one on
its zone's wall clock with its offset, `+0900`, `+0000` in UTC, an offset with seconds,
a local mean time's, in UTC as before. A format that writes what a type has not, a
time of day of a Date, a date of a Time, a zone of a naive instant, is refused when the
column is bound, as Polars refuses it. **The break:** a naive instant was written with
a `Z`, as though in UTC (feature-gaps §1), and a zone's offset `+09:00`; ScanCSV reads
`+0900` now (`dtype.datetimeLayouts`, `Z0700`), and both forms.

**Floats** (`floatText`): Polars' text by default, the cast's (`kernel.AppendFloatText`,
step 325), where the writer kept Go's, `1.23456789e+08` and `1e-07`;
`WithFloatPrecision(n)`, `n` digits after the point, `strconv`'s `'f'`, which rounds
half to even as Rust does; `WithFloatScientific(true)`, Rust's `{:e}`, an exponent
unpadded and signed only when negative (`appendScientific`), `false` positional;
`WithDecimalComma`, a float's and a Decimal's point a comma, quoted under a comma's
separator, as Polars quotes every float there, an integer not. NaN, inf and -inf in
each. A Decimal keeps its scale.

**The rest:** `WithIncludeBOM`, a UTF-8 byte-order mark first, which ScanCSV skips;
`WithCSVCompression(CSVGzip or CSVZstd)` and `WithCSVCompressionLevel`, klauspost's
gzip (0 to 9, 6 by default) and Zstandard (1 to 22, 3), closed by `Writer.Close`; and
`SinkCSV`'s path checked against the codec, `.gz` and `.zst`, an uncompressed one not
ending so, unless `WithCSVCheckExtension(false)`, Polars' `check_extension`.
`csv.NewWriter` returns an error now, for a format, a precision or a level.

| Polars 1.44.1 `write_csv` | ursus |
| --- | --- |
| `quote_style` of each kind, `quote_char` | as Polars |
| `date_format`, `datetime_format`, `time_format` | as Polars; a misfit refused, as Polars |
| `float_precision`, `float_scientific`, both | as Polars |
| `decimal_comma` | as Polars; with scientific Polars runs values together, each written here |
| `include_bom`, `null_value` quoted | as Polars |
| `compression`, `compression_level`, `check_extension` | as Polars; a level past the range refused, where Polars panics or clamps |
| an instant's, a time's, a float's own text | as Polars, now |
| a formatted value holding the separator | written bare, a record misread; quoted here |
| a Duration | refused; written as printed here |

## 2. Tests

**`TestCSVWriteOptions`,** Polars 1.44.1's answers for its probe frame of an integer, a
float, text, a date, an instant and a time of day with nulls: the default, each quote
style, a quote of its own, the three formats, a BOM, a null's text quoted; a float's
precision of 2 and 0, scientific, never, both; a decimal comma under a comma's and a
semicolon's separator; a float's own text of ten values, never scientific, NaN under a
precision and scientific, a Float32's; a Decimal under a precision and a comma; an
integer beside a comma; nulls under `QuoteNonNumeric`; `QuoteNever`'s empty text.
**`TestCSVWriteInstants`:** an instant by unit and zone, three formats with zones, a
Date's and a Datetime's formats not each other's, a time's; read back by ScanCSV naive,
in UTC and in Tokyo; refused, a naive instant's zone, a Date's time, a Time's date, no
such directive, a negative precision. **`TestCSVWriteCompressed`:** gzip and Zstandard
at their default and another level, decompressed and read back; a level past each's
range; `SinkCSV`'s eight paths and `WithCSVCheckExtension(false)`.

**Teeth (18 of 18 bite):** QuoteAlways leaves numbers bare; QuoteNonNumeric leaves
text bare; QuoteNever quotes as needed; a null not quoted under QuoteAlways; a date's
format ignored; an offset with a colon; a naive instant with a Z; a float's precision
ignored; Go's padded exponent; no decimal comma; a comma's float quoted only as
needed; an integer quoted for a comma; no BOM; no extension check; Zstandard's level
unchecked; a naive instant's zone format taken; +0900 not read; Go's float text.

## 3. Performance

A million rows written to `io.Discard`, the median of seven, against the writer before
the step, built alike from `git archive`, on a machine shared at a load of about five:

| | before | now | Polars |
| --- | --- | --- | --- |
| an integer, a float, text and an instant | 519 ms | 397 | 168 |
| the integer | 33.4 | 32.6 | 5.8 |
| the float | 92.4 | 77.6 | 11.1 |
| the text | 27.9 | 27.5 | 6.7 |
| the instant, Datetime(us) | 382 | 246 | 129 |

An instant is formatted into a reused buffer with `AppendFormat` and quoted from it,
where it was a string a value, and a float by the cast's appender. Text kept its
speed once its quoting was chosen once a column rather than a value (a call more a
value cost it 4% first). Polars writes a file's parts in parallel; ursus's writer is
one thread's, its gap (§8).

**Gate:** test-all 140 ok, race 28 ok (the root package 976 s here), levels, vet ×3 and
the bench engine tests clean; the 22 PDS-H answers match DuckDB.
