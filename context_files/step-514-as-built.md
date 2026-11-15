# Step 514 — as built

**qcheck shrinks a finding to a minimal case and emits its test.** Plan step 455 of
[`v0.7.2-plan.md`](./v0.7.2-plan.md) (H5 P2), on the qcheck of steps 512 and 513,
plugged in where [`step-512-as-built.md`](./step-512-as-built.md) §7 and
[`step-513-as-built.md`](./step-513-as-built.md) §9 said. Until now a finding was a
seed and a query of up to four operations over tables of up to 8,193 rows, reduced
by hand. Now `qcheck shrink` reduces it: the two findings of step 523 went from 184
rows and four operations, and from two tables of 8,192 rows, to six and five rows
in 0.2 and 0.9 seconds, and this PR's first `qcheck-duckdb` run shrank a third of
their class, found against DuckDB, from 79 rows and four operations to five rows
and two (§6).

## 1. What

Only `qcheck/` and `ci.yml` change; the root module, its `go.mod` and its code gain
nothing, and no dependency is added.

| package | lines (tests) | what |
| --- | --- | --- |
| `shrink` | 3,803 (676) | `Case`, its JSON and the tables it holds (`case.go`, `query.go`); `Signature` and how a failure is classed (`signature.go`); the checker, every query inside `fuzzcheck.Run` (`run.go`); `Shrink`, its budget and its cost (`shrink.go`); the passes (`passes.go`); `EmitGo` (`emit.go`), `EmitSQL` and `EmitPolars` (`emitsql.go`); `LoadCorpus`, `Replay`, `Judge` and `WriteArtifacts` (`corpus.go`) |
| `self` | 41 | `Bisect`, the self check's bisection exported, which `caseRun.bisect` now calls (`bisect.go`) |
| `compare` | +51 | a `Verdict` says what differs (`What`: count, columns, rows or a cell) and in which `Column`; a `Plan` names its two sides (`Want`, `Got`), "DuckDB" and "ursus" when empty, as before |
| `duck` | (177) | `TestCorpusAgainstDuckDB`, `TestEmittedSQLRunsInDuckDB`, and `TestEmittedSettingsAreDuckDBs` behind the `duckdb` tag |
| `cmd/qcheck` | 304 | `qcheck shrink`, and `-shrink` on `self` and `duck` |
| `testdata/corpus` | 3 cases | the shrunk findings, regressions since step 523 fixed them (§6) |

## 2. A case is JSON

A `Case` is a finding as data, one file in `qcheck/testdata/corpus` or under a
`-shrink-dir`:

- `source`: the finder, `self` or `duck`; `seed`: the generated case it came from.
- `mode`: how it runs. `differential` runs the reference configuration and the
  failing one, which must agree, as the self check does; `rerun` runs the failing
  one twice, which must agree to the bit; `run` runs it once, inside
  `fuzzcheck.Run`, for ErrInternal, a panic, an allocation past the bound or a
  goroutine left behind; `schema` compares `CollectSchema` with the AST's types;
  `duck` runs sqlgen's SQL in DuckDB and the query in ursus under the failing
  configuration, as package compare does.
- `reference` and `config`: the two configurations, threads, batch, the optimizer
  off, `WithVerify` and the records each table is cut into.
- `tables`: each **generated**, its seed, `max_rows` and its shape (rows, columns'
  names and types), which is checked when it is read so a case says so when the
  generator changes; or, once shrunk, **its rows**: an integer and a Date's days a
  number, a float its shortest text ("NaN", "+Inf", "-Inf", "-0" among them, and a
  NaN of another payload `NaN:` and its bits), text a string or `{"hex": …}` where it
  is not UTF-8, a Boolean, `null`. A table keeps the seed `build.Records` cuts it by.
- `query`: the typed AST, each frame an object naming its op, each expression one
  naming its operator and type, read back through `ast`'s constructors and then
  `ast.Check`, so a file cannot hold an ill-typed tree.
- `open`: a known open finding, the step that fixes it and what it is; `note`; and
  `shrunk`, the sizes before and after, the runs and the seconds.

**The signature** is how a case fails, and every step of a shrink keeps it:

