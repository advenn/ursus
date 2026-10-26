# Step 374 — as built

**The 0.6.8 report, and its documents.** It ran on step 373's code and changed no
code.

0.6.8 is **the leftovers of feature completeness, and five of 0.7's performance tasks
done ahead of it** (`v0.6.8-scope.md`, step 365). Steps 366 to 373 built it:

- **the leftovers (366–368):** a database written, `WriteDatabase`; partitioned
  writes for the four sinks; a String or Binary column past 2 GiB;
- **performance (369–373):** `Profile`, first, so the rest are measured by it; a
  count that reads no column; slice pushdown; a frame's aggregate that takes its
  columns whole; a CSV file read faster.

## 1. The scope's claims

**The leftovers answer as Polars 1.44.1 does,** or the difference is recorded in
`feature-gaps.md` §9:

- **a database written:** Polars' three modes, checked against a recording driver
  for the four dialects and round-tripped through a real SQLite outside the
  repository. Its types are ursus's own for each dialect, where Polars' are
  SQLAlchemy's or ADBC's (§9);
- **partitioned writes:** Polars' tree name for name, and each reads the other's
  dataset back as the frame. A time of day's key keeps its fraction, a key's name is
  escaped, and past 64 open files the least recent closes (§9);
- **large strings:** Arrow's LargeString and LargeBinary past 2 GiB, a String or a
  Binary below, where Polars always exports the large type (§9). CI's `bigstrings` job
  checks a real 3 GiB column.

**The performance items change no answer,** to the bit where it is a float:

- a frame's aggregate is checked against the same aggregate of one group, to the bit
  (372);
- a float read from a CSV is `strconv`'s, to the bit, over a million random decimals
  (373);
- a count is the same count (370); a pushed slice is the same rows (371).

**No regression:**

- **The plan diff:** the bench's own query code was built against v0.6.7
  (`git archive v0.6.7`) and against step 373's code, and printed every PDS-H query's
  optimized plan at SF=0.1 and every h2o query's at 2,000,000 rows. **All 37 are
  byte-identical.** Slice pushdown changed one of the 34 golden plans, `slice.txt`,
  as it was built to (371); the rest held at every gate.
- **A timing A/B:** steps 368 and 370 to 373 changed what runs beneath the plans: the
  strings' offsets, the scans, the aggregates of no keys, the CSV reader.
  - Both runners were built as `bench/Makefile` builds one, `GOEXPERIMENT=simd
    GOAMD64=v3`.
  - The 22 PDS-H queries at SF=0.1 ran on four threads, the two runners alternating
    query by query, their order swapped each round: three rounds of five iterations,
    twice over, at a load of 2.6 to 3.5.
  - **In all, step 373's took 1.00 of v0.6.7's time both times** (591.0 ms to 590.8,
    and 611.6 to 610.0).
  - q4 read 1.15 and then 1.10, q21 1.07 twice. Run again alone at SF=0.1, twenty
    iterations in each of four rounds, they read 1.03 and 1.02; at SF=1, beside q3 and
    q10, three rounds of five, 0.93, 0.99, 1.03 and 0.99. Within their noise.
  - **Over CSV,** which only the CSV reader's step changed: h2o's `gb1` over its 569
    MB file took 0.78 of step 372's time (373).
- **The answers:**
  - PDS-H's 22 at SF=0.1 matched DuckDB's at every step's gate.
  - h2o's 15 ran once for this report, ursus alone at 10,000,000 rows on four threads:
    over Parquet each in 0.16 to 3.5 seconds, over CSV in 0.98 to 4.2, and all 30
    match DuckDB's.

So the README's figures stand, and h2o over CSV is faster than they say.

## 2. What 0.6.8 leaves

**For 0.7, performance,** as the scope's §5 lists it:

- predicate pushdown through group-by and window keys;
- expression-level CSE;
- the CSV writer on several threads;
- a work-stealing scheduler;
- the integer sort, the unique of a few keys and the text sort's gaps to Polars.

**And from 0.6.8's own steps** (`feature-gaps.md` §8):

- **a CSV file read in about 1.4 times Polars' time,** where it was three and a half.
  A column at a time would save the builders' call a field, about a seventh of the
  read;
- **a frame's aggregate** at 0.9 to 2.0 ms over a million rows, where Polars takes 0.13
  to 0.57: the rest is the scan and the pipeline of 123 batches, and a float sum's one
  chain of adds.

**Not built,** in `polars-parity.md`: Excel, Avro, Delta and Iceberg, and a plan
serialized.

## 3. The documents

- **`CHANGELOG.md`:** the 0.6.8 section, dated for the report: where it ends and its
  known limitations.
- **`README.md`:** the sources, types, optimizer and execution rows, and why the
  figures stand.
- **The documentation site:** *new in 0.6.8* notes for a dataset written, large
  strings and a database written in the I/O guide, and for `Profile`, a count and
  slice pushdown in "Lazy and eager".
- **`v0.6.8-scope.md`:** where it stands.
- **`step-373-as-built.md`:** it said no suite reads a CSV; h2o's does, and the record
  now has its `gb1` timed.

**Gate:** step 373's light gate stands, this step changing no code: the tests at v3,
28 packages ok; levels, vet ×3 and the bench engine tests clean; CI's every width,
the scalar build, the large-strings job and the race detector passed on step 373's
commit.
