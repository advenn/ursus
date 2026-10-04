# Step 77 — as built

**CSV and I/O answer correctly.** This is `audit.md` §5's open rows that answered
wrongly — I8, I9, I10, I19, I21 and I22 — with S25 and the cast half of S21, which share
their parsers. It also covers **I27**, a silent loss the step's own round-trip sweep
found. It is the fourth step of the road to 0.3.

Ten commits and this document.

Authoritative where it disagrees with the vision docs and [`design/`](./design/).

Each of these was asserted on its own exit code:

- `make test-all` exit 0, with **105** package-ok lines: 21 packages × 5 SIMD
  configurations.
- `make race` exit 0 (21).
- `make levels` and `go vet` clean in all three modules.
- PDS-H SF=0.1 exit 0, with **22/22** matching the duckdb reference.

No benchmarks.

**The suite is 2740 passing tests and subtests**, counted from `go test -json`'s pass
events as at step 76 (2707).

---

## 1. What was wrong, measured

Each row was measured three ways before anything changed: ursus, through a
`go test -overlay` file so the repo stayed untouched; Polars 1.44; and DuckDB 1.5.

| row | ursus | Polars | DuckDB |
| --- | --- | --- | --- |
| I8 | Amsterdam 1930's offset +00:19:32 written `+00:19`: read back **32 s off** | writes zoned instants in UTC | — |
| I9 / S25 | `1_000` → 1000 and `0x1p3` → 8, by inference, a Float64 schema and `Cast` | String | String |
| I10 | a blank line in a one-column file **dropped**, trailing ones too | null | null; skips one in a wider file |
| I19 | `u64::MAX` and 2^63 inferred as **lossy Float64**; Int128 and Decimal schemas refused, as "opening csv source"; no String cast to either | Int128; reads both schemas, rounding 1.235 → 1.24 and −1.235 → −1.24 | Double; the same rounding |
| I21 | 1.0 written `1` and +Inf `+Inf`, so a float column read back as **Int64 or String** | writes `1.0`, `inf`, `NaN`; infers Float64 | infers Double |
| I22 | a 3-row zero-column frame wrote `""` lines, read back as **2 rows of a column named ""**; Parquet read back **(0, 0)** | has no rows without columns | — |
| **I27, new** | a null String written as an empty field read back as `""`: **every null String lost** | writes `""` for the empty string and nothing for a null, and reads them apart | reads both as null |

The writer's own doc called I27 "a property of CSV". One older test even said: *"if
this passes the documented limitation is wrong"*. Polars shows that quoting tells the
two apart.

## 2. Evidence first, in two commits

**E1 is `io_byhand_test.go`**: 26 cases, 21 wrong and listed in a two-way
`knownIODefects` ratchet, and 5 controls. Cases added later bring it to 31:
- two for I27, in E2's commit;
- three the teeth asked for (§4).

**E2 is `TestCSVRoundTripsEveryType`**, generated.
- **Shape:** 22 types, from the integers to Datetime at four units in four zones,
  including instants whose offset has seconds, each with a null.
- **Routes:** written, then read back with the frame's own schema. For the 12 types
  whose text is unambiguous, also read back by inference and cast back. That is 34
  routes; E2's commit message says 33, which is wrong.
- **Today:** ten routes failed. Nine were the rows above, and one was a row the audit
  did not have: **I27**, the String route. Two by-hand I27 cases joined E1.

## 3. The fixes

1. **I8: an instant whose offset has seconds is written in UTC.** RFC 3339 has no
   spelling for such an offset, and UTC is exact for every reader.
   - Every other zoned instant keeps its local text.
   - `FormatTemporal` is the one change. It also serves `Cast(String)` and display.
2. **I9 / S25: `dtype.ParseFloat` is the one float grammar.** It is the decimal one:
   a sign, digits with an optional point, an exponent, or inf, infinity and nan. It
   has no underscores and no hex.
   - CSV inference, the CSV float columns and `Cast(String)` all ask it. strconv still
     rounds.
   - Step 73's parse sweep had skipped underscore text for floats rather than pin an
     answer. It now pins it, and `0x1p3`, as malformed.
3. **I10: the scanner learns the record width** from the header, or from the schema
   when there is no header.
   - In a one-column file a blank line is that column's empty field, so it is null.
   - In a wider file a blank line is still skipped.
