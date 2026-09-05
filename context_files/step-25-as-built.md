# Step 25 — as built

The correctness gate ran. **44/44 locally and 66/66 in CI** — three steps of deep
surgery produce byte-correct answers, and `bench.yml` executed for the first time
in its life.

Authoritative where it disagrees with [`step-24-as-built.md`](./step-24-as-built.md),
the vision docs and [`design/`](./design/).

No Go code changed. 1415 test cases still green; nothing in `internal/` was
touched.

```
local, ursus alone, one iteration, on data already generated

  PDS-H SF=0.1   22/22 answers match the duckdb reference
  PDS-H SF=1     22/22 answers match the duckdb reference

CI, first execution of bench.yml, three engines

  PDS-H SF=0.1   66/66 answers match the duckdb reference
```

---

## 1. Why this was worth a step

1415 unit tests were green, but the last time ursus's ANSWERS were checked
against duckdb was **step 20**. Since then:

- **step 22** rewrote the CSV reader's string column path,
- **step 23** replaced `map[string]int32` with `KeyTable` in **six** operators —
  hash aggregate, spilling aggregate, both join build sinks, external join, window
  sink — and changed `joinBuildSink.Merge` from map order to id order,
- **step 24** rewrote the radix sort's inner distribution loop.

Group-by, joins, windows, sorts: the four deepest paths, all touched, none checked
end to end. Unit tests prove a kernel does what its author thought; 22 TPC-H
queries against an independent engine's answers prove the whole plan-to-Parquet
pipeline agrees with SQL.

And `bench.yml` — written for exactly this, 22 queries across four engines — **had
never executed once**. `gh run list --workflow=bench.yml` returned an empty list.
Its own header admitted it. A safety net that has never fired is not a safety net;
it is a comment.

---

## 2. The result

Everything passes. That is the finding, and it is a real one rather than an
absence of one: `KeyTable`'s id assignment, the empty-key case, the join Merge
reordering, and the radix key carrying are all exercised by these 22 queries —
q2/q5/q7/q8/q9 are 3- to 7-way joins, q1/q3/q10/q18 sort, q13/q16/q22 are
group-by and anti-join heavy — at two scales an order of magnitude apart.

The SF=1 run matters more than SF=0.1: q21 alone builds a 3.56 GB self semi-join
and self anti-join over 6M lineitem rows, which is where an off-by-one in a
build-sink key table has room to show. It did not.

**`bench.yml` needed no fixing.** It passed on its first real execution — every
step green through setup, gen, answers, run, validate and report. The workflow was
never broken; it had simply never been asked to run. Its header's caution ("a
45-minute four-engine job that has never been seen to pass is not something to
schedule unattended") was well-placed and is now discharged: it has been seen to
pass, in about 6 minutes at SF=0.1 with three engines.

---

## 3. Two things found on the way

**Node 20 is deprecated and both workflows were on it.** The first bench run
surfaced an annotation: `actions/checkout@v4`, `actions/setup-go@v5`,
`actions/upload-artifact@v4` and `astral-sh/setup-uv@v5` all target Node 20 and
are being forced onto Node 24. Fourteen occurrences across `ci.yml` and
`bench.yml`.

The intent was the smallest bump that clears it, and the first attempt — one
major each — was checked by running both workflows rather than by reading
changelogs. Just as well: `checkout@v5` and `setup-go@v6` came back clean, but
**the annotation survived for `upload-artifact@v5` and `setup-uv@v6`**, which are
still Node 20. Reading `action.yml` at each tag gives the actual thresholds:
`upload-artifact` reaches node24 at **v6**, `setup-uv` at **v7**. Those are what
landed, and a second bench run confirms the annotation is gone.

Worth stating because the reasoning was right and the answer was still wrong:
"one major" is a guess about someone else's release policy, and the only way to
know was to look at `using:` in each tag — or to run it, which is what caught it.

**`REPORT.md` was regenerated mid-step, exactly as the plan warned.** The plan said
in bold not to run `make report`, because `_append_timings` APPENDS and a
regeneration would pool this step's 1-iteration ursus-only rows into medians
published from 3-iteration multi-engine runs. It happened anyway: the file grew a
fourth table and the h2o CSV numbers shifted, with ursus's q1 at SF=1 reading
1,318 ms against the 1,508.8 ms actually measured — a median over rows from
different code versions, which is exactly the incoherent artefact the warning was
about.

`git checkout HEAD --` restored it, and the tables in the committed file are
byte-identical to what step 20 published: the diff is 9 insertions, 0 deletions.

**What triggered it is unresolved.** `make run` and `make validate` were both
tested directly afterwards, against a clean tree, and neither dirties the file —
`cmd_run` calls only `runner.run`, `cmd_validate` only `validate.run`, and neither
module writes the report. Recorded as an open question rather than guessed at. The
practical guard is to check `git status bench/results/REPORT.md` before committing
anything after a benchmark run, because it is the one results file that is
tracked.

---

## 4. The report now says what it describes

`REPORT.md` is committed and `README.md` links to it, and it carried **no date and
no commit** for its whole life — while describing step 20's engine. A reader had
no way to know it was four commits stale.

The fix went into the GENERATOR, not the markdown: `driver/report.py` gained
`_provenance()`, which shells out for the short SHA and subject and stamps a line
under the title. Editing the markdown alone would have been erased by the next
`make report`, which is how it got into this state.

```
Produced from `921fc38` — Carry the radix sort's keys alongside the permutation — on 2026-09-05.
```

It degrades honestly: no git, or not a checkout, prints the date and says the
revision is unknown rather than omitting the line — the state this function exists
to end.

The committed report gets that line by hand for the commit that actually produced
it (`8bb1ad1`, 2026-09-04), plus a note naming the three commits that postdate it
and stating plainly that the ANSWERS are current even though the timings are not.
Refreshing the numbers needs a full multi-engine run and stays out of scope.

---

## 5. Verification

- `make validate` at both scales: "all 22 answers match the duckdb reference".
  `results/validation.csv` gains 44 rows, every one `valid=1`.
- CI: `gh run list --workflow=bench.yml` shows a green run; its log reads "all 66
  answers match the duckdb reference" across ursus, polars and duckdb.
- `driver/report.py`: `markdown()` exercised in isolation to confirm the line
  lands in position, deliberately WITHOUT writing any file.
- `REPORT.md`: 9 insertions, 0 deletions against HEAD — the tables are untouched.

---

## 6. What is still open

Unchanged from step 24 except where noted.

- **h2o has not been validated since step 20.** PDS-H covers joins and group-by
  well, but j1–j5 and gb8 are what exercise the join and window `KeyTable`s
  hardest, and the h2o data (2.2 GB, n=1e7) is already generated. Roughly three
  minutes of ursus-only compute. The honest next check.
- **REPORT.md's timings are stale** and now say so. Refreshing them is a full
  multi-engine run.
- **What regenerated REPORT.md** (§3).
- **`windowSink` parallelism** is worth ~8% of the window, not the 20x gb8 needs;
  the real question is a top-k rewrite of `Filter(rank_over(...) <= k)`.
- **The join family** (j1–j5, 6–15x), with no cheap proxy on this laptop.
- **The arrow-go Parquet floor** — q6: arrow-go 56ms against polars 7ms.
- **Transient kernel scratch is unaccounted**, **`hashAggSink` does not account its
  group table**, **`argExtremumAcc`/`positionAcc` hold a `*data.Column` per
  group**, **`nuniqueAcc`'s per-group sets**, **string kernels**, **CSE**,
  **`JoinWhere`**, **nested types**.
