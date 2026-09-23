# Step 67 — as built

**A window grid that gave up, and said it was finished.** Two loop guards in
`gridWindows` exhausted into `return out, nil`, handing back a truncated grid as
though it were complete.

Three commits and this document.

Authoritative where it disagrees with the vision docs and [`design/`](./design/).

`make test-all` exit 0 (**100** package-ok lines = 20 packages × 5 SIMD configurations),
`make race` exit 0 (20), `make levels` and `go vet` clean in all three modules, PDS-H
SF=0.1 exit 0 with **22/22** matching the duckdb reference — each asserted on its own
exit code. No benchmarks. The golden plans `group_by_dynamic.txt` and `rolling.txt`
did not move.

---

## 1. Measured first

200 rows spanning three hours, through the public API:

| every | offset | windows | rows covered |
| --- | --- | ---: | --- |
| `1s` | `30m` | 10800 | **200 of 200** |
| `1s` | `2h` | 7696 | **142 of 200** |
| `1s` | `10h` | 0 | **0 of 200** |
| `1m` | `100h` | 0 | **0 of 200** |

The `30m` row is the control: the same shape needing 1800 back-steps covers
everything, so the mechanism is precisely the 4096 bound and not the offset. The last
two are the starker failure — **a frame of two hundred rows comes back empty, with
`err == nil`.**

A row that falls in no window disappears because `Finish` pushes from windows to
rows rather than the reverse: it walks the grid and appends the rows each window
covers, so an index no window covers is never appended, never reaches an
accumulator, and never influences a cell. Nothing compares the covered count against
the input height. The operator already refuses a null index and an unsorted index,
and both doc comments give the reason that applies here word for word: *"Dropping it
would change the row count with no error."*

`Offset` had **no validation** beyond `Err()` — no sign, no magnitude, not even the
single-component rule `Every` gets — and **nothing in the repository had ever set
it.** `grep "Offset:" --include=*_test.go` returned nothing.

### What was not measured, and why

The `1<<24` cap needs 16.7 million windows to fire. A `window` is 24 bytes, so the
grid alone is 384 MiB and the query peaks near 1.3 GiB. It was read from the code
rather than run — but the step produced its own measurement anyway: **the tooth that
restores truncation takes 3.43 seconds**, because it builds all sixteen million
windows before returning. That is the cost the fix's prediction avoids, and it
confirms the important half of the claim: the cap **returns**, it does not die.

One survey prediction was wrong. `Offset("1000y")` does not come back empty; it
errors with *"cannot store Datetime(ns, UTC)"*, because a thousand years past 2024
leaves the nanosecond range. The in-range shape that does return empty is
`Offset("10h")` against a three-hour span.

## 2. One quantity, one ceiling

Both guards asked the same question — how many grid points does this query want —
with two unrelated magic numbers and no answer.

**4096 was not a number anything derives.** `Every("1s")` with `Offset("2h")` needs
7200 steps and is an ordinary request, so the cap stopped early and left the
invariant the loop exists to establish quietly broken. It counts against the ceiling
now, refuses at it, and the 7200-step query works.

**`1<<24` is kept as `defaultMaxWindows`.** The value was never the problem; the
silence was. It is the same number `internal/source/csv/scanner.go` uses for
`defaultMaxRecord`, and that is the precedent: a named constant, a refusal, and a
hint naming the knob. `uerr.KindResource`, whose own doc calls it *"a limit ursus
refused to exceed rather than a request it could not understand"*, so
`errors.Is(err, ursus.ErrResource)` tells a caller to change `Every` rather than
their data.

**The count is predicted, not discovered.** For a fixed interval it is a division.
Without that the ceiling would be honest and expensive — 384 MiB spent to say no,
which is what the 3.43-second tooth measures. A calendar `every` cannot be divided
into a tick range and cannot reach the ceiling without a span of millennia, so it
falls back to counting as it walks.

**No closed form for the back-step**, though it was tempting. `gridWindows`' own doc
says the grid is built by repeated addition *"because a calendar interval does not
multiply — three months added one at a time from January 31st clamps at each step,
and `3*1mo` has no meaning that agrees with it."* A back-step computed as `k*every`
would disagree with the forward walk for a month interval whose offset carries days.
Iteration stayed; only the bound changed.

The back-step also gained the non-advance check the forward walk already had.
Without it a step that fails to move is an infinite loop rather than a bounded one.

## 3. Accounted, like everything else derived

Nothing derived by this operator was accounted: not the grid, not `starts`,
`bucketOf`, `rows` or `groups`. `WithMemoryLimit` did not stop a two-row frame asking
for sixteen million windows.

Every sibling does it. `windowSink` retains `cap(perRow)*4` plus accumulator bytes
and calls `Check()`; `budget.go`'s own `RetainBytes` doc names three such retentions,
and the temporal grid should have been a fourth. It is retained on **capacity rather
than length**, which is `windowSink`'s `stateBytes` shape: `append` grows
geometrically, so that is a handful of retains over the whole walk rather than one
per window.

