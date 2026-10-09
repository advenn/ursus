# Step 152 — as built

**Two Tier 2 items of `v0.5-scope.md`:** audit I20, fixed, and each Parquet footer
read once, measured and declined.

## 1. I20: a String that is not UTF-8, written to Parquet

**The finding (audit.md):** `SinkParquet` writes a String column's bytes unchecked,
so a value that is not UTF-8 lands in a Parquet STRING column, which the spec
requires to be UTF-8. Polars and DuckDB refuse to read the file.

**How a String comes to hold such a value:** ursus does not validate text on the
way in. A Go string, a CSV field and a cast from Binary can each carry any bytes.

**The fix (`internal/source/parquet/writer.go`).** `writeColumn` checks every
present value of a String leaf with `utf8.ValidString`, where the promise is made.
The first that fails is refused:

- the error is `ErrValue`, naming the column and quoting the value;
- its hint: cast to Binary to write the bytes as they are;
- a Binary column is not checked, since writing raw bytes is what it is for.

Struct fields and list elements are leaves of their own, so the check reaches them
too.

**The cost** is one pass over the column's bytes, which the encoder reads anyway.

**Tests (`parquet_utf8_test.go`):**

- a value that is not UTF-8 is refused flat, as a struct field, and as a list
  element;
- the same bytes cast to Binary are written, and read back unchanged.

**Teeth:**

| tooth | result |
| --- | --- |
| a String written unchecked | **bites:** all three refusals |
| a Binary checked too | **bites:** the Binary case |

## 2. Each Parquet footer read once: measured, and declined

**The proposal (Tier 2):** a footer is parsed at plan time, for the schema, and
again when the file is opened to be read. Passing the first parse to the second,
with `file.WithMetadata`, would save one.

**Measured,** opening a file and parsing its footer, twenty times each:

| file | per footer |
| --- | --: |
| PDS-H SF=1 `lineitem.parquet`, 154 MB | 0.83 ms |
| PDS-H SF=0.1 `lineitem.parquet`, 15 MB | 0.11 ms |
| `nation.parquet` | 0.01 ms |

**The saving is under 0.5% of any PDS-H query:**

- SF=1's queries take hundreds of milliseconds, and SF=0.1's tens.
- The scan's workers already share one footer (`parallel.go`), so the second parse
  happens once per file per query.

**Against it, safety.** Reusing the plan-time parse would need proof that the file
had not changed in between. The scan reads its own footer today precisely so that
`layoutFor` refuses a file rewritten between `CollectSchema` and `Collect`.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean.
