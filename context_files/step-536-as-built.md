# Step 536 — as built

**v0.8.0 prepared, not tagged.** The maintainer decided on 2026-11-20 to release v0.8.0
now, its theme ursus in a Go service and the stable API: users on 0.7 lack every crash
guard and fix of the bug hunt (steps 507–534). This step prepares everything; the tag is
the maintainer's. It also records v0.7.3, tagged on `release-0.7` the same day by step
535, on master. Documents, `bench.yml` and the service probe's build; no library code
but godoc.

## 1. The problem

A release needs four things master did not have:
- a CHANGELOG section of its own, saying what a v0.7 user can trip on and what to do
  instead, as v0.7.2's says for a v0.7.1 user;
- `api/v0.8.0.txt`, the list `make api` holds every later change to;
- documents true to the code: the README still named v0.7.2 the last release and said
  the API moves, the installation page still said Go's v0 rule applies, and no godoc
  said what a service may hand ursus;
- the report §8 of `v0.8-scope.md` asks for, against Polars 2.1 as step 506 set it and
  run off the laptop: `bench.yml` ran PDS-H alone, at the lock's Polars 1.44.1, one
  iteration, and could not time an older ursus.

## 2. What was built, and why

### The CHANGELOG

"Unreleased — toward v0.8" became "v0.8.0 — 2026-11-20", its sections in the order the
earlier releases have them: where it ends (the report's figures and what was built),
what a v0.7.x user can trip on, Breaks, Changed (its two sections made one, 528 then
530), Features, Faster, Fixed, Tests and known limitations.

**What a v0.7.x user can trip on,** each with what to do:
- the shared budget and threads (395, 397): `WithMemoryLimit`, `WithThreads` or an
  engine of one's own keep 0.7's;
- a Zstandard window past 128 MiB refused (528): decompress with `zstd -d --long=31`;
- a float aggregate's NaN is `math.NaN()`'s bits (530): read a payload with a pick;
- a float `Max` of a tie answers the last tied row's bits (523);
- an Arrow time past its day refused (525, and Parquet's, 510): write a span as a
  Duration;
- a read cut short is an error (534): a reader of one's own ends with `io.EOF` itself;
- corrupt Parquet and IPC refused (510, 511, 517, 519–521, 531, 533), and an IPC
  stream's message held to arrow-go's limits (511): write smaller record batches;
- answers corrected (496–499, 507, 509): `Clip`'s name, a subnormal's log, a Float32's
  text, a digraph's title case, a null Struct where a Struct of nulls was, an inclusive
  split's last part, `"-nan"` inferred a float, a CSV's last `\r`;
- arrow-go's Parquet codecs replaced in its registry, process-wide (520).

**Known limitations** say what 0.8's scope measured and left (400, 398, spill files a
crash leaves), the two findings the fuzz targets skip by name (NDJSON inference, 511; an
IPC record batch's validity bit a row of a Struct of Nulls, 529), and what is wanted
and not built (506).

**Fixed's** first paragraph said "All three are in v0.7.1 too" above entries that are
not v0.7.1's; it says now which of the 0.7 line's fixes are where.

### v0.7.3, recorded on master

As master recorded v0.7.1 (`f2d2746`), at the main session's request: `api/v0.7.3.txt`
from `release-0.7`, its features v0.7.2's; a "v0.7.3 — 2026-11-20" section between
v0.8.0 and v0.7.2, its Fixed entries `release-0.7`'s words, its intro in the form of
v0.7.1's and saying that the bug hunt's guards are 0.8's alone; steps 532 and 535's
records and index rows from `release-0.7`, so the section's link holds; the README names
v0.7.3 the last of the 0.7 line. v0.8.0's trip-on entry for 525 says v0.7.3 has it.

### The API listing

`make api-tag TAG=v0.8.0` on this branch: 2,481 features, 92 more than v0.7.2's 2,389,
none gone. The tool takes the newest `api/v*.txt` by version, so `make api` measures
against v0.8.0 now, `0 added and 0 gone since v0.8.0`, and goes on doing so once
v0.7.3's file is beside it (0.8.0 sorts above 0.7.3). If a pull request adding API
merges before the tag, `make api` at the tagged commit says how many were added, and
`make api-tag TAG=v0.8.0` there writes the tag's list.

### The documents

- **README:** the last release v0.8.0, v0.7.3 the 0.7 line's; "the API still moves"
  replaced by ZeroVer's wording and the stable API; the status paragraph; Parquet
  complete's list as step 506 has it; the testing paragraph's oracles, qcheck's two
  modes and the fuzzing; the test count, 3,240 since step 116, now 14,208 passing tests
  and subtests, counted from this step's `go test -json` runs of the module's packages;
  the speed against Polars 2.1 and 1.44.1, and 0.8's report beside 0.7's history
  table, which is relabelled as the laptop's against 1.44.1.
- **`docs/`:** every statement changed since v0.7.0 checked against the code
  (`lazy-and-eager`, `memory`, `types-and-nulls`, `coming-from-polars`,
  `installation`, `aggregation`, `index`, `io`, `lists-and-structs`, `services`):
  - wrong and fixed: the installation page's versions (Go's v0 rule, which no longer
    applies), and `memory.md`'s "up to about one and a half times" the budget, where
    step 395 measured 1.4 to 1.8;
  - missing and added: io's 0.8 readers, `ScanLines` and its forms, `ScanIPCFrom`,
    `ScanIPCStreamFrom`, the IPC bytes readers and the `fs.FS` readers; services'
    list of `fs.FS` readers lacked `ScanLinesFS`; `benchmarks.md` gains 0.8's table.
- **Godoc:** the package doc gains "In a service": sharing, the budget and threads,
  cancellation, errors, and input a service did not write, what is checked before it
  sizes anything, what stays the service's, and the open finding. `Collect` gains
  "Cancellation" (the context's error, never part of the answer as the whole, some tens
  of milliseconds, a Go function finishing its call, the other entry points alike), and
  `CollectBatches` says its last yield is the context's error. Budgets were documented
  already (`WithMemoryLimit`, `Engine`, `Collect`).

### `bench.yml`

Inputs `suite` (`pdsh`, `h2o`, `probe`), `rows`, `io`, `iterations`, `polars` and
`baseline`, beside `scale` and `engines`; the default dispatch is the correctness check
it was. `polars` installs that version with `uv pip` over the lock's and sets
`UV_NO_SYNC`, as `uv run` would put the lock's back; `baseline` builds a tag's runner
from `git archive` of its tree, and the driver runs it with `PATH_GOBIN` and
`PATH_RESULTS` of their own, after the branch's engines; `suite: probe` builds
`svcprobe` on the branch and on the baseline and runs `lat`, `conc` and `alloc`,
alternated, two rounds, with `pl_lat.py`. Timeout 150 minutes; h2o's longest run took
eight.

`svcprobe`'s `shared` mode and its default engine's figures moved to `engine.go`
(`//go:build !ursus07`), and `engine07.go` stands in for them under `-tags ursus07`, so
the probe's other modes build against a 0.7 tree. The runner and the driver are
unchanged since v0.7.2, so v0.7.2's runner takes the branch's driver's flags.

### The report

`context_files/v0.8-report.md`, from eleven runs: the four suites with ursus, Polars
2.1.0, DuckDB and v0.7.2; the probe; four controls of ursus and the lock's Polars 1.44.1;
and 0.8 against v0.7.2 again at PDS-H SF=1 and h2o over Parquet.

| suite | ursus / Polars 2.1.0 | ursus / Polars 1.44.1, the control | 0.8 / v0.7.2 | again |
| --- | --: | --: | --: | --: |
| h2o, 10M rows, Parquet | 2.30 | 1.31 | 0.92 | 0.98 |
| h2o, 10M rows, CSV | 1.64 | 1.18 | 0.94 | |
| PDS-H SF=1 | 2.32 | 1.91 | 0.99 | 0.99 |
| PDS-H SF=0.1 | 1.68 | 1.79 | 0.98 | |

The probe: a 1,000-row query 0.35 to 0.74 of v0.7.2's time on all threads, 0.05 to 0.14
of Polars' from Python; sixty-four 10,000-row group-bys at once 2.3 times v0.7.2's
queries a second on 66 goroutines where it started 465; the 10,000-row group-by 66 kB
allocated where 358. Every answer of every engine in every run matches DuckDB's.

**Found by the report:** Polars 2.1 is faster than 1.44.1, most on h2o over Parquet,
so the gap is wider against it than the 0.7 report's; a query's ratio to v0.7.2 moves
by a quarter between CI runs, so only a suite's is read; on four vCPUs a 10,000-row join
on all threads takes 1.4 times its time on one (step 396's rule for a join).

### Merged with master

Step 537 (#91, the nightly qcheck workflow) merged into master while this branch was
open; master was merged in, its CHANGELOG line moved into v0.8.0's Tests and the
index's rows kept, its row on top as the newest.

## 3. Tests and their oracles

No test changes and no library code but comments. The report's answers are held to
DuckDB's reference, the bench's oracle, in every run. The API listing is the tool's
output over the tree.

## 4. The teeth

- **`api/v0.8.0.txt` holds 0.8's additions:** in a copy of the tree (`git archive`),
  `ScanLinesFS`, new in 0.8, renamed unexported. With `api/v0.8.0.txt`: `api: 2480
  features, 0 added and 1 gone since v0.8.0`, `! pkg github.com/advenn/ursus, func
  ScanLinesFS(fs.FS, string, ...LinesOption) *LazyFrame`, exit 1. With it moved aside,
  v0.7.2 the newest: `91 added and 0 gone since v0.7.2`, exit 0, so without this step's
  file the removal passes. Restored from the saved copy: `0 added and 0 gone since
  v0.8.0`.
- **The probe's split:** against v0.7.2's tree without `-tags ursus07`, `undefined:
  ursus.NewEngine`, `ursus.WithEngineMemoryLimit`, `ursus.WithEngine`; with it, it
  builds.
- **`bench.yml`'s first runs failed and taught two fixes:** all five failed in a minute,
  the baseline's `git archive` run from `bench/` archiving only `bench/`, so
  `engines/go` was missing; it archives from the repository's root (`5fbb291`). The
  first probe run read the suites' `ROWS=1e7` as `svcprobe`'s sizes, 1 row; the probe
  job's `ROWS` is empty (`aa38c9d`).

## 5. The checks

All under `GOFLAGS=-p=1 GOMAXPROCS=4 GOEXPERIMENT=simd GOAMD64=v3`, the tests under the
shared lock, after the last Go edit (the godoc); the edits after them are Markdown no
test reads, and the cheap checks below ran after the last edit of all:
- `go test ./internal/...`: 30 packages ok, 5,190 tests and subtests passed, 0 failed;
- `go test .` in full: ok, 7,048 passed, 0 failed;
- `go test ./dtype/... ./i128/... ./selector/... ./ursustest/... ./docs/examples/...`:
  ok, 1,970 passed, the docs' examples among them;
- `cd qcheck && go vet ./... && go test ./...`: ok, as the root package's godoc changed;
- `GOEXPERIMENT=simd go vet ./...`: clean;
- `go run ./internal/gen/levels`: `levels: ok`, no new package;
- `GOEXPERIMENT=simd go run ./internal/gen/api -check`: `api: 2481 features, 0 added
  and 0 gone since v0.8.0`;
- `go run ./internal/gen/testinventory -check`: `testinventory: ok`;
- `$(go env GOROOT)/bin/gofmt -l .`: nothing;
- the docs: `go run ./internal/gen/docsite -ref master` and `mkdocs build --strict`
  (docs/requirements.txt's tools in a venv): built, no warning;
- `bench/micro/svcprobe`: `go vet` with and without `-tags ursus07`, and built against
  v0.7.2's tree with it.

## 6. Tagging

The tag is the maintainer's, on this pull request's merge commit on master, as v0.7.0
was tagged off master's commit; annotated, as v0.7.0 to v0.7.3 are:

    git fetch origin
    M=$(gh pr view release/v0.8.0-prep --json mergeCommit --jq .mergeCommit.oid)
    git switch --detach "$M"
    GOEXPERIMENT=simd go run ./internal/gen/api -check   # "0 added and 0 gone since v0.8.0"
    git tag -a v0.8.0 -m v0.8.0 "$M"
    git push origin v0.8.0
    git switch master

If the check says some were added, a pull request merged API after this branch: then
`make api-tag TAG=v0.8.0` at the tagged commit, and a pull request for the file;
otherwise nothing follows. The tag's push publishes 0.8.0's documentation and moves
`latest` to it (`docs.yml`).

**For the maintainer to decide before tagging:**
- the CHANGELOG's date, 2026-11-20, if the tag is later;
- whether 0.8's wider gap to Polars 2.1, as the README and the report now say it, is
  how it should be said;
- the two open findings in the known limitations, which the release ships with;
- `docs.yml` moves `latest` on any `v*` tag, so a later v0.7.x tag would move it back
  from 0.8; a 0.7.x tag after v0.8.0 wants `latest` set back by a manual run.
