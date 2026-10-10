# Step 166 — as built

**The 0.6 midpoint audit, recorded, and its small findings fixed.**

## 1. The audit

At the maintainer's request, after step 165: what is done, what is left, the
numbers, what must improve, and what later versions should build. It is
`audit-0.6-midpoint.md`. Its findings not fixed here are placed in `v0.6-scope.md`:

- the internal types in the public API, with the API breaks;
- the CSV reader's copies, with item 1;
- duplicated key code, unit coverage of `plan` and `expr`, and projection pushdown's
  391-line function, in "From the midpoint audit".

## 2. Fixed

**The SF=0.1 record (§4.1).** Step 154 recorded PDS-H at SF=0.1 as 24 ms, 2.55×
Polars. Those are DuckDB-Go's geomean and ratio, one column to the right of ursus's
in the report's table. The run's raw timings give ursus 29.0 ms, 3.09×. Step 127's
35 ms and 4.01× were checked against that report and are right. Corrected:

- `step-154-as-built.md`, with a note saying what was misread;
- its index row;
- `v0.5-scope.md` and `v0.6-scope.md`;
- the README's table, 2.6× to 3.1×;
- the changelog's v0.5.0 highlight, with the same note.

**The Makefile's header (§4.2)** called `GOEXPERIMENT=simd` mandatory. The package
doc says it is optional, and `test-all` runs the whole suite without it. It now says
what the experiment does and that it is optional.

**An unguarded goroutine (§4.3, §4.7).** The group-by's partitioned fold routed each
worker's groups on a goroutine of its own, with no `uerr.GuardErr`, unlike the merge
beside it. So an internal panic there ended the process rather than failing the
query. `eachConcurrently`, the join's partitioned build's fan-out, never used its
sink. It is now a package function, and the fold's routing and merge both run
through it: one fan-out, guarded once.

**The "Not done" lists (§4.9).**

- The README now names the union of the two lists.
- The v0.5.0 changelog's list gains the three it lacked, `RollingMap`, Arrow IPC files
  and NDJSON, with a note.
- `dataframe-features.md`'s §14, the recommendation written before v0.1, is marked as
  that, with what became of its v0.3 line.

**The notes, brought up to date** after the audit, at the maintainer's request:

- **The changelog** had nothing since v0.5.0. It now has an "Unreleased — toward
  v0.6.0" section for steps 156–166:
  - what is faster, with each step's figure;
  - what a user of v0.5.0 can trip on: `Filter(a.And(b))` no longer evaluating b on
    rows a drops, an inner join's order following either input, `Explain`'s
    additions, and a deferred Semi or Anti join's key columns;
  - what is fixed;
  - "Breaks: none yet".
- **The package doc's tour** began with `ursus.Scan(src)`, whose `plan.Source` no
  caller outside the module can name. It is what pkg.go.dev shows first. It now
  begins with `ursus.ScanParquet("sales.parquet")`.

## 3. Tests

**`TestEachConcurrentlyReturnsAPanicAsAnError`:**

- a write to a nil map in one of 40 items, on 4 workers, comes back as a
  `uerr.PanicError`;
- an error returned by one item comes back joined;
- without either, every one of 100 items runs, and runs once.

## 4. Teeth

| tooth | result |
| --- | --- |
| a worker's panic not guarded | **bites:** `TestEachConcurrentlyReturnsAPanicAsAnError`, the test binary dying |

Nothing in the speed of any query changes: the fold's work is the same, run by the
same number of goroutines.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's. The package doc's tour, a
comment, changed after it; `go vet` and `go doc` pass.
