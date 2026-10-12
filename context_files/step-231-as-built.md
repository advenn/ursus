# Step 231 — as built

**F6 of `v0.6.3-scope.md`, its last item: `Struct().JsonEncode()`.** The features
tier is done with it.

## 1. Why

Polars' `struct.json_encode` writes each struct as JSON text. ursus had no way to
write JSON at all, and NDJSON is on the README's "not done" list. This is the
expression half.

## 2. What changed

**`internal/kernel/jsonenc.go`.** `JSONEncode` prepares each column of the struct,
recursively, once (`newJSONCol`), then writes each row.

**The rules:**

- a null is null, a Bool true or false;
- **an integer, Int128 and UInt64 included,** is its digits, from its String cast;
- **a float:**
  - the shortest decimal that reads back to it, at 32 bits for a Float32;
  - with ".0" on a whole number;
  - an exponent from 10¹⁶ up or under 10⁻⁵, signed and with no leading zeros, as
    Polars writes 1.0, 1e+20 and 1e-7;
  - NaN and the infinities, which JSON has no number for, are null, as Polars';
- **a String** is quoted, with a quote, a backslash and the control characters
  escaped and every other character as it is. A byte that is not UTF-8 is U+FFFD,
  so the text is JSON;
- **a Decimal, a Date, a Time, a Datetime, a Duration and an Enum** are strings of
  their String cast, the text the CSV writer writes;
- **a List** is an array and **a Struct** an object, keys in field order.

**Elsewhere:**

- **`internal/expr/call.go`:** `FnStructJSONEncode` follows `FnStructRenameFields`.
  It types a String and refuses a Binary anywhere within, which Polars panics on.
- **`expr_struct.go`:** the method, with the differences below.

**Two differences from Polars:**

- **Temporal text.** Polars writes a Datetime with no zone as `2024-01-02
  03:04:05.120` and a Duration in ISO 8601 (`PT90S`). This writes the cast's text,
  the same as everywhere else in ursus.
- **A null struct** is a null String, where Polars writes the text "null".

## 3. Tests and teeth

- **`TestJsonEncodeAnswersAsPolars`:**
  - Polars 1.44.1's text for a struct of an integer, a UInt64 past Int64, a whole,
    a huge, a tiny and an inexact float, −0, NaN, infinity, a Float32, a Bool, a
    String with every escape and a non-ASCII letter, a Date and a Decimal;
  - a list with a null and a nested struct with a null field;
  - a Datetime against its cast's text;
  - a null struct;
  - a Binary refused.
- **`TestJSONNumbersAndStringsReadBack`** (kernel):
  - 2,000 random floats and edge cases, at 64 and 32 bits, each reading back bit
    for bit through `encoding/json`;
  - 2,000 random strings of control characters, quotes and every plane, reading
    back exactly;
  - an invalid byte written as U+FFFD.

The test's `0.1 + 0.2` was first a Go constant, which the compiler folds exactly
to 0.3. It is computed at run time.

| tooth | result |
| --- | --- |
| a whole float written as an integer | **bites:** both |
| NaN written as a number | **bites:** both |
| a float32 written at 64 bits | **bites** |
| a quote left unescaped | **bites:** both |
| a control character left raw | **bites:** both |
| an invalid byte kept | **bites:** once the test read the bytes. Go's decoder turns the byte into U+FFFD itself |
| a temporal written raw | **bites** |
| a null element written as its slot | **bites** |
| exponent zeros kept | **bites** |
| a Binary admitted | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's. The kernel file was one
const block short of gofmt's alignment when the gate began; formatted after, with
vet and its tests run again.