4. **I19:**
   - **`i128.Parse` checks the range on the digits** before any arithmetic. It used to
     wrap, which is why `CanCast` refused String → Int128.
   - **`i128.ParseDecimal`** reads decimal text as an unscaled value. It rounds half
     away from zero past the scale and refuses a value past the precision.
   - **Inference:** an integer past Int64 infers as Int128.
   - **Reader and casts:** the CSV reader reads Int128 and Decimal schemas, and
     `Cast(String)` reaches both, through the same parsers.
   - Step 73's parse sweep derives its targets from `CanCast`, so it now covers both,
     against a `big.Rat` oracle.
   - A 38-digit decimal text still infers as Float64, as in both engines.
5. **I21: the writer writes floats so they read back as floats.**
   - It appends `.0` to all-digit text, and writes `inf`, `-inf` and `NaN`.
   - **Inference:** a NaN or Inf word counts as a float beside a number. A column of
     nothing but words stays String; Polars infers that as a float.
6. **I22: both writers refuse a batch with rows and no columns**, with KindValue. A
   frame with no columns and no rows writes an empty CSV, with no header naming
   nothing.
7. **I27: the scanner records whether a field was quoted.**
   - In a String column an unquoted empty field is null, and a quoted `""` is the
     empty string. Every other type reads an empty field as null, as before.
   - The writer always quotes the empty string, and a null is its `NullValue`.
   - In a one-column frame a null is therefore a blank line, which fix 3 reads as
     null. The writer's old special case, which wrote a one-column null as `""`, is
     gone.
   - Three older CSV tests pinned the old rule and are restated under the new one.

## 4. Teeth

Every patch was checked to have applied, and every one ran against a green baseline.
Of 28, **27 bite**. The 28th was retired, because the code it aimed at could not be
observed (below).

| reintroduce | fails |
| --- | --- |
| the seconds kept in the offset | both I8 cases, E2 |
| the grammar unchecked / each of the three call sites on strconv | the I9 and S25 cases and the parse sweep / the case for that site |
| blank lines always skipped / the reader's width unset, with a header / without | the I10 cases, E2, the null-strings test / the same / the header-less case |
| past Int64 inferred as a float / no Int128 builder / no Decimal builder | the I19 inference cases, E2 / the Int128 cases / the Decimal cases |
| `i128.Parse` unchecked / decimals truncated / ties down / no final precision check | the overflow case, the parse sweep / the rounding cases, the sweep / the same / the sweep's 99999999.995 |
| no Int128 cast / the Decimal cast unrouted / `CanCast` refusing it | `TestCanCastAgreesWithTheKernel`, the S21 cases, the sweep, each |
| no `.0` / `+Inf` / words always String | the integral-float and spelling cases / the spelling case / the I21 round trip |
| the CSV zero-column check / the empty header / the Parquet check | each I22 case |
| an empty String always a value / the empty string unquoted / the quoted flag lost | I27, I10's String case, E2, `TestScanCSVEmptyStringIsNotNull` / I27 / I27, the buffer-integrity test |

**Silent at first, and fixed:**

- **The header-less width, `.0`, and `+Inf`.** Each wanted a case: a header-less
  one-column file with a blank line, an all-integral float column, and the written
  text of the specials. The float grammar reads `+Inf` back either way, so only the
  text can tell.
- **The width set during inference** could not be made to bite. A blank line cannot
  change an inferred type, so the two calls were removed instead. The reader sets the
  width, where it decides a value.

All of this went in one commit, after the teeth.

## 5. Behaviour changes

- **A zoned instant whose offset has seconds is written in UTC.**
- **`1_000`, `0x1p3` and other Go-only number syntax are text**, to inference, to a
  float schema and to `Cast`.
- **A blank line in a one-column CSV is a null row.**
- **An integer past Int64 infers as Int128.** Int128 and Decimal schemas read from
  CSV. `Cast(String)` to Int128 and Decimal exists.
- **Floats are written `1.0`, `inf`, `-inf` and `NaN`.** NaN and Inf words beside
  numbers infer as Float64.
- **A frame with rows and no columns is refused by the CSV and Parquet writers.**
- **In a String column, an unquoted empty field is null and a quoted `""` is the empty
  string.** The writer quotes every empty string. A file from a tool that writes
  nulls as `""` now reads them as empty strings, as Polars does.

## 6. Still open

- **I23, I24** (the production nullability check), **I16–I18, I20, I25, I26.**
- **S21's other half:** `ToUpper("ß")`, and the `Round` doc.
- The rest of the road to 0.3: the optimizer's leftovers next (O4, O5, O7, O9, P1,
  O8b).
