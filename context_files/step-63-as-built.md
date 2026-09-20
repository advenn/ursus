# Step 63 — as built

**The as-of join's tolerance failed open, four ways.** Step 62's as-built named it as
the last unchecked temporal arithmetic I knew of. It was that, and underneath it a
second defect that needs no tolerance at all, and a third that blames the user's data
for the engine's own conversion.

Six commits and this document.

Authoritative where it disagrees with the vision docs and [`design/`](./design/).

`make test-all` exit 0 (**100** package-ok lines = 20 packages × 5 SIMD configurations),
`make race` exit 0 (20), `make levels` and `go vet` clean in all three modules, PDS-H
SF=0.1 exit 0 with **22/22** matching the duckdb reference — each asserted on its own
exit code. No benchmarks were run and no speedup is claimed: `withinTolerance` runs
once per *left row*, not per candidate, and the two `sort.Search` calls beside it
dominate. PDS-H and h2o contain no as-of join and the single golden
(`asof_join.txt`) carries no tolerance, so no plan output could move; that was
checked rather than assumed.

---

## 1. Measured first, through the public API

`withinTolerance` decides whether a candidate row is close enough to match. It had
**three unchecked operations and three `return true`s** on six lines — and `true`
means *within tolerance*, so every way it could fail answered yes.

| fixture | why it matched |
| --- | --- |
| `Date` 1970-01-01 vs 2262-04-12, tolerance `1h` | `106752 × 8.64e13` wraps negative |
| `Datetime(ns)` `MaxInt64` vs `MinInt64`, `1ns` | the subtraction wraps to −1 |
| `Datetime(ns)` `0` vs `MinInt64`, `1ns` | `-d` leaves `MinInt64` negative |
| `Datetime(ns)` 2262-04 vs 1970, calendar `1mo` | `FromTime` fails → `return true` |
| `Duration(s)` keys 31 years apart, calendar `1mo` | `ToTime` refuses a Duration → `return true` |

The root cause of the first three is one decision: it scaled the **data up** to
nanoseconds — `d*npt`, where `npt` is 8.64e13 for a `Date` — rather than scaling the
**tolerance down**. Two ordinary dates matched a one-hour bound.

The fourth is **new since step 61**. Making `FromTime` range-check honestly flipped
that path from accidentally-*excluding* — a wrapped bound compared false — to wrongly
*including*. An accurate conversion turned a benign accident into a live defect one
caller away, which is the second time this family has done that.

## 2. The tie-break, which needs no tolerance at all

`AsOfNearest` chose between the backward and forward candidate by subtracting the two
distances signed. With `k = 0`, a backward candidate at `MinInt64` and a forward one
at `+1` — all valid instants — the left side wrapped and **nearest picked the
candidate 292 years earlier over the one a nanosecond later**, under default options
with no tolerance configured.

## 3. The fix: the tolerance is measured in the key's own ticks

Converted **once**, at sink construction: `tolTicks = Nanos() / npt`. The per-row test
is then `absDiffU64(left, right) <= tolTicks` — a comparison, and nothing else.

- **No multiply, so no overflow case to get right.** The true difference of two int64s
  lies in `[0, 2^64)`, so the unsigned subtraction holds it whatever the operands are
  — including the pair whose signed difference is `MinInt64`, where negating leaves the
  value negative and every bound then succeeds.
- **Flooring the tolerance is an identity, not a policy.** For integers `d ≥ 0`,
  `npt ≥ 1`, `T ≥ 0`: `d·npt ≤ T ⟺ d ≤ floor(T/npt)`. Wherever the old test did not
  overflow the admitted set is **unchanged**, so every behaviour change here is a
  change away from a wrong answer. A sub-tick tolerance floors to zero, admitting only
  an exact tick match — exactly as before, and now pinned by a test, since no fixture
  in the repository had a tolerance that was not a whole number of key ticks.
- `T ≥ 0` is load-bearing and **asserted** rather than assumed: the resolver refuses a
  negative tolerance three files away, and if that check moved, Go's truncating
  division would stop being a floor and the identity would fail.

**Three failures, three different answers, because they are three different failures:**

| failure | answer |
| --- | --- |
| a calendar bound past the end of the type | **saturate** |
| a calendar tolerance on a `Duration` or `Time` key | **refuse at plan time** |
| `NanosPerTick` answering false | `uerr.Internalf` |

Saturating is **exact, not conservative**: every candidate is itself a representable
tick, so a bound clamped to `MaxInt64` admits precisely the set an unbounded bound
would. That is what §4 is about.

