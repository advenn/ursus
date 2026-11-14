# Step 506 — as built

**The plan after 0.7.2, and complete Parquet and Apache Iceberg wanted.** Set on
2026-11-14 with the maintainer, after the oracle-backed test suite (steps 496–504)
was merged. Documents only.

## 1. The rule the plan follows

The maintainer: "ursus doesn't need to be clone of polars. polars has features and we
need those features, thats it. we don't need to copy or clone it."

So Polars is the list of capabilities users need, not the specification of how
ursus answers:
- Where ursus answers differently for a reason of its own, its answer stands and is
  documented. Examples are exactness, refusing rather than guessing, and Go's types.
- A difference from Polars is not by itself a bug. ursus is fixed only where it is
  wrong by its own rules or docs.
- The recorded Polars answers under `testdata/oracle` are a reference for the
  features, and `oracleDifferences` names where ursus chose otherwise.
- With the stable API (step 494), a release only adds.

## 2. Wanted since this step

Both are in `feature-gaps.md` §7 and in the README's list of what is not done.

- **Parquet, complete:**
  - Nested columns read and written at any depth. Today only a flat column, a List
    of a primitive and a Struct of primitive fields are; a list of lists, a list of
    structs, a struct holding either and a map column are refused by name.
  - A map read as `List(Struct(key, value))`.
  - Field IDs.
  - Page indexes and bloom filters, read and written.
  - Compression level and per-column options on write.
  - Key-value metadata read and written.
  - A footer cache.
  - Zstandard decoding (§8).
- **Apache Iceberg tables:**
  - Read first:
    - the snapshot asked for;
    - its manifests and their Parquet data files;
    - columns matched by field ID;
    - pruning by partition and statistics;
    - v2 deletes applied.
  - Then write, append and overwrite, each committed as a new snapshot.
  - Catalogs: a table's path first, then the REST catalog.
  - Storage goes through ursus's openers and `fs.FS`; object stores are still the
    caller's.
  - Manifests are Avro, which ursus does not read. An Avro reader of its own, or a
    dependency, is a decision for the scope.

## 3. The plan

| | theme | what |
| --- | --- | --- |
| next | the differences closed | §12's 25 findings, read under §1: 22 are ursus's own rules, moved to §9 as documented differences, so their 39 recorded cases are asserted again instead of skipped; 2 docs that claim a Polars behaviour Polars 2.1 does not have are corrected (`ToDecimal`'s tie, Rolling `Offset`'s "as Polars' does"); 1 is a bug by ursus's own rules and is fixed: an NDJSON non-object read into a Struct under `ignore_errors` is a struct of nulls where every other unreadable value is a null |
| then | the bug hunt on master | the finders of `v0.7.2-plan.md` M3–M4, run on master's code before 0.8 is released: the parsers fuzzed against the standard library and arrow-go, random queries checked against DuckDB with a shrinker and a corpus, the long runs on CI; its oracle steps (456–459) are done by 496–504 |
| v0.8.0 | ursus in a Go service, the stable API | built in steps 392–407 and since; `api/v0.8.0.txt`, a report against a Polars 2.1 baseline run off the laptop, the tag on the maintainer's word |
| v0.9 | Parquet complete, then Iceberg | §2: Parquet's nesting and maps first, as Iceberg's files need them, then field IDs, page indexes, bloom filters and the write options; then Iceberg read, then Iceberg write |
| v0.10 | SQL | SQL over ursus's frames in DuckDB's dialect, parsed into ursus's plan; DuckDB the oracle, PDS-H in SQL the first suite (step 487) |
| v0.11 | structural performance | the bets 0.7 left: a zstd decoder of ursus's own, text without per-row copies, the CSV reader, window partitions, Categorical as codes |

The order of v0.9 and v0.10 is the maintainer's to swap. A capability users need
and ursus lacks is added when asked; nothing is added to match Polars for its own
sake. Out of scope: search and text indexes, GPU, distributed execution and Python
bindings. A stable 1.0 is not expected yet.
