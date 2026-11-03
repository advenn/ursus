# Step 437 — as built

**v0.7.2, cut on `release-0.7`.** The bug hunt `v0.7.2-scope.md` set: steps 410 to
436, merged by pull requests #1 to #29. The maintainer chose to cut here (D31), with
milestone 2's fixes in. The finders of milestones 3 and 4 go on toward 0.7.3.

## 1. What it holds

- **Sixteen fixes** (412–425, 427, 429, 430):
  - three crashes no recover catches, from a JSON line, a Parquet footer and a Go type;
  - silent wrong answers: a decimal's exponent, a business day's range, a stream's
    empty read, a Parquet footer's row count, a naive Datetime's "Z", a streamed
    rolling window's last bits;
  - allocation far past the input: a Parquet dictionary page, and interned types never
    freed;
  - a path traversal on Windows, a schema cache a cancellation poisoned, a
    `rolling_map` that ran on after its cancel;
  - the memory ledger's slices, and its charge for text.
- **Two changed texts** (426, 428): a join's refusal, and a panic's kind in an Arrow
  stream.
- **Tests** (411, 431–435): the race job's headroom, exact heap readings, teeth for
  eleven guards, and the merges' repair.
- **The API pinned** (436): v0.7.1's 2,389 features, diffed strictly in CI. It found
  and took out one addition.

## 2. The gate

- **The light gate:** `./internal/...` and the root package in full on the release
  tree, vet, levels and `make api`, at each step and on the merged branch, where it
  found step 435's break.
- **CI:** every width, the scalar build and the race detector, on each pull request
  and on `release-0.7` after the merges.
- **PDS-H's 22 answers against DuckDB,** by `bench.yml` dispatched on `hunt/api-pin`,
  the code the release tags:
  - at SF=0.1, all 44 answers, ursus's and DuckDB's, match the DuckDB reference;
  - at SF=1, likewise.

  These are runs 41456141330 and 41456145984, on `0f08c7f`.
- **Against v0.7.1**, PDS-H at SF=0.1 over Parquet on the laptop, four threads, the two
  runners built alike (`GOEXPERIMENT=simd GOAMD64=v3`) from `git archive` of v0.7.1
  and of `0f08c7f`, alternated three rounds of five iterations a query:
  - the 22 queries took 1.01 of v0.7.1's time, 469.8 ms against 465.1;
  - the three above 1.05 were run again, five rounds: q6 1.02, q14 0.99, q10 1.04.
  - Level. 0.7.2 changes no kernel on PDS-H's path. Its costs are a type's first
    interning and a footer parsed twice, both per query or per file.

## 3. Tagging

The tag is the maintainer's: `v0.7.2` on `release-0.7`, at step 438's merge commit. That step
fixed a flaky bound CI found at #29's merge commit, so the commit tagged is one whose
CI passed. After it,
master takes the CHANGELOG's v0.7.2 section and `api/v0.7.2.txt` by a pull request, as
it took v0.7.1's (`f2d2746`). The fixes go to master one forward-port each.