| kind | and | from |
| --- | --- | --- |
| `mismatch` | `what`: `count`, `columns`, `rows` or `cell`; for a cell, `op`, the operation that computes the differing column, followed back through every column passed through or renamed (a `Select` alias, a group key, a join's `_right`), so a step that drops a rename keeps it, and `type`, the column's | the comparison (`compare.Compare`, whose `Verdict` now says what differs) |
| `refusal` | `what`: the side that refused (`reference`, `variant`, `ursus`); `class`: the ursus error kind it is (`schema`, `type`, `value`, `unsupported`, `io`, `resource`, `other`) | one side refused, the other answered |
| `internal` | `class`: the error's first line, its numbers elided | fuzzcheck: ErrInternal |
| `panic` | `class`: the function the panic was raised in | fuzzcheck |
| `alloc`, `leak` | | fuzzcheck: the allocation bound, a goroutine left behind |
| `hang` | | fuzzcheck's watchdog, which ends the process: a hang is recorded, never shrunk in one |
| `rerun` | as a mismatch | one configuration, two answers |
| `schema` | | ursus's schema is not the AST's |
| `sql` | `class`: DuckDB's error, numbers elided | DuckDB refused the SQL |

The op and type make the signature stable across steps that rename or drop
columns, and narrow enough that a step sliding onto another bug is refused: step
523's two cases are `mismatch cell Min Float64` and `mismatch cell Max Float64`, and
a step that kept a mismatch in another column, or a row count, would not be kept.

## 3. The passes

`Shrink` first runs the case as it is: one that passes is `ErrPasses`, one whose file
names another signature is refused, and a case with none learns it then. Then it
runs passes until a whole round keeps no step or the budget is spent:

1. **factors:** the failing configuration to the fewest factors that still fail,
   through `self.Bisect`, the self check's bisection (each single factor of its
   difference from the reference first, then pairs, and so on); a DuckDB, run or
   rerun case tries the reference itself first, which may fail; then lower levels of
   the factors kept, four threads for eight, two records for more.
2. **rows:** ddmin on each table's rows, Zeller's delta debugging: the table emptied,
   then halves and their complements, then finer cuts.
3. **ops:** each operation dropped, the root's first: the frame replaced by its input,
   a join by its left side or its right.
4. **parts:** an aggregation, a group key, a `Select` or `WithColumns` column, a sort
   key dropped; `MaintainOrder` off, a group-by's and a join's.
5. **exprs:** each expression, from its root down, replaced by a simpler one of its
   type: an operand of its own, nearest first; a literal (0, 0.0, "", false and true,
   day 0), or a simpler literal; a column of its input. Each node above is built again
   through `ast`'s constructors and must keep its type; an aggregation stays one at
   its root.
6. **columns:** each table's columns dropped where the query no longer reads them, the
   last first and never a table's last; the tables no scan reads.
7. **values:** each column's values simplified: the whole column to 0 or to its first
   value; each distinct value everywhere it is, so rows that share a key keep sharing
   one; then, in a table of at most 64 rows, each value toward 0 (0, half, ±1, its
   absolute value), a float to +0, 1, -1 or its integer part, text to "", its first rune
   or its first half, false, or the column's first other value.

**Each step must** pass `ast.Check`, keep inner and left joins under 10,000 matches
counted on the base tables (`gen.MaxJoinRows`, or the case's own if larger: making
keys alike must not make a join of millions of rows), fail with the signature, and
**lower the case's cost**: in order, its operations, expression nodes, tables, rows,
columns, factors, their levels, the columns read, its literals' and its values'
complexity (a null, 0, false and "" cost nothing; a sign, more bits, more text and
each distinct value cost more). A step that lowers nothing is not kept, which ends
the passes; candidates already run are remembered by their query's Go, tables and
configurations.

**The budget** is 2,000 runs or 120 seconds (`DefaultRuns`, `DefaultTime`): a run is
one candidate checked, which runs its query in each of the case's configurations,
twice in a differential; a candidate refused by `ast.Check` or the join bound runs
nothing and is not counted. Every query runs inside `fuzzcheck.Run` with the
finders' limits, a minute's wall and the run's allocation bound. A shrunk case is run
three times more, and the result says how many failed alike.

