# Step 68 — as built

**A window an index column cannot represent.** Nothing related the window interval to
the index's *resolution*, so a `Date` index accepted `Every("1h")` and asked for
twenty-four windows per day that all floor to the same day.

Two commits and this document.

Authoritative where it disagrees with the vision docs and [`design/`](./design/).

`make test-all` exit 0 (**100** package-ok lines = 20 packages × 5 SIMD configurations),
`make race` exit 0 (20), `make levels` and `go vet` clean in all three modules, PDS-H
SF=0.1 exit 0 with **22/22** matching the duckdb reference — each asserted on its own
exit code. No benchmarks. The golden plans did not move: this adds a refusal, not a
rendering.

---

## 1. The characterisation was wrong twice before it was measured

This defect was described three times and only the third was right. Both earlier
versions were mine to pass on, and the correction is the most useful thing in the
step.

| said | actual |
| --- | --- |
| "the frame comes back with `Len()` 0" | `DataFrame` has no `Len()`; the frame is 24 rows per day |
| "every window is empty and every row is dropped" | rows are dropped **only** under `ClosedNone` |
| — | the default **duplicates the group** and `ClosedBoth` **multiplies the data** |

Measured, three rows over two days on a `Date` index:

| every | Closed | height | distinct starts | rows covered |
| --- | --- | ---: | ---: | ---: |
| `1h` | Left (default) | 48 | 2 | 3 of 3 |
| `1h` | **Both** | 48 | 2 | **73 of 3** |
| `1h` | **None** | 49 | 2 | **0 of 3** |
| `1m` | Left | 2880 | 2 | 3 of 3 |

All four conventions are wrong and each is wrong differently.

## 2. The invariant is DISTINCT STARTS, and finding that out took measuring

A coverage assertion — every input row lands in some window — **passes** under the
default. Every row still finds a home, because the last window of each day is the one
whose upper edge crosses into the next day, so the whole day's data lands there.

What is wrong is the grid itself: a group-by returning **twenty-four rows all
labelled 2024-01-01**, twenty-three of them empty. The property that catches all four
conventions is *one row per window **start***, and it holds for overlapping windows
too, because `Period` changes a window's width and not where it begins.

The plan for this step said to assert coverage. That would have shipped the default
case green.

## 3. Three refusals, one place

`TemporalGroup.Schema` is the only place holding both the resolved index field and
the node's `every`/`period`/`offset`, and `resolveTemporalGroup` reaches it by
calling `Schema()` — so one check covers `CollectSchema`, `Explain` and `Collect`.

All `uerr.KindType`, modelled on `nodes_asof.go`'s refusal of a calendar tolerance on
a key that is not an instant, whose comment applies verbatim with *grid* for *bound*:
**without it the interval went silently inert.**

- **An interval finer than the index's tick.** All three of `every`, `period` and
  `offset`, because each fails differently:
  - the grid duplicates a group;
  - **`Rolling` coarsens** — it never consults `Every`, so only `Period` can be too
    fine, and `Period("1h")` on a `Date` index gave byte-identical counts to
    `Period("1d")`. A window of the wrong *width* looks entirely ordinary;
  - a sub-day **`offset` is not inert**, which I had assumed: it moves the grid by a
    whole tick and leaves an extra empty leading window. No coverage or
    distinct-start assertion can see that, so it is asserted on window count alone.
- **A `Duration` index.** `IsTemporal()` admits it, so the plan resolved, the entire
  input was buffered, and `ToTime` then failed with an **`Internalf`** — *"this is a
  bug in ursus; please report it"* — for an ordinary type mistake.
- **A calendar interval on a `Time` index**, which is `truncateOut`'s refusal for a
  different operator, in the same words.

### It deliberately differs from `dt.truncate`, and says so

