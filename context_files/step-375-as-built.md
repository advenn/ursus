# Step 375 — as built

**The 0.7 scope.** No code changed.

[`v0.7-scope.md`](./v0.7-scope.md) takes the theme the maintainer set, performance,
and orders it by where the suites' time goes against Polars 1.44.1.

- **Timed:** PDS-H's seven worst queries at SF=1 against Polars on four threads,
  alternating, two rounds of five, and four of them on one thread, each engine's CPU
  time measured too; the bare scan of q14's four lineitem columns, and of one, in
  both.
- **Profiled:** the seven queries, and h2o `gb4` and `j1` over Parquet at ten million
  rows, each profile split by where its CPU went: zstd, the rest of the Parquet read,
  the runtime's collections, zeroing and copying, and the kernels and operators.
- **zstd measured alone:** lineitem's two widest columns compressed at level 3 and
  decoded by klauspost's decoder, the one arrow-go uses, and by libzstd through
  pyarrow, on one thread: 508–571 MB/s against 849. lineitem's layout read from its
  footer and its pages: 51 row groups, a page a column chunk, no row group excluded by
  q14's dates.
- **Found:** the Parquet read is most of most of the worst queries; zstd is a floor
  ursus keeps, being pure Go, but about 100 ms of q14's scan beyond it is ursus's own,
  29% of it the runtime's over buffers allocated each page; step 193's dictionary
  predicates cover text alone, and decode a batch's other columns whole before taking
  the kept rows.
