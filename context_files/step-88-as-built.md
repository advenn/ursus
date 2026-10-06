# Step 88 — as built

**Runnable examples for what 0.3 adds.** The README sends a new user to
`example_test.go` first. Its eight examples covered filter, group-by, join, computed
columns, whole-frame aggregation, reading typed values and Arrow import, and none of
what 0.3 adds. Each example renders on pkg.go.dev beside the symbol it is named for,
and `go test` checks its printed output, so one that stops being true fails the
build.

One commit and this document. No library code changed.

Each of these was asserted on its own exit code:

- `make test-all` exit 0, with **105** package-ok lines: 21 packages × 5 SIMD
  configurations.
- `make race` exit 0 (21).
- `make levels` and `go vet` clean in all three modules.
- PDS-H SF=0.1 exit 0, with **22/22** matching the duckdb reference.

**The suite is 2944 passing tests, subtests and examples** (2936 at step 87).

---

## 1. The eight

| example | shows |
| --- | --- |
| `ExampleScanParquetFrom` | Parquet read through an `io.ReaderAt` that stands in for a ranged GET |
| `ExampleExpr_MapElements` | a Go function over each value |
| `ExampleLazyFrame_Explode` | `Split` into a List, `.List().Len()`, and `Explode` |
| `ExampleLazyFrame_JoinWhere` | a join on a price band rather than equal keys |
| `ExampleDecimal` | Decimal(10,2) × Int64 is an exact Decimal(29,2) |
| `ExampleEnum` | an Enum sorting small, medium, large: by its categories, not its text |
| `ExampleWithMemoryLimit` | a `Unique` of 20000 keys spilling under 16 KiB, and counting right |
| `ExampleLazyFrame_WriteParquet` | a List column written to Parquet and read back |

Every printed output was checked by hand before it became the expected output. For
example: 2.25 × 7 is 15.75, and 3 falls in the dear band, not the cheap one.

## 2. One example that taught something

`ExampleWithMemoryLimit` first counted 5000 keys and reported that nothing spilled.
That was correct. All 5000 keys appear in the first batch of 8192 rows, which is
admitted before the freeze; every later row is a duplicate of a key already held,
and is dropped rather than routed. The example now uses 20000 keys in batches of
1024, which do spill.

## 3. Still open

Nothing new: see step 87's §6.
