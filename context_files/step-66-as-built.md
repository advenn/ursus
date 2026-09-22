# Step 66 — as built

**An interval meant something the user did not type.** `v0.3-scope.md` item 5's
second half, and the last silent wrong answer I knew of that was reachable from
ordinary public API.

Three commits and this document.

Authoritative where it disagrees with the vision docs and [`design/`](./design/).

`make test-all` exit 0 (**100** package-ok lines = 20 packages × 5 SIMD configurations),
`make race` exit 0 (20), `make levels` and `go vet` clean in all three modules, PDS-H
SF=0.1 exit 0 with **22/22** matching the duckdb reference — each asserted on its own
exit code. No benchmarks.

---

## 1. Measured first

`Every` bounded **one count** — `strconv.ParseInt(s[:n], 10, 32)` — and nothing
bounded the product of that count with its unit's multiplier, or the running sum
across components.

A sweep derived from the unit table: **11 units, 81 inputs, 30 of them not
representable — and all thirty answered wrongly.** Not one was refused. The bound did
not exist, rather than existing and leaking.

| input | means | was |
| --- | --- | --- |
| `Every("357913942y")` | 357.9M years | **8 months** |
| `Every("613566758w")` | 11.7M years | **2 days** |
| `Every("5124096h")` | 585 years | **25m26.29s**, and `IsCalendar()` flipped |
| `Every("536870912y")` | 536.9M years | **MinInt32 months** |

Each with `Err() == nil`, `IsZero() == false` and `Negative() == false`, so every
downstream gate inspected the wrapped value and found it well formed. `GroupByDynamic`
planned an 8-month grid; the as-of tolerance became 25 minutes and silently
null-padded; `dt.Truncate` collapsed a column to year 0.

Three regimes, not one. Positive wraps were silent. **Negative wraps were caught and
described wrongly** — `Every("200000000y")` was refused with *"got −157913941y4mo"*
for an input containing no minus sign. Zero wraps hit the existing `it spans no time`.

## 2. The bound is each type's MAXIMUM, and that is what fixes the negation

Both halves of each accumulation are checked now — the multiply and the add are
separate failures, and `"178956970y"` fits while `"178956970y"` twice does not.

The choice worth keeping is the *direction* of the bound. `Every` holds its three
accumulators **non-negative** for the whole loop, because the parser refuses a
per-component sign — the `-` belongs to the interval and is applied once at the end.
Capping at `MaxInt32` therefore lands the negation in `[-MaxInt32, 0]`, which can
never reach `MinInt32`, the one int32 with no negation.

So the negation needs no check of its own. It costs one representable value per
component and buys an operation that cannot be wrong, and the comment there says so
rather than a guard that could not fire.

## 3. It was never only the parser

Three other routes took unvalidated input, and two of the three defects need no
overflow at all:

| route | produced |
| --- | --- |
| `IntervalOf(1, -1, 0)` | `--1mo1d`, which `Every` cannot read back |
| `IntervalOf(math.MinInt32, 0, 0)` | its own `Neg()` |
| `FromDuration(math.MinInt64)` | likewise |

Two invariants now hold of **every interval that exists**: no component at its
type's minimum, and no mixed signs. Both are enforced where values are built, and
that is what makes `String()` and `Neg()` correct without either gaining a guard.
`String()` hoists one leading `-` and negates all three components, which is right
exactly when nothing mixes signs — `Negative()` is an **OR** over the three fields,
and that mismatch was the whole of the `--1mo1d` bug.

`MonthsInterval` and `DaysInterval` are **deleted**: exported, unvalidated, called
nowhere, and the cheapest route to a `MinInt32` months. They also contradicted the
line four fields above them, which says the accessors exist so that *"nobody
constructs a half-valid Interval that skipped Every's validation"*.

**A design alternative was considered and put to the user.** `String()` can be made
*total* by dividing before negating — every quotient of `MinInt32` is representable
even though `MinInt32` is not — which would need no refusals at all and would keep
`FromDuration` accepting every `time.Duration`. It renders a mixed interval as
`1mo-1d`, honestly, but outside `Every`'s language. The decision was to refuse at
construction instead, because it keeps `Every(i.String()) == i` true of every
interval that can be built, with no exceptions for the fuzz target to carve out.
The alternative is recorded here because it is the better answer if that invariant is
ever relaxed.

## 4. The two callers that would have swallowed it

`IntervalOf`'s callers — `truncateOut` and `truncateTemporal` — check `IsZero()` and
`Negative()`, not `Err()`. Both gained an `Err()` check **ordered ahead** of the
existing one, and the ordering is the whole of it: an interval that failed to build
is zero-valued, so `IsZero` fires first and the message degrades to *"got
`<invalid>`"*, which says nothing.

It is unreachable from the public API — `DtExpr.Truncate` checks `Err()` before
either can see the value — and reachable from a hand-built IR node, which is what
drives it. Stated rather than assumed.

## 5. The instrument

