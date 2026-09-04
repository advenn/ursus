# Step 14 — as built

Calendar intervals and temporal grouping. Authoritative where it disagrees with
[`step-13-as-built.md`](./step-13-as-built.md) and the vision docs.

**1107 test cases green** — 428 top-level tests and 679 subtests (971 before this
step) — under `GOEXPERIMENT=simd` × `GODEBUG=simd={512,256,128,0}`, with the
experiment off, and under `-race`. `make levels` and `go vet` clean.

179 files, ~53.8k lines. `LazyFrame` gains two operations; `Expr` still has 94.

```go
// None of this existed. A grep for GroupByDynamic|Rolling|Interval|Every outside
// comments returned zero hits.
lf.GroupByDynamic(ursus.Col("ts"), ursus.DynamicOptions{
        Every:   ursus.Every("1h"),
        GroupBy: []ursus.Expr{ursus.Col("service")},
    }).
    Agg(ursus.Len().Alias("errors"))

lf.Rolling(ursus.Col("ts"), ursus.RollingOptions{Period: ursus.Every("7d")}).
    Agg(ursus.Col("revenue").Mean().Alias("ma7"))

ursus.Col("ts").Dt().Truncate(ursus.Every("1mo"))   // and it reads the column's zone
ursus.Col("ts").IsBetween(lo, hi, ursus.ClosedLeft)

// And a spilling group-by can finally write its own most common output.
lf.GroupBy(k).Agg(ursus.Col("n").Sum()).SinkParquet(ctx, "out.parquet")
```

---

## 1. Why this step

`dataframe-features.md` §14's v0.2 line was six of eight, and the two that remained
were exactly this family. The sharper evidence is `ursus-api.md` §16: **both of its
worked examples were unbuildable**, and a third snippet joined them — one uses
`GroupByDynamic` + `Every("1h")`, one uses `JoinAsOf` + `AsOfTolerance(Every("1m"))`,
one uses `RollingMeanBy(Col("date"), Every("7d"))`. All three are blocked on one
missing type.

---

## 2. A sorted index turns every window into a row RANGE

The naive reading is alarming: one row can belong to many windows, and every
aggregate operator here indexes group state by row — step 12's `s.groups` doc records
what happens when that array stops being dense.

But both operators require the index sorted, and that changes the shape completely:

> If rows are ordered by the index, every temporal window covers a **contiguous run
> of rows**. A window is not a set of row ids; it is a pair `(lo, hi)`.

That collapses the two operators into one mechanism and — the part that mattered most
— lets both reuse **the entire existing accumulator family unchanged**. A window's
rows are expanded into `(row, group)` pairs, gathered once, and fed to the ordinary
accumulators, so all nineteen aggregates work in a temporal window on the day the
operator lands. `TestAllNineteenAggregatesInAWindow` asserts exactly that, and it
needed no new kernel.

|  | `GroupByDynamic` | `Rolling` |
| --- | --- | --- |
| windows from | a grid: `offset + k·every`, each `period` long | one per row, ending at that row |
| exist when empty | **yes** | no, by construction |
| overlap | when `period > every` | when rows fall within `period` |
| output height | one per window | one per input row |

**The cost, stated rather than discovered.** Expansion is `O(Σ window sizes)` =
`O(n · period/every)`. A seven-day mean over daily data is 7n. The O(n) alternative is
a *sliding* accumulator, and `kernel.Accumulator` has no `Remove` — that is a second
kernel family, not a tweak, and it is deliberately not this step.

### Empty windows are the whole point

A hash group-by creates a group when a row arrives, so an hour with no rows is simply
absent and a gap in a time series is invisible. The grid is generated from the
observed `[min, max]`, so the gap is a row with a zero in it.
`TestGroupByDynamicEmitsEmptyWindows` pins both halves — four windows from the dynamic
operator, two from `GroupBy(truncate(ts, every))` over the same data.

### Sortedness is VERIFIED, not asserted