`truncateTemporal` **accepts** a finer-than-tick interval as a no-op, because *"every
instant is already on a boundary"*. That is right there and wrong here, and the
difference is what the answer is made of: truncate's answer is the **input**, so a
no-op is the correct floor; a grid's answer is a window **count**, and flooring
twenty-four windows onto one tick makes twenty-four indistinguishable ones rather
than one. A reader who finds both should not have to guess which is the mistake.

## 4. Two silent teeth, both of which found something

| reintroduce | result |
| --- | --- |
| the whole resolution check | **bites** — three sweep cases |
| the `Duration` refusal | **bites** |
| compare against nanoseconds rather than the index's tick | **bites** |
| the `IsCalendar` early-continue removed | **silent — and it was dead code** |
| the `Time`-plus-calendar refusal | **silent — it had no test at all** |

**The `IsCalendar` continue could not change an outcome.** A pure calendar interval
carries no nanoseconds, so the `n > 0` guard already skipped it. It is gone, and what
replaces it is a comment explaining why the comparison needs no exclusion: a **mixed**
interval like `Offset("1mo12h")` is checked on its sub-day part, which is correct,
because twelve hours is as unrepresentable on a `Date` index beside a month as it is
alone. That case is now a test.

**The `Time`-plus-calendar refusal had no test**, because the suite has never used a
`Time` index for grouping at all — removing the arm changed nothing. It has one now,
with the sub-day control that shows why the index type alone could never decide it.

Before this step the suite used exactly two index types, `Datetime(us)` and
`Datetime(ns)`. No `Date`, no `Time`, no `Datetime(s)`, no `Duration`, and no
interval finer than the index's tick anywhere — the nearest miss was `"1us"` against
a `Datetime(ns)` index, which is *coarser*, the working direction.

## 5. A repo instrument caught me

Renaming a test left a comment citing the old name, and `TestEveryCitedTestExists`
failed on it. That guard exists because this project has cited instruments that were
never built; this is the first time it has caught a citation that *stopped* being
true rather than one that never was.

## 6. Still open

- **`!next.After(start)` misattributes.** Its message names `Every`, which the
  resolver has already proven positive, so the stated cause is impossible in every
  case the guard can fire. What it catches is `time.Time` saturation — a property of
  the index instant — reachable through an `Int64 → Datetime(s)` relabel cast, which
  `unary.go` calls "the tick-count escape hatch" and does not range-check. The
  misattribution needs no measurement; the reachability is strong but unmeasured.
- `rolling`'s expansion is `O(rows × rows-per-window)` and unaccounted — an OOM
  rather than a refusal. Same accounting gap step 67 closed for the grid.
- **`Optimizer.Verify` is off in `Explain`.** `optimizer()` never sets it and
  `AssertPlan` goes through `Explain`, so **all 32 golden plans were generated and
  are checked with the schema-preservation check off**. A rule that desynchronises a
  plan renders a clean golden, `-update` checks it in, and the failure surfaces at
  `Collect` as a `KindSchema` error blaming the user's data. Two files, ~5 lines, plus
  whatever flipping it surfaces — the best value-per-line left, and now the strongest
  remaining item of any kind.
- Carried from `v0.3-scope.md`: decimal aggregation's precision rule; nested Parquet
  write; cloud object stores; `unique`/`over` not spilling; the byte-flip spill sweep;
  the seventeen-site planner leak; `spill.Writer.Write`'s bare error; `callCache`
  eviction; the stale benchmark suite.
- **Documentation debt**, sized: `Pivot` has been re-listed for twenty steps despite
  step 47 adjudicating it — the thing step 47 said it was ending. The README's "1610
  test cases" should be **2330**. Its forward-looking tail has three stale claims
  (`reverseSink` untested, `JoinWhere` unimplemented, the CSV allocation defect). Its
  opening line says execution "spills to disk rather than falling over" when three
  operators do, though the Status table below it is already accurate.

**With this closed I know of no remaining silent wrong answer reachable from ordinary
user code with ordinary data.** What is left in the defect list is a silently wrong
golden file, two misattributed errors, a resource leak on an error path, and an
unaccounted OOM. The v0.3 feature items are the work now.
