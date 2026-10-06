# Step 86 — as built

**Three temporal edges, and a doc that misquoted Polars.** These are `audit.md`'s
S18–S20, and the `Round` half of S21. They were the last measured rows in the
release notes' list of answers that can be wrong, short of S24's general case, which
is step 87's. O13, the other open row, was measured too, and not reproduced in its
plainest shape.

Four commits and this document.

Authoritative where it disagrees with the vision docs and [`design/`](./design/).

Each of these was asserted on its own exit code:

- `make test-all` exit 0, with **105** package-ok lines: 21 packages × 5 SIMD
  configurations.
- `make race` exit 0 (21).
- `make levels` and `go vet` clean in all three modules.
- PDS-H SF=0.1 exit 0, with **22/22** matching the duckdb reference.

No benchmarks.

**The suite is 2925 passing tests and subtests** (2913 at step 85).

---

## 1. What was measured, against Polars 1.44

| row | ursus before | Polars |
| --- | --- | --- |
| S18 | A strict Int64 → Time(ns) of 90000 s was `01:00`, and −1 was `23:59:59.999999999` | Refused when strict; null when lossy |
| S19 | Duration(ms) −1500 → Duration(s) was **−2**, while ursus's own `TotalSeconds` of it is −1 | −1 for both the cast and `total_seconds` |
| S20 | `Abs` and `Neg` of the minimum Duration wrapped to itself, silently | Wraps too. ursus has refused every other overflowing temporal operation since step 61, so here it diverges on purpose. |
| S21 | `Round`'s doc and its kernel's said half-away-from-zero "matches Polars" | `round(0)` is half to even: 0.5 → 0, 2.5 → 2. Half-away is `mode="half_away_from_zero"`. |
| O13 | Two typed nulls whose types render alike — `Enum("a, b")` beside `Enum("a", "b")`, and a Struct whose field name contains `": Int64, "` — each kept its own type | — |

## 2. Evidence first

**E1 is `castedges_byhand_test.go`: 11 cases, 6 wrong.** It covers the six defects
above, plus five controls that pin what must not move:

- an in-range Int64 → Time still casts;
- Datetime → Time still takes the time of day;
- a coarser Datetime still floors;
- an ordinary Duration's `Abs` still works;
- an Int64's `Abs` of its minimum still wraps, as Polars' does.

## 3. The fixes

1. **S18.** A cast to Time folds into the day only from a Datetime: that is its time
   of day. From anything else — a tick count, a Duration — a value outside the day
   is refused when strict and null when lossy. Time arithmetic wraps on its own, in
   `dispatch.go`, and never went through this fold.
2. **S19.** `rescaleTemporal` floors when the target is an instant, and truncates when
   it is a Duration, so −1.5 s is −1 s, the mirror of 1.5 s.
3. **S20.** `Neg` and `Abs` of a Duration refuse the minimum with KindValue, naming
   the row. The integer types keep wrapping, as all integer arithmetic here does.
4. **S21.** Both docs now say `Round` is Polars' `half_away_from_zero` mode, and not
   its default. The behaviour stays, because it is how a spreadsheet rounds.
   `CHANGELOG.md` lists it with the other deliberate differences.

## 4. Teeth

Every patch was checked to have applied, and every one ran against a green baseline.
**All 6 bite**, one after a re-aim.

| reintroduce | fails |
| --- | --- |
| every cast to Time folds | the strict and the lossy S18 cases |
| a lossy cast keeping values outside the day | the lossy case (re-aimed after a first patch left a variable unused) |
| a coarser Duration floors | both S19 cases |
| a coarser instant truncating too | the Datetime-floors control |
| the minimum Duration's negation unchecked | both S20 cases |
| integers checked too | the Int64-wraps control |

## 5. Behaviour changes

- **A strict Int64 → Time cast** refuses a value outside the day, and a lossy one
  makes it null. So does any cast to Time that is not from a Datetime.
- **A Duration cast to a coarser unit** truncates toward zero.
- **`Abs` and `Neg` of the minimum Duration** are refused.

## 6. Still open

- **S24 in general:** every string builder past 2 GiB in one column. That is step 87.
- **O13** beyond its plainest shape, and **I23**.
- **S26:** `MinInt // −1` wraps, as Go and Polars do.
- **The J8 remainder:** comparison against a float meets at Float64, as in Polars.
- **I24:** the non-nullable check runs only in test builds.