`ursus-api.md` specifies `SetSorted` as an optimizer hint. **Step 14 does not ship
it.** `plan.Pushdown`'s doc already makes the argument: *"Claim this only if it is
true. It is the one value that deletes a correctness check from the plan."* An
unchecked `SetSorted` is precisely that, and its failure mode is wrong rows with no
error. The operator walks the index in order anyway, so checking is free; it refuses
with the offending row number and tells the user to sort.

---

## 3. `Interval`, and the three rules that are not interchangeable

```go
type Interval struct{ months, days int32; nanos int64 }   // Arrow's month-day-nano
```

Three fields because each obeys a different rule, applied in that order:

**Months clamp; they do not normalise.** `time.Date(2024, 2, 31, …)` is March 2nd, so
`AddDate(0, 1, 0)` on January 31st answers March 2nd and skips February entirely.
Every calendar system clamps to the last day of the target month. This is the single
most common bug in date arithmetic and it gets a table of expectations written out by
hand — deriving them from `AddDate` would be deriving them from the thing being
corrected. The test also asserts what `AddDate` answers, so the *reason* for the clamp
is verified rather than claimed.

**Days are calendar days.** On a spring-forward day a calendar day is 23 hours; on a
fall-back day, 25. `TestIntervalDayIsNotTwentyFourHours` asserts both that the wall
clock does not move and that the elapsed time is not 24 hours — and that
`FromDuration(24*time.Hour)` gives the other answer, which is also correct, for a
different question. Storing one day as 86 400 000 000 000 nanos would collapse that
distinction where it can never be recovered.

**Nanos are absolute**, and delegate to the tick algebra `internal/expr/resolve.go`
already gets right.

`Every` carries its parse failure in the value rather than returning it, because
returning `(Interval, error)` would break the one call shape the API is for — an
option-struct field. That is the deferred-error rule the rest of the library follows,
and `TestEveryErrorSurfacesInTheQuery` checks it reaches `Collect`.

**A bug found while building it.** `TruncateTo`'s day grid first computed the day
number by subtracting two local midnights and dividing by 24h. Across a DST boundary
the elapsed time is a whole number of days *minus an hour*, so the quotient is one too
small for every date after the transition — a bug invisible for eight months a year.
The day number is now computed on the calendar in UTC and only then read back as a
local date.

---

## 4. Truncation: a Duration floors absolutely, an Interval floors on the calendar

The plan called `truncateTemporal` ignoring the column's timezone a *live bug*. Having
built it, that is not quite right and the as-built should say so: a `time.Duration` is
a fixed span of elapsed time, so flooring by one is zone-independent **by definition**.
`Truncate(24*time.Hour)` landing on 00:00 UTC is the correct answer to the question a
Duration asks.

What was missing is that there was no way to ask the *other* question. So:

```go
Truncate(time.Hour)     // the instant, floored to a whole hour since the epoch
Truncate(Every("1d"))   // the start of the LOCAL day, in the column's own zone
```

Both are asserted — `TestTruncateByCalendarIntervalIsLocal` and
`TestTruncateByDurationStaysAbsolute` — so the distinction is pinned rather than
documented, and the shipped Duration behaviour did not silently change.

The signature widened with the union constraint this codebase already uses for `Lit`
and `IsBetween`, so every existing call still compiles:

```go
type Span interface{ Interval | time.Duration }
func (d DtExpr) Truncate[T Span](every T) Expr
```

---

## 5. `Closed`, and why its zero value is none of the four

Three callers want three different defaults, and each is obviously right for its own
question:

| | default | because |
| --- | --- | --- |
| `IsBetween` | both | "between lo and hi" includes the endpoints |
| `GroupByDynamic` | left | the only convention under which a grid **tiles** |
| `Rolling` | right | a row's window ends at its own instant, so closing the left end would drop the row from its own window |

A zero value meaning one of them would silently give the wrong default to the other
two. So the zero is `ClosedDefault`, each caller resolves it once with `Or`, and no
call site inherits a convention it did not ask for.

`IsBetween` gained the argument §16 already passes it, as a **variadic** — so every
existing two-argument call compiles unchanged, and passing two is an error rather than
last-one-wins.