**The ceiling is query-wide.** `Finish` generates a grid per categorical bucket, so
one that reset per call would let fifty buckets build fifty times it — a limit that
scales with cardinality is not a limit.

**`holdsHint` gains `group_by_dynamic` and `rolling`.** They fell to the default,
*"%s buffers its whole input; raise the limit"*, which is exactly the falsity
`holdsHint` was written for when that line was wrong about `tail`. It is wrong here
in the same way and worse: the input can be two rows while the grid is sixteen
million windows.

## 4. Keeping the tests cheap was a design constraint, not a nicety

These are large-input defects and the machine this runs on cannot afford the 1.3 GiB
the cap needs. Three things made the suite cheap:

- **Two-row fixtures reach a large grid**, because the window count comes from
  `span/every` and not from the row count. Two rows a year apart ask for 31.5 million
  windows.
- **The prediction makes a refusal instant.** Every refusal test would otherwise cost
  what the tooth costs.
- **The query-wide ceiling is asserted where it is cheap.** Proving it end to end
  means building sixteen million windows — the first version of that test took 1.96
  seconds and was thrown away. The arithmetic is asserted directly in
  `internal/physical` instead, and the public test covers the same property through
  the memory limit, where one bucket's grid fits and three do not.

The whole file runs in 0.26 s.

## 5. Teeth

| reintroduce | result |
| --- | --- |
| the back-step cap at 4096 | **bites** — three coverage cases |
| the forward ceiling truncating instead of refusing | **bites**, in 3.43 s, which is the measurement |
| the ceiling made per-call | **bites** — the internal arithmetic test |
| `RetainBytes` dropped | **bites** |
| `Check()` dropped, `RetainBytes` kept | **bites** — proving the retain alone is inert |
| the `holdsHint` cases removed | **bites, after re-aiming** |

**The `holdsHint` tooth went silent, and the reason generalises.** The test drove a
grid so large that the window ceiling's own prediction refused it first, so the
budget — and therefore the hint — was never reached. A refusal test has to be aimed
at *the* refusal it means to assert, not merely at some refusal. Re-aimed at a grid
under the ceiling and over the limit, it bites. This is the third step running where
a tooth that looked silent was actually pointed at the wrong thing.

## 6. It is a one-off, and that was checked

A module-wide sweep for the same pattern — a counted loop whose bound is a synthetic
cap rather than a data length or a user knob — found **exactly these two**, across
`internal/`, `dtype/`, `i128/` and the root package. Everywhere else either errors on
exhaustion (`optimize.go`'s `maxIterations`, `extagg.go`/`extjoin.go`'s
`maxSpillDepth`, `scanner.go`'s `defaultMaxRecord`) or the bound *is* the documented
semantics (`InferRows`). So the scope was exactly these two plus the mechanism their
fix needed, rather than a family.

Worth recording that this cut against the codebase's own stated culture:
`data/column.go` argues a structural violation must panic *"rather than a clamp"*,
and `dtype/interval.go` and `physical/eval.go` both say an unreachable guard is
"protection that is not there". These two were *reachable* and protected nothing.

## 7. Still open — three more in the same operator

Surveyed, deliberately left, and each its own shape:

- **`Every` finer than the index tick.** A `Date` index with `Every("1h")` floors all
  24 windows per day to the same day number, so `rangeOf` returns an empty range for
  every one and **the whole frame comes back with `Len()` 0**. No cap involved and no
  large data needed — arguably worse than either guard. A plan-time check in
  `resolve.go`, beside the `Offset` validation that is also missing entirely.
- **`!next.After(start)` misattributes.** Live, but its message names `Every` when the
  cause is an index instant near `MaxInt64` seconds, reachable through an
  `Int64 → Datetime(s)` relabel cast.
- **A `Duration` index** is admitted by `IsTemporal()` at plan time and then refused
  by `ToTime` with an `Internalf`; a plan-time `KindType` refusal is what belongs.

Also `rolling`'s expansion is `O(rows × rows-per-window)` and unaccounted, so a
`Period` covering most of the data is an OOM rather than a refusal. Same accounting
gap, different loop.

Carried from `v0.3-scope.md`: decimal aggregation's precision rule; nested Parquet
write; cloud object stores; `unique`/`over` not spilling; the byte-flip spill sweep;
`Optimizer.Verify` off in `Explain`; the seventeen-site planner leak;
`spill.Writer.Write`'s bare error; `callCache` eviction; the stale benchmark suite.

Two list-hygiene items: **`Pivot` should come off** — step 47 adjudicated it and the
standing list has re-listed it since, which is the thing step 47 said it was ending.
And the README's **"1610 test cases" is stale**; the suite is at **2330**.