**Deterministic:** the passes take their candidates in a fixed order, no map's, and a
case's tables are never changed in place, so a seed shrinks to the same case to the
byte (`TestShrinkingIsDeterministic`), as long as the bug itself answers alike at
its configuration.

## 4. What is emitted

`WriteArtifacts` writes, for a shrunk case named `<source>-<seed>`:

- **`<name>.json`**, the case.
- **`<name>_test.go`** (`EmitGo`), a test in package `ursus_test`, one file: each
  table built from its rows with arrow-go's builders in a closure, the query as
  `Query.Go` writes it, run under the reference configuration and the failing one,
  each table read in the records `build.Records` cuts it into, by their offsets; the
  two answers held to agree by `ursustest.AssertFrameEqual`, in order where the query
  promises one and with `IgnoreRowOrder` where not, a Count by its number; an
  `ErrInternal` under either fails it, and so does one side refusing. It is a
  differential: the expected answer is the reference's, never a typed table. A rerun
  runs the failing configuration twice; a schema finding compares `CollectSchema`
  with the AST's types, qcheck's rules; an allocation is bounded at fuzzcheck's 1 GiB
  by `runtime.ReadMemStats`, and a leak by the goroutines a second after. A noisy
  column, a float Sum or Mean of a group-by on several threads, is dropped before the
  answers are compared where the thread counts differ (its last bits move with them,
  feature-gaps §1); a finding in a noisy column is not emitted, as a Go test does not
  hold its bound. Its doc names the seed, the sizes, the signature, the
  configurations and the comparison's first difference. Imports are only those the
  file uses, and it is gofmt'd.
- **`<name>.sql`** (`EmitSQL`) for a DuckDB case: duck's settings, sqlgen's macros,
  each table created with its `__rid` and inserted row by row, a Date as its days as
  `duck.Load` registers it, and sqlgen's translation of the bounded tree. A test of
  package duck holds `SQLSettings` to `duck.Settings`, and another runs the script for
  generated cases in a database of its own, whose answer must be the answer through
  `Load` (both on CI).