**One subtlety worth recording:** when the lower end is open, the grid has to reach
one step further back, or a row sitting exactly on the first boundary is in no window
at all. That is the only place the boundary convention changes the *grid* rather than
the range search, and forgetting it silently drops the first row of every
closed-right query.

---

## 6. Three `DynamicOptions` fields are refused, not ignored

`Every`, `Period`, `Offset`, `Closed` and `GroupBy` are implemented. `Label`,
`StartBy` and `IncludeBoundaries` produce an error naming themselves.

That is step 12's `MaintainOrder` lesson applied *before* rather than after: that flag
sat assigned, rendered and unread for four steps, and its doc had to say "the flag
documents the guarantee rather than changing behaviour". A field that is accepted and
does nothing is worse than one that does not exist, because the next reader believes
it. `IncludeBoundaries` additionally does not fit — `Aggregate.Schema()` has no slot
for extra output columns.

---

## 7. The Parquet type surface, closed in both directions

Two step docs had recorded this. It cost ~110 source lines and it un-blocked two
**delivered** milestones.

| | |
| --- | --- |
| **Int128 write** | one `toNode` arm → `DECIMAL(38,0)` on `FLBA(16)`. **Zero writer lines** — the 16-byte path dispatches on the arrow-go writer type and `data.TypedColumn[i128.Int128]` already succeeded on an Int128 column |
| **Datetime, both ways** | `TIMESTAMP(isAdjustedToUTC, unit)` on `INT64` |
| **Time, both ways** | `TIME(unit)` — `INT64` for micro/nano, `INT32` for milli, which needed a bespoke narrowing arm because ursus stores every `Time` as int64 ticks |
| **Duration** | still refused, on purpose: arrow-go's `IntervalLogicalType.toThrift()` **panics**, so there is no route |

**The hazard was real, and the teeth check proved it.** arrow-go's `createTimeUnit`
ends in `panic(...)` for anything but MILLIS/MICROS/NANOS, and `dtype.Second` is
ursus's **zero value**. Removing the guard does not produce a wrong answer — it takes
the process down.

**Two documented asymmetries.** `Int128` reads back as `Decimal(38,0)`, because
Parquet has no other 128-bit integer and nothing in the file distinguishes them. A
named-zone `Datetime` reads back as `UTC`, because Parquet stores only the
`isAdjustedToUTC` boolean. Both are asserted rather than hidden.

**Two tests used Datetime as "the unsupported type"** and step 14 made it supported —
exactly as step 6 predicted when the same thing happened to CSV. `TestSinkParquetIsAtomic`
now uses a **failing strict cast**, which is type-agnostic and permanently stable;
`TestParquetRefusesUnsupported` uses `Duration`, which genuinely has no route.

### And two defects step 13 left in its own test file

- `TestSpillingJoinIsBounded` justified `SinkCSV` with *"it refuses Int128 and every
  temporal type"* — true of Parquet, false of that test, whose fixture is four Int64
  columns. It sinks to Parquet now, which is what the comment was trying to avoid.
- Its fixture comment said *"20,000 build rows, forty rows each"* while the code said
  `60_000` — an edit that updated one number and not the other. The measured 4.9×
  figure in step 13's as-built came from the real fixture and stands; the comment did
  not.

`TestSpillingHashAggIsBounded` gets its `Sum` back, so it now measures the aggregate a
user would actually write.

---

## 8. Verification

`make test-all` (four SIMD widths plus experiment-off), `make race`, `make levels`,
`go vet` — all clean.

Seven teeth checks, each by reintroducing the defect:

| Reintroduced | Caught by | What it looked like |
| --- | --- | --- |
| Normalise months instead of clamping | `TestIntervalClampsMonthEnds` | Jan 31 + 1mo = **Mar 2**, skipping February |
| Drop the zone from calendar truncation | `TestTruncateByCalendarIntervalIsLocal` | rows land a whole day early, and two rows of the same local day get different buckets |
| Emit only non-empty windows | `TestGroupByDynamicEmitsEmptyWindows` | 2 windows where 4 belong |
| Treat `Closed` as always-left | `TestClosedBoundaries`, `TestRollingIsOneGroupPerRow` | `both` gave 3 where 5 belong; a rolling row dropped out of its own window |
| Skip the sortedness check | `TestTemporalGroupRefusesUnsortedIndex` | unsorted input accepted, with plausible wrong groups |
| `>=` for a rolling lower bound | `TestRollingIsOneGroupPerRow` | one extra row in every window |
| Drop the Second-unit guard | `TestParquetRefusesSecondResolution` | **a process panic** from inside arrow-go |

The oracle worth naming: `TestRollingMatchesAManualFilter` checks every row's window
against `Filter(ts > t-period, ts <= t)`, which shares no code with the operator. And
`TestGroupByDynamicMatchesTruncateGroupBy` pins the core against
`GroupBy(Col("ts").Dt().Truncate(every))` for the one configuration a hash group-by
can express — a differential against something already trusted.

---

## 9. Honest gaps

- **`AsOf` and `JoinWhere` are not done**, which leaves v0.2's line at seven of eight.
  Both prerequisites now exist: `Interval` (for `AsOfTolerance`) and a verified
  sortedness discipline. `kernel/merge.go`'s `Merger` makes `MergeSorted` nearly free
  on top.
- **Temporal types promote only to themselves.** `dtype/promote.go` says so
  deliberately, and the consequence is sharper than it looks: a `time.Time` literal
  always lifts to `Datetime(ns, UTC)`, so `Col("ts").Gt(someTime)` fails on any column
  that is not nanosecond-UTC. `TestRollingMatchesAManualFilter` had to build a
  nanosecond fixture to write its oracle. That is a real usability gap and a
  promotion-rule change with wide blast radius, so it is recorded rather than made.
- **`Expr.RollingMean(windowSize)` and the other 24 expression-level rolling methods
  are out.** They need a *frame* concept `WinParams` does not have, and `RollingMeanBy`
  additionally needs a per-row binary search no kernel provides.
- **`Upsample`, `DateRange`, `SetSorted`/`IsSorted`, `MergeSorted`** are out.
- **`TemporalGroup` has no projection-pushdown arm**, so it falls to the conservative
  default and requires every input column. Correct, not optimal — which is the
  property `rule_projection.go`'s header promises for a new node type on the day it
  lands. It joins the eight other missing arms, now deferred four times. Worth
  repeating that `README.md` undercounts those at six: `*HStack` and `*RowIndex` are
  also missing.
- **`Duration` in Parquet** stays refused; arrow-go panics rather than returning an
  error, so there is nothing to route through.
- The `O(n · period/every)` expansion is the measured shape, not a bound. A sliding
  accumulator with `Remove` would make it O(n) and is a second kernel family.

---

## 10. Files

**New:** `dtype/interval.go` (368), `internal/plan/nodes_temporal.go` (158),
`internal/physical/tempgroup.go` (608), `groupby.go` (147), `interval.go`,
`internal/expr/closed.go`. Tests: `dtype/interval_test.go`, `tempgroup_test.go`,
`temporal_test.go`.

| File | Change |
| --- | --- |
| `internal/kernel/dtfn.go` | `truncateCalendar` — month and day grids in the column's own zone; `packTicks` extracted |
| `expr_dt.go` | `Truncate[T Span]` |
| `agg.go` | `GroupBy` carries an optional `TemporalGroup`, so all thirteen shorthands work on a temporal group with no second implementation; `keyNames` excludes the index |
| `expr.go` | `IsBetween`'s variadic `Closed` |
| `internal/plan/resolve.go`, `node.go`, `rule_predicate.go`, `internal/physical/operator.go` | one arm each for the new node |
| `internal/source/parquet/types.go`, `writer.go`, `parquet.go` | §7 |
| `parquet_test.go`, `aggspill_test.go`, `joinspill_test.go` | the retargeted refusal tests and step 13's two defects |
