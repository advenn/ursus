# Step 364 — as built

**The 0.6.7 report, and its documents.** It ran on step 363's code and changed no
code.

0.6.7 is **feature completeness, part 3: frame operations.** Steps 288 to 363 built it,
and more than its scope:

- **R, C, P, G, J, V and tier 2 (288–321):** the scope's rows, columns, reshaping,
  group-bys, joins, Go values in and out, and a `Series` with operations; the
  documentation site and user guide beside them (294–297);
- **Polars' API diffed (312), and what it found (322–331, 336–338):** selectors, the
  name and meta namespaces, Polars' odds, `TopKBy`, `InterpolateBy`, an expression's
  rolling, a window inside a window, a list a group in `Agg`;
- **the types (341–344, 351, 352):** `Array` and `Arr()`, `Categorical` and its I/O, a
  List, an Array or a Struct as a key;
- **I/O (332–335, 345–360):** Arrow IPC, JSON and NDJSON, compressed input, datasets
  and hive partitions, Parquet schema evolution, databases read, `read_csv`'s and
  `write_csv`'s options, Polars' date patterns;
- **the spills 0.6.6 left (339, 340):** `CorrSpearman`'s pairs and `Mode`'s counts;
- **performance (361–363):** a sort and a unique on several threads, and text sorted by
  its bytes.

The maintainer declared parity done on 2026-10-25. What `polars-parity.md` still lists
is left: Excel, Avro, Delta and Iceberg, a plan serialized, and niche options.

## 1. The scope's claims

**It answers as Polars 1.44.1 does.** Every step's tests hold each answer to Polars' on
the same inputs, but for the differences `feature-gaps.md` §9 records.

**It spills or streams where its pieces do.** Each step tested its operation under a
memory limit where it holds data, as the scope's §8 asks: `UniqueKeep` as `Unique` or
its window, `Having` as the group-by, `Upsample` as a join, the readers a batch at a
time. The eager ones, `PartitionBy` and the `Series` operations, hold their frame, as
`Pivot` and `Transpose` do.

**No regression.**

- **The plan diff:** the bench's own query code was built against v0.6.6
  (`git archive v0.6.6`) and against step 363's code, and printed every PDS-H query's
  optimized plan at SF=0.1 and every h2o query's at 2,000,000 rows. **All 37 are
  byte-identical.**
- **A timing A/B, this time:** steps 361 to 363 changed the sort and the unique beneath
  the plans, so the plans alone did not settle it.
  - Both runners were built as `bench/Makefile` builds one, `GOEXPERIMENT=simd
    GOAMD64=v3`.
  - The 22 PDS-H queries at SF=0.1 ran on four threads, the two runners alternating
    query by query, their order swapped each round: three rounds of five iterations,
    twice over, at a load of 1 to 3.
  - **In all, step 363's took 0.98 and then 0.95 of v0.6.6's time** (581 ms to 570, and
    645 to 615). Each query was within its noise: q2 read 1.27 once and 0.95 again,
    q16 0.77.
  - So 0.6.4's figures stand.
- **The answers:** PDS-H's 22 at SF=0.1 matched DuckDB's at every step's gate. h2o's 15
  ran once for this report, ursus alone at 10,000,000 rows over Parquet on four
  threads, each in 0.11 to 4.1 seconds, and all 15 match DuckDB's.
- **The 34 golden plans** held at every gate.

## 2. What 0.6.7 leaves

- **For 0.6.8,** as the maintainer set it on 2026-10-26: partitioned writes, a String
  column of more than 2 GiB, a database written; and several of 0.7's performance
  tasks (`v0.6.8-scope.md`).
- **In `feature-gaps.md` §8:** a count that reads every column; a CSV file read in
  about three and a half times Polars' time, and written on one thread; the integer
  sort two to three times Polars', a unique of a few keys twice, text one and a half.

## 3. The documents

- **`CHANGELOG.md`:** the 0.6.7 section, dated for the report: where it ends and its
  known limitations.
- **`README.md`:** the sources, types, frame operations, joins, grouping and execution
  rows, and why the figures stand.
- **`v0.6.7-scope.md`:** where it stands, and §10, what follows.

**Gate:** step 363's light gate stands, this step changing no code: the tests at v3,
28 packages ok; levels, vet ×3 and the bench engine tests clean; CI's every width,
the scalar build and the race detector passed on step 363's commit.