- **`<name>.py`** (`EmitPolars`), the query in Polars where its operations map: frames
  `filter`, `select`, one `with_columns` a column (ursus's read the ones before them),
  `group_by(..., maintain_order=)` or a keyless `select`, a stable `sort` with
  `nulls_last` per key, `head`, `slice`, `join` with `coalesce` and `maintain_order`;
  typed literals, `cast(strict=False)` for a lossy cast. Text to a Boolean has no
  Polars cast, and a case holding one gets no snippet. It is a reading aid, not an
  oracle: ursus's semantics are its own where they differ (§9).

A DuckDB case's Go test is the same differential, ursus at the reference against the
failing configuration, with DuckDB's answer in its comment: the root's tests link no
DuckDB, and where ursus answers alike at both, the `.sql` and the corpus's replay on
CI are what hold the finding.

## 5. The corpus and the ratchet

`qcheck/testdata/corpus/` holds shrunk cases. `TestCorpus` (package shrink, every
`go test`) replays each self-check case as it is, up to three times until it fails
with its signature (a finding over threads may fail only most of the time), and
`Judge` decides:

- a case that **fails and is not marked open** fails the test: the finding is fixed,
  or marked open with the step that fixes it;
- a case **marked open that no longer fails** fails it as **stale**: the mark is
  removed once the fix is in, and the case stays a regression;
- a case that **fails another way** fails it, a finding of its own;
- a case not marked that passes, and one marked open that fails, hold.

A file must be named for its case, and a case must name a signature. DuckDB cases
are replayed by package duck's `TestCorpusAgainstDuckDB` in the `qcheck-duckdb` job:
one so far, `duck-822025313` (§6). `TestTheRatchetBites` holds `Judge` to all four verdicts through
the planted bug (§7).

`qcheck shrink ... -corpus testdata/corpus -open "step N: …"` adds a case; the mark
comes out with the fix.

## 6. The shrunk findings

Both of step 523's findings failed on master when they were shrunk, and went into
the corpus **marked open with step 523**, with a third of their class that this
PR's CI found. Step 523's PR (#78) then merged; with master merged in, `TestCorpus`
failed the two self-check cases as stale, "marked open … but no longer fails … in 3
runs", the ratchet's own tooth, and the three `open` marks are removed: the cases
are **plain regressions** now, each note saying it was fixed by step 523. The third,
a DuckDB case, is replayed by package duck's `TestCorpusAgainstDuckDB` on CI.

| case | signature | before | after | factors | runs, time |
| --- | --- | --- | --- | --- | --- |
| `self-802018528` (PR #72's qcheck job) | `mismatch cell Min Float64` | 1 table, 184 rows, 4 columns; 4 ops (filter, join, group-by, its aggregations), 12 expression nodes; 4 factors | 1 table, 6 rows, 2 columns (`f64b` holding 1, -0 and four +0, `boolc`); 3 ops, `Filter(boolc)`, an inner join with itself on `f64b` keeping the left's order, a keyless `Min(f64b)`; 3 nodes | threads 4, batch 3 | 273, 0.2 s |
| `self-807010171` (CI run 807's qcheck job) | `mismatch cell Max Float64` | 2 tables, 16,384 rows, 8 columns; 1 op, a group-by on three keys with four aggregations; 14 nodes; 5 factors | 1 table, 5 rows, 2 columns (`i64a` 0 and 1, `f64b` one -0 among +0s); 1 op, `GroupBy(i64a).Agg(Max(f64b))`; 3 nodes | threads 4, batch 1 | 855, 0.9 s |

| `duck-822025313` (this PR's first `qcheck-duckdb` run, 33,610 cases; shrunk there by `qcheck duck -shrink`) | `mismatch cell Min Float64` | 1 table, 79 rows, 6 columns; 4 ops, a `WithColumns`, a `Select`, a group-by of four aggregations, a `WithColumns`; 29 nodes; 5 factors | 1 table, 5 rows, 1 column (`f64f` 0, 1, 0, 0, -4.78e170); 2 ops, `Select(f64f.Mod(f64f))` then a keyless `Min`, whose input holds NaN, +0 and -0; 5 nodes | threads 4, batch 1 | 266, 1.1 s |

All three failed alike three times of three after shrinking. The second was reported
as the query's Mean; its signature says Max, as step 523 found. Each emitted test,
built into a module of its own, failed on master before step 523 with -0 against +0
between the reference and four threads, and passed over step 523's branch (96cc3ee,
built read-only before it merged): the third is step 523's class too, its DuckDB
answer the reference's. Its -4.78e170 is a divisor whose remainder is -0; the values pass then
tried no -1 for a float, and now does.

## 7. Tests

All in `qcheck/shrink` unless named; the expected values are the planted bug's
minimum, a round trip's own input, the reference configuration's answer, or the
checker's verdict: no typed table.

- **`TestShrinkerReachesMinimum`**: a test hook (`corrupt`) plants a known minimal
  bug, a row too many or a Count one more under any configuration but the reference
  whenever a Filter reads an Int64 column of a table holding 7. The first big cases
  it fails (hundreds of rows or more, two operations or more; no join, as a step
  cannot rename a right side's suffixed columns back) shrink each to the minimum: one
  table of one row and one column, an Int64 holding 7, one operation, a Filter reading
  it, one factor, failing three times of three. Three cases through the differential
  (967 to 11,027 rows, 2 to 4 operations, to the minimum in 45 to 61 runs) and two
  through package compare's path against an engine, ursus at the reference standing
  in for DuckDB, whose SQL `EmitSQL` then writes.
- **`TestShrinkingIsDeterministic`**: one seed shrunk twice, the two files equal to the
  byte, the seconds left out.
- **`TestAPassingCaseIsNotShrunk`**: `ErrPasses`, and a file naming another signature
  refused.
- **`TestEmittedTestCompiles`**: the planted bug's shrunk cases and cases of every mode
  and branch (rerun, internal, alloc, leak, schema, DuckDB, a noisy column dropped,
  tables read in records, every column type and its extremes) emitted into a
  temporary module that requires this checkout's ursus by a replace, with the qcheck
  module's go.sum, and `go vet ./...` run on it, which type-checks the tests; offline
  (`GOPROXY=off`, `GOFLAGS=-mod=mod`). Its tooth: a file whose `WithMemoryLimit` takes a
  string must fail the same vet.
- **`TestCorpus`** and **`TestTheRatchetBites`** (§5).
- **`TestACaseIsItsFile`**: 120 generated cases written and read back, generated and
  held as rows, their tables equal to the bit (an IPC stream's bytes) and their queries
  to the character, NaN, -0, the infinities, MinInt64 and NUL among their values; and
  text that is not UTF-8.
- package duck: `TestCorpusAgainstDuckDB`, `TestEmittedSQLRunsInDuckDB`,
  `TestEmittedSettingsAreDuckDBs`, on CI.

The self check's tests and teeth pass unchanged with `caseRun.bisect` through
`Bisect`.

## 8. The CLI and CI

```
qcheck shrink (-case FILE | -seed N [-duck] [-variants V]) [-out DIR] [-corpus DIR]
              [-open TEXT] [-note TEXT] [-runs R] [-time T] [-v]
qcheck self ... -shrink [-shrink-dir DIR]
qcheck duck ... -shrink [-shrink-dir DIR]
```

`-seed N` runs the finder on case N alone, the self check or DuckDB with `-duck`,
and shrinks its first finding that is a case (a canary's, a vacuous op and a stale
rule are a run's, not a query's); `-case` shrinks a file, with DuckDB for a DuckDB
case. It prints the signature, the sizes before and after, the runs and the steps
each pass kept, and writes §4's files into `-out`; it exits 1 when the case does not
fail. With `-shrink`, `self` and `duck` shrink each finding of the run, at most three
(plan step 455's), into `-shrink-dir`, `qcheck-findings` by default.

ci.yml: the `qcheck` and `qcheck-duckdb` jobs run their five minutes with `-shrink
-shrink-dir qcheck-findings`, and on failure upload `qcheck/qcheck-findings` as the
`qcheck-findings` and `qcheck-duckdb-findings` artifacts (`if-no-files-found:
ignore`). The `qcheck` job's `go test ./...` now runs the shrinker's tests and the
corpus's replay.

The jobs' runs on this PR, each from its run's seed:

| commit | job | the tests | the five minutes |
| --- | --- | --- | --- |
| 4332183 | `qcheck` | ok (shrink 10.1 s) | `self` from 822,000,000: 16,253 cases, no finding |
| 4332183 | `qcheck-duckdb` | ok: the settings, the corpus (no DuckDB case yet), 30 or more emitted scripts against `Load` | `duck` from 822,000,000: 33,610 cases, one finding, seed 822,025,313, shrunk by `-shrink` in 1.1 s and uploaded (§6) |
| c8ee3d6 (step 523 merged in, the marks removed) | `qcheck` | ok (shrink 9.5 s; the corpus's two self cases pass) | `self` from 825,000,000: 15,584 cases, no finding |
| c8ee3d6 | `qcheck-duckdb` | ok: `duck-822025313` replayed, unmarked, and passes against DuckDB | `duck` from 825,000,000: 24,984 cases, no finding |

Every other job of run 825 (c8ee3d6), the race job and the fuzz workflow's targets
among them, passed.

## 9. Not done

- A join dropped for its right side keeps the frames above reading `x_right`, which
  that side does not have, so a finding in a right side's column keeps its join; the
  planted bug's test draws no join for that reason.
- A hang is recorded in a case's format but not shrunk: fuzzcheck's watchdog ends the
  process.
- A DuckDB case's Go test cannot hold DuckDB's answer; its comment does.

## 10. Checks

See the PR for the outcomes: `cd qcheck && go vet ./... && go test ./...`, `go test
./internal/...`, `GOEXPERIMENT=simd go vet ./...`, `go run ./internal/gen/levels`,
`GOEXPERIMENT=simd go run ./internal/gen/api -check`, `go run
./internal/gen/testinventory -check` (it skips `qcheck/`, a directory with a
`go.mod`), `gofmt -l .`; no root code changed, so `go test .` in full was not run.
