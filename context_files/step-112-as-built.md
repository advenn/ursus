# Step 112 — as built

**Parquet's INT96 timestamps and unannotated fixed-length byte arrays read.** This
covers two items:

- `v0.4-scope.md` item 22: *"INT96 timestamps, which Spark writes"*.
- Item 23's `audit.md` I16.

## 1. What was wrong

- **INT96** is the deprecated nanosecond timestamp, and Spark still writes it by
  default. ursus refused any file holding one as soon as it was opened: *"INT96
  columns are not supported"*.
- **An unannotated FIXED_LEN_BYTE_ARRAY** was typed Binary by the schema, then
  refused by the reader: *"a FIXED_LEN_BYTE_ARRAY that is not a DECIMAL"*. The plan
  promised a column that no `Collect` could produce.

**The fixture:** `testdata/parquet/pyarrow_int96_flba.parquet`, a 651-byte file
PyArrow 25 wrote with `use_deprecated_int96_timestamps=True`, documented in that
directory's README. Before the fix, it failed to open.

## 2. The fix

**INT96** is read as a naive `Datetime(ns)`, as PyArrow and Polars read it; its bytes
carry no zone.

- **`int96Nanos`** decodes the eight little-endian bytes of nanoseconds into the
  day, then the four of the Julian day, against the Unix epoch's Julian day,
  2,440,588.
- **What does not fit Datetime(ns),** from 1677-09-21 to 2262-04-11, is refused
  rather than wrapped. So is a day with more nanoseconds than a day holds. The
  conversion has no error to return, so it raises; both readers' recovery passes a
  raised error through as itself (step 87), a value error.

**An unannotated FIXED_LEN_BYTE_ARRAY** reads through the byte-array path.

- `byteArrayCol` now reads from an interface rather than the BYTE_ARRAY reader
  type.
- `flbaAsBytes` adapts a FIXED_LEN_BYTE_ARRAY reader to it. Converting each value is
  a slice header, and `byteArrayCol` copies the bytes out, as it must for a buffer
  arrow-go reuses.

## 3. Tests and teeth

**`TestInt96AndFixedLenByteArrayRead`** reads the fixture:

- **Timestamps:** 2024-01-02 03:04:05.123456, a null, 1969-12-31 23:59:59.999999 (a
  negative instant), and 1900-01-01.
- **Fixed bytes:** "abcd", 00 01 02 03, a null and "zzzz", read as Binary.

**`TestInt96Nanos`** (internal) checks hand-built values:

- **Exact:** the epoch, a nanosecond before it, 2024-01-02 03:04:05.123456789, and
  1900-01-01.
- **Refused as value errors:** a day's worth of nanoseconds, the year 2263, and the
  year 1600.

**One existing test changed with the behaviour.** `TestTypeRefusals` pinned INT96 as
refused. The first gate run caught it, because only the root package and the new
tests had been run. The case moved to `TestTypeMapping`, as a naive Datetime(ns),
with an unannotated FIXED_LEN_BYTE_ARRAY beside it as Binary. Its first version
passed the node's type length in the field-ID slot, and arrow-go panicked, which is
how that was found.

| tooth | result |
| --- | --- |
| INT96 refused again | **bites** |
| an unannotated FIXED_LEN_BYTE_ARRAY refused again | **bites** |
| no range check on the day | **bites:** the year 2263 wraps |
| the epoch one day off | **bites** |
| the day's halves read in the wrong order | **bites:** both tests |

**Gate:** test-all 105 ok, race 21 ok, levels, vet ×3 and the bench engine tests clean; PDS-H SF=0.1 22/22 against DuckDB. test-all was run twice: a killed earlier run's `make` kept writing to the same log, so the first log was mixed, and a clean second run is the record.
