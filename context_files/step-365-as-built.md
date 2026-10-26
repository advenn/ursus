# Step 365 — as built

**The 0.6.8 scope.** No code changed.

[`v0.6.8-scope.md`](./v0.6.8-scope.md) takes the leftovers of feature completeness, as
the maintainer set them on 2026-10-26, a database written, partitioned writes and large
strings, and five of 0.7's performance tasks from `feature-gaps.md` §8: `Profile`, a
count that reads no column, slice pushdown, a frame's aggregate without a group-by's
machinery, and a faster CSV read.

- **Names and defaults** are Polars 1.44.1's, read from `write_database`,
  `write_parquet`, `sink_parquet` and `pl.PartitionBy` in the bench's environment.
- **Answers probed:** a partitioned write's paths, a key percent-escaped (`/`, `=`, a
  space, `%`, `:`, past ASCII), a null key's `__HIVE_DEFAULT_PARTITION__`, an empty
  key's `k=` read back as the empty string, `max_rows_per_file`'s numbering, and
  `include_key=False`.
- **Surveyed in the code** for each item, by three read-only passes: the database
  reader and the four sinks, the 32-bit offsets of a String column and who reads them,
  and the causes of the five performance gaps. The scope cites what they found.