The refusal for a non-instant key is the one `truncateCalendar` makes one layer down,
in nearly the same words — *"one month after a five-second span"* is not a question.
It is stated **twice**, though: once in the resolver and once in `withinTolerance`,
because `ToTime` *accepts* a `Time` by placing a wall clock on 1970-01-01, so a month
either side of it spans every time of day and the bound admits everything. A function
that cannot answer should not depend on a check in another file to avoid being asked.

`withinTolerance` gained an `error` return, plumbed through `nearest` into `match`.
It is expected never to fire; it exists so the function is **total**, and so there is
no `return true` left in it to explain.

## 4. A bound is not a value

`FromTime` refuses an instant the type cannot hold. That is right for a datum and
wrong for a window edge, so `dtype.FromTimeBound(t, up)` saturates beside it. Two
functions rather than a flag, because the distinction is the point: a caller storing a
value must be told it cannot, and a caller computing an **edge** wants the widest edge
the type can express.

`ToTime`'s day multiply is now checked too — a `Date` column is int32 days and cannot
reach it, but `ToTime` is exported and `dtype.Date.ToTime(math.MaxInt64)` wrapped.
That check made a **seventh** fail-open shape visible rather than fixing one: a `Date`
key whose instant cannot be represented began taking the `ToTime`-failed path, which
returned true. It went into the ratchet and came out with the rest.

## 5. The key that does not survive promotion

Both sides of an as-of join cast to one promoted key type, and that cast was
**non-strict** — which turns a value the target cannot hold into a **null**. The very
next loop reports a null key as:

```
ursus: join_asof: the as-of key "ts" is null at row 0
  filter the nulls out before joining
```

So joining a `Datetime(s)` key at 2262-04-11T23:47:17Z — one second past what
nanoseconds can hold — to a `Datetime(ns)` key blamed a **not-null column containing
no nulls**, and told the caller to filter nulls they did not have.

The cast is strict now, and `Cast` already names the value and the row. The error is
**wrapped** rather than passed through: a bare `cast: …` never mentions the join that
asked for it, and `Cast`'s own hint — *use a non-strict cast* — is advice this caller
cannot take, since following it is exactly how the value became a null.

## 6. The instruments

**The sweep** (`internal/physical/asoftolerance_test.go`) derives its key types
through the resolver's own `IsTemporal()` gate over step 61's exhaustive
`allOperandTypes()`, and asserts the filter is **neither empty nor everything** — so a
change to the resolver fails this file instead of silently sweeping nothing. 17 key
types × 10 tolerances × 24² probe pairs = **97,920 comparisons**.

**The oracle is tri-state — `within` / `outside` / `meaningless` — and that is the
whole design.** Collapsing the third into a bool is how this defect happened: a
calendar span from a `Duration` has no answer, and answering `true` makes the
tolerance inert instead of loud. The duration arm computes the **original**
nanosecond semantics in `math/big`, so it *proves* the floor identity rather than
restating the new arithmetic; the calendar arm shares `AddTo` and none of the tick
plumbing. It must not use `Time.Before/After`, which compare the internal `ext` field
as a signed int64 — the one place Go's `time` package is not modular-consistent across
the int64 range.

**My first version of it was wrong in the instructive way.** I wrote `agrees = got`
for the meaningless cases, which made them agree *by construction*, so `Duration cal`
and `Time cal` never registered as disagreements at all. Answering a question that has
no answer is itself the defect, and the oracle has to say so.

**Counters**, because a sweep of this size can go quiet: comparisons, the
`1500ms × Datetime(s)` cell, and the one that matters most — **both verdicts observed
for every key type**, since a sweep where everything matches is the bug being swept
for and one where nothing matches proves nothing.

A **two-way ratchet** of seven failure shapes held the evidence: populated red in
commit 1, emptied by commit 4. An unlisted disagreement fails; so does a listed one
that now agrees.

## 7. Teeth

