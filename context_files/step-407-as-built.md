# Step 407 — as built

**What is stale.** D2 of `v0.8-scope.md`, but its figures.

## 1. What was wrong

- **The README** said ursus was "at 0.6" and "This is v0.6", that it had one
  dependency, where `go.mod` has three direct (arrow-go, klauspost's compression and
  `golang.org/x/text`), that Array was not there, and listed as not done ten things
  built since: `MapGroups`, `RollingMap`, a lazy `Pivot`, `Upsample`, `Sample`,
  `List().Eval`, a rolling rank, Spearman's correlation, Arrow IPC and NDJSON. Its
  Sources named no `fs.FS`, and object storage `ScanCSVFrom` alone beside
  `ScanParquetFrom`.
- **`ci.yml`** called the SIMD experiment mandatory, "package `simd` does not compile
  without it", where every kernel has a scalar twin, which its own scalar-only job
  runs.
- **The docs:** `installation.md` named two dependencies of three and said "at 0.6",
  as `index.md` did.

## 2. How

- **The README:** the last release, v0.6.8, and master's 0.7 and 0.8, each with its
  scope; the docs' service guide linked, on the site's `dev` until a release has it;
  `fs.FS` among the sources; the three dependencies; nested types as they are,
  checked by writing one: List, Array and Struct of any depth through Arrow IPC and
  records, and Parquet refusing a List of Lists or of Structs by name, on both sides;
  every `From` reader; and what is not done, from `feature-gaps.md`: object stores and
  SQL out by decision, Parquet's deeper nesting, page indexes and bloom filters, Avro,
  Excel, Delta Lake and Iceberg, predicate pushdown into CSV and Arrow scans,
  expression-level common subexpressions, a join order by statistics, a work-stealing
  scheduler, `collect_all` and plan serialization, rolling weights.
- **`ci.yml`:** the experiment optional, the scalar-only job its proof.
- **The docs' pages:** three dependencies, and 0.x.
- **Left:** the README's figures, which the scope takes from 0.7's report, which the
  maintainer will ask for: its Benchmarks section and "Is this for you?"'s ratios are
  0.6.4's.

## 3. Measured

Nothing.

## 4. Tests

None: prose. The nested types' sentence was checked by writing a `List(List(Int64))`,
a `List(Struct)` and an `Array(Array(Int64, 2), 2)` to Parquet, refused for the
first, and to IPC, read back whole.

**Gate:** the light gate run with step 406's, below it: 29 packages ok, levels, vet ×3
and the bench engine tests; the 22 PDS-H answers match DuckDB's at SF=0.1.