**The oracle is a magnitude, and that is the point.** The obvious invariant — *"either
it errors or `String()` round-trips"* — is necessary and **not sufficient**:
`Every("357913942y")` round-trips perfectly, to eight months. The sweep re-derives
each component in `math/big` from the count and the unit and compares.

It is an **internal** test, the first in `dtype`, because `intervalUnits` is
unexported and deriving the axis from it is the point: a unit added there is swept
the day it is named. It also refuses a unit touching more than one component, since
its arithmetic assumes exactly one and a unit that broke that would be swept wrongly
rather than loudly.

**`FuzzEvery` is the repository's first fuzz target**, and it checks the *structural*
invariants rather than the magnitude — deliberately. A magnitude oracle needs to know
what the input meant, which means re-deriving digits and units: a second parser, and
two parsers are two chances to disagree. The sweep **constructs** its inputs so it
knows the intended value without parsing anything; the fuzzer is handed arbitrary
bytes and checks what holds for every interval — no panic, what parses renders and
re-parses to the same value, and what parses can be negated twice back to itself.
Its seed corpus caught `--178956970y-8mo` immediately.

`go test` runs a fuzz target's seeds as ordinary tests, so `make test-all` exercises
them five times over with no Makefile change. `make fuzz` is there for a generating
run, opt-in, because the pre-merge gate must stay deterministic and bounded.

## 6. Teeth, four of which went silent for four different reasons

| reintroduce | result |
| --- | --- |
| the multiply check dropped, the add kept | **bites** — five oracle rows |
| the add check dropped, the multiply kept | **bites** — the compound cases |
| the day check dropped | **bites, once aimed at the arithmetic** |
| the cap widened to admit `MinInt32` | **bites, once a compound case existed** |
| `IntervalOf` stops refusing mixed signs | **bites** |
| `IntervalOf` stops refusing the minimum | **bites, once a test existed** |
| `FromDuration` stops refusing `MinInt64` | **bites** |
| the `Err()` check leaves `truncateOut` | **bites, once a test existed** |

Four went silent on the first attempt and the reasons are all different, which is
worth more than the table:

- **The day tooth neutered the report, not the arithmetic.** Removing the `if !ok`
  left `addMul`'s *bounded* result in place, so the value stayed correct and only the
  error vanished. A tooth has to cut the thing that computes, not the thing that
  complains.
- **The cap tooth needed a case no single term can reach.** Landing exactly on 2³¹
  requires `v*mult == 2³¹`, and no unit's multiplier divides that with `v` inside the
  count's own int32 bound. Two terms do — `1073741824mo1073741824mo` — and without
  it, widening the cap by one is invisible to every other test in the file.
- **Two were plain coverage gaps.** Nothing asserted `IntervalOf(MinInt32, 0, 0)`,
  which is *not* mixed — nothing in it is positive — so the sign check lets it
  through and only the minimum check stops it. And nothing asserted that the truncate
  refusal says *why*: removing the `Err()` check left both sites still refusing, via
  `IsZero`, so the agreement test still passed.

One was a compile error read as a bite, and one patch failed to apply because
`gofmt` had realigned a comment. Both are the same lesson as step 65's: check that
the patch landed.

## 7. Two fixtures of mine were wrong and the tests said so

`"2000000000ms2000000000ms"` was supposed to overflow the nanosecond sum. It is
4×10¹⁵ nanoseconds — nowhere near int64 — so it tested nothing at all, and no single
millisecond term can reach the bound either, because `ParseInt` caps a count at 2³¹.
The largest pair of *hour* counts does.

And the rendering test's `MinInt32` fixture stopped wrapping the moment the parser
was bounded, which is the fix working: that half now asserts the refusal, and the
mixed-sign half, which never went through the parser, stayed as evidence for the
commit after.

I also created an empty `internal/kernel/dtfn_test.go` by appending a heredoc to a
file that did not exist — the same mistake step 63 made, and it fails the whole
package with `expected 'package', found 'EOF'`.

## 8. Still open

- **`tempgroup.go`'s grid guards exhaust silently.** The 4096 back-step loop and the
  `1<<24` window cap both fall out into `return out, nil` with no error. Reachable
  with a **valid** interval — `Every("1s")` over a column spanning more than about
  194 days hits the cap and every window past it silently vanishes; a large `Offset`
  with a small `every` exhausts the back-steps and returns **zero rows**. Unchanged
  by this step, and its own: the fix is a resource-limit policy decision rather than
  an arithmetic one.
- The bare `int64 → int32` narrowing at both `IntervalOf` call sites. Latent, because
  the only producer widens from int32 and round-trips.
- **`Every`'s image is now asymmetric**: `[-MaxInt32, MaxInt32]` rather than the
  two's-complement range. Deliberate, per §2, and the one value per component it
  gives up is what buys a total negation.
- Carried from `v0.3-scope.md`: nested Parquet write; cloud object stores; decimal
  aggregation's precision rule; `unique`/`over` not spilling; the byte-flip spill
  sweep; `Optimizer.Verify` off in `Explain`; the seventeen-site planner leak;
  `spill.Writer.Write`'s bare error; `callCache` eviction; `Pivot`; the stale
  benchmark suite.