| reintroduce | result |
| --- | --- |
| the unchecked multiply | **bites** — the sweep and three public subtests |
| fix only the subtraction | **bites** — the `MinInt64` arm survives as a distinct failure |
| `FromTime`'s failure meaning match | **bites** — the sweep and the 2262 fixture |
| `ToTime`'s failure meaning match, and a `Time` treated as an instant | **bites the sweep** — the public path is closed by the resolver, which is why the sweep builds the sink directly |
| the signed tie-break | **bites** |
| `planAsOfJoin` never calling `prepareTolerance` | **bites** |
| the *sweep* not calling it either | **bites** — the instrument would have tested a path production never takes |
| rounding the tolerance up instead of flooring | **bites** — the floor identity is pinned, not incidental |
| `FromTimeBound` lying instead of clamping | **bites `dtype`'s own test**, and *nothing else* |
| every probe the same instant | **bites** — 15 key types report never producing both verdicts |
| the non-strict cast, the null check removed, the wrap removed | **bite** |
| the tie-break as `<`, and "always pick backward" | **bite**, each a different subtest |

Two entries are worth more than their row. **`FromTimeBound` lying fails only the
`dtype` test** — the as-of fixtures cannot see it, because a lie in that direction
*excludes* where the truth would have admitted. The public tests would have passed a
broken bound. And **every probe the same instant** fires 15 of 17 key types, not 17.
Asking why the other two stayed quiet found §9's last entry.

One tooth was **re-aimed rather than recorded as silent**: the first version of the
vacuity tooth left an import unused, so it failed to *build* rather than firing the
counter. A build failure is not a test failing.

## 8. The fixture, not the test, is the unit of coverage

The twelfth recorded instance, and one that comes from a case that **could not
discriminate** rather than one that was wrong.

I wrote a tie-break subtest with quotes at 100 and 101 against a trade at 100,
expecting it to assert that an exact match beats a nearer-looking future. With exact
matches enabled both searches land on **the same row**, so no tie rule can tell them
apart and the case asserted nothing. It was replaced by a control in the opposite
direction — a forward candidate that is genuinely closer must win — without which
*"always pick the backward candidate"* would pass the equidistant case.

The DST test had the same shape of error in its arithmetic. I wrote it asserting that
a fixed `23h` would not reach a quote `23.5` hours earlier; across spring-forward that
gap is **22.5 elapsed hours**, so a fixed `23h` reaches it correctly and my premise was
wrong, not the engine. Rewritten, it proves what it was for: a calendar day across
spring-forward is **23 elapsed hours**, so a fixed `24h` reaches a quote the calendar
day does not.

## 9. Still open

- **`dtype.Interval.Every` accumulates unchecked.** `Every("400000000y")` wraps int32
  months into a positive 505032704 and parses successfully, so the interval means
  something the user did not type. The resolver's negative-tolerance refusal is what
  makes `tolTicks` non-negative at the sink — an invisible dependency this step
  asserts rather than removes.
- **A calendar bound past Go's own maximum time is not saturated but wrapped**, and
  this one was found by the instruments failing to be vacuous. `Datetime(s)` and
  `Datetime(s, UTC)` were the two key types that did NOT report a vacuous verdict
  when every probe was made the same instant — because at the top of the int64
  seconds range they answer `outside` for a key against *itself*.

  `Datetime(s).ToTime(MaxInt64)` is year 292277026596, which is fine.
  `Every("1mo").AddTo` of it is year 292277026597, which is **past what `time.Time`
  can represent** — Go's calendar arithmetic wraps there silently, and the result
  still prints as a plausible date while its `Unix()` has gone negative. `FromTime`
  believes it, so `FromTimeBound` returns −9223372036852097409 for an upper bound and
  the window admits nothing.

  It is the opposite direction to this step's defect — it fails **closed** — and it is
  reachable only with a `Datetime(s)` key within a month of year 292 billion. The
  sweep cannot see it because the oracle also starts from `t.Unix()`; that shared
  dependency is the hole. `FromTimeBound`'s doc now states what it actually
  guarantees: exactly what `FromTime` reports, and `FromTime` is exact for any *valid*
  `time.Time`, not a defence against an invalid one.

  A reflexivity clamp — a key is always within a non-negative tolerance of itself —
  was considered and **rejected**: it would fix the case that is easy to see while
  leaving the bound just as wrong for every other candidate, which is a worse state
  than a recorded limit.
- **The as-of build sink is never driven under a memory budget.** `joinspill_test.go`
  has no as-of coverage at all, though the sink buffers the whole right side and
  accounts its memory.
- Carried: the contract fixture's missing List/Struct columns (32 predicted
  divergences, sized); `mean(Datetime)`; `list.mean`'s hard-coded Float64; the
  byte-flip spill sweep; `Optimizer.Verify` off in `Explain`; six planners leaking an
  opened operator; `spill.Writer.Write`'s bare error; UDF name uniqueness; `callCache`
  eviction; `Pivot`; the stale benchmark suite.
