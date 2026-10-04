# Step 76 — as built

**Scalar functions answer correctly.** This is `audit.md` §6's scalar rows, S5–S13:
seven silent wrong answers, a crash, and a refusal that was the wrong kind. It is the
third step of the road to 0.3, after step 75's aggregations and windows.

Twelve commits and this document.

Authoritative where it disagrees with the vision docs and [`design/`](./design/).

Each of these was asserted on its own exit code:

- `make test-all` exit 0, with **105** package-ok lines: 21 packages × 5 SIMD
  configurations.
- `make race` exit 0 (21).
- `make levels` and `go vet` clean in all three modules.
- PDS-H SF=0.1 exit 0, with **22/22** matching the duckdb reference.

No benchmarks. The golden plans did not move: none holds a truncate, which now renders
a fourth argument.

**The suite is 2707 passing tests and subtests**, counted from `go test -json`'s pass
events as at step 75 (2658).

---

## 1. What was wrong, measured

Each row was measured three ways before anything changed: ursus, through a
`go test -overlay` file so the repo stayed untouched; Polars 1.44; and DuckDB 1.5.

| row | ursus | Polars | rule chosen |
| --- | --- | --- | --- |
| S5 | `Epoch` of 1969-12-31T23:59:59.5 is 0 | −1 | floor, as Polars |
| S6 | `IsIn` cast its values to the column's type. It matched where `Eq` refuses: Int64 vs `"1"`, Bool vs `1`, String vs `1`, Date vs an instant. It refused where `Eq` is false: Int8 vs 5000, Uint64 vs −1. It rounded: Float32 vs 0.1, Datetime(s) vs 00:00:01.5. `list.contains` the same. | refuses the type pairs, false for the rest | **`IsIn(v)` answers what `Eq(v)` answers** |
| S7 | regex `Replace` wrote `$2$1` literally; `ReplaceAll` expanded it | expands | expand, as `ReplaceAll` |
| S8 | Int64 −7 // 2 = −3 and −7 % 2 = −1; **float `%` truncated too**; Duration −7s // 2 = −3s | −4, 1, 1.0; Duration // int refused | floor, and `Mod` is its remainder |
| S9 | `SplitN(sep, 0)` = `[]`, against its own doc | (returns a struct) | the doc: n ≤ 0 is unlimited |
| S10 | `Truncate(Every("1h"))` at +05:30 floored on the UTC grid: 10:47 → 10:30. `GroupByDynamic` the same. | 10:00 local | the wall clock, sub-day included |
| S11 | near Datetime(ns)'s minimum: `Every("1mo")` was ErrInternal, and `time.Hour` **wrapped to 2262** | wraps to 2262 for all | refused with KindValue |
| S12 | `CountMatches("", literal)` null; the regex form len_chars + 1 | len_chars + 1 for both | len_chars + 1 |
| S13 | `StripCharsStart/End("")` trimmed only `" \t\n\r"` | Unicode whitespace | Unicode whitespace |

Measuring found more than the audit said:

- `IsIn` disagreed with `Eq` on **186 of 255** (column type, Go type) pairs.
- The float `%` truncated as the integer one did.
- `GroupByDynamic` shared S10's floor.
- `GroupByDynamic` shared S11's ErrInternal.

## 2. Evidence first, in four commits

**E1 is `scalar_byhand_test.go`**: 41 cases, 27 wrong and listed in a two-way
`knownScalarDefects` ratchet, and 14 controls. Cases added beside the fixes bring it
to 45:
- the year 3000 and a 1500ms grid, for the new wall-clock floor;
- `GroupByDynamic` near the ns minimum;
- the sign of a zero float remainder.

`TestIsInRefusesAtPlanTime` sits beside it.

**E2 is `TestIsInAgreesWithEq`**, generated.
- **Shape:** 17 column types × 15 Go literal types, over a pool of edge values, for
  255 pairs and 1021 values `Eq` answers.
- **Oracle:** `Eq` itself. `IsIn` must refuse what it refuses, with the same kind,
  and answer the same rows.
- **Today:** 186 pairs disagreed, on 827 values.

**E3 is `TestFloorDivAndModAgainstExactIntegers`**, generated.
- **Shape:** every integer type's edges and small values, every pair.
- **Oracle:** `big.Int`'s truncated quotient, corrected to a floor.
- **Today:** the four signed types were wrong on 122 pairs. The unsigned ones were
  right, because there truncating is flooring.

**E4 is `TestTruncateFloorsTheWallClock`**, a sweep.
- **Zones:** six — UTC, Kolkata, Kathmandu (+05:45), New York, Lord Howe (a 30-minute
  shift) and Chatham (+12:45).
- **Intervals:** five — 15m, 1h, 2h, 3h and 90m.
- **Instants:** ten minutes apart around each 2024 transition, 1665 samples.
- **Oracle:** it walks back a minute at a time.
- **Today:** 19 pairs were wrong.

**The E4 oracle had a mistake, found by the fix.** In a fall-back fold it took the
latest instant whose wall clock was on the grid, even when that wall clock read
*later* than the input's. So it floored 01:00 EST at 90m to 01:30 EDT, which is not a
floor of the wall clock. Polars says 00:00 EDT. The oracle now requires the wall
clock not to pass the input's, and it was corrected in the fix's commit, which says
so. The ratchet counts it held before were measured with the mistaken oracle. They
were wrong answers either way, since the UTC grid is wrong in both readings, but
the counts would differ.

## 3. The fixes

1. **S5: `Epoch` floors** the sub-second division.
2. **S8: `FloorDiv` floors and `Mod` is its remainder**, for every integer type,
   every float and Duration: `a == b·(a//b) + a%b`, and `a%b` takes the divisor's
   sign.
   - The integer quotient is decremented when there is a remainder and the signs
     differ. That never overflows.
   - The float remainder is `math.Mod`'s exact one, corrected. A zero takes the
     divisor's sign, and x % ±Inf of a finite x of the other sign is ±Inf, both as
     in Python.
   - **Polars computes `a − b·floor(a/b)`**, which gives NaN at a ±Inf divisor and
     +0 for every zero. It is inexact for large quotients, so ursus keeps the exact
     form.
   - Two older tests pinned the truncation and moved with it: `math_test`'s float
     Mod case, and the temporal-overflow oracle, which now floors.
   - The unused `divIntScalarConst` is gone.
3. **S6: `expr.MembershipType`** is the type `==` would compare the receiver, or a
   list's element, and the values at.
   - The resolver refuses what `Eq` refuses, at plan time.
   - The evaluator casts the probe set there, and casts the column, or the list to
     `List(elem)`, when it differs.
   - Equality stays grouping equality: NaN matches NaN.
   - **This reverses step 73's documented `IsIn(0.1)` on Float32**, which is now
     false, as `Eq(0.1)` and Polars are. `IsIn(float32(0.1))` matches.
   - Step 73's cast cases and an older refusal test relied on `IsIn` parsing a
     string. They are restated under the new rule.
4. **S7:** the first match is found with `FindStringSubmatchIndex` and expanded with
   `ExpandString`.
5. **S9, S12, S13:** `limit <= 0` is unlimited; the literal arm asks `strings.Count`;
   the one-sided strips use `unicode.IsSpace`.
6. **S10: an Interval floors the wall clock.**
   - `Truncate` passes a fourth argument saying whether it got an Interval or a
     `time.Duration`. Both used to arrive as the same three numbers.
   - The kernel routes a zoned Interval through `Interval.TruncateTo`.
   - Its sub-day arm floors the wall clock, without `UnixNano`, which is undefined
     past 2262. A grid like 1500ms is floored exactly in `big.Int`.
   - It maps the floored wall clock back at the input's own offset when that holds,
     which keeps a fall-back fold. Else it uses the offset in force there. In a
     spring-forward gap it returns the instant the gap ends, found with `ZoneBounds`.
   - **`time.Date` was my first choice for the last case and was wrong.** In a gap it
     may pick either side, and picked the one before the gap: 01:00 EST for 02:00.
     E4 caught it.
   - All of these are Polars' answers, measured at NY's fold and gap and at Chatham.
7. **S11: a floor the type cannot hold is refused with KindValue**, on the calendar
   path and the tick path (`overflowsI64`). It names the instant and a coarser unit.
   `GroupByDynamic`'s window bounds get the same KindValue.

## 4. Teeth

Every patch was checked to have applied, and every one ran against a green baseline.
All 24 bite.

| reintroduce | fails |
| --- | --- |
| the truncating epoch | both S5 cases |
| the truncating quotient / remainder | E3, three S8 cases, the temporal-overflow oracle / E3, two S8 cases |
| float `%` as `math.Mod` / a zero remainder unsigned | `TestFloatModAndFloorDiv`, S8 Float64 / the zero-remainder case |
| meet at the column's type | E2, five S6 cases, `TestIsInRefusals` |
| no plan-time refusal | `TestIsInRefusesAtPlanTime` |
| the needle at the element type | `List(Int8).Contains(5000)` |
| the raw replacement | both S7 cases |
| `limit < 0` / an empty literal null / ASCII start / ASCII end | S9 / S12 / S13 / S13 |
| an Interval takes the tick path / `Truncate` always says Duration | four S10 cases and E4, each |
| `UnixNano` in `TruncateTo` | the year-3000 case |
| `time.Date` decides / decides the gap | the NY fold control, the spring case, E4 / the spring case, E4 |
| no overflow check / the calendar path internal / `group_by_dynamic` internal | the three S11 cases, one each |

**Silent at first, and fixed:**

- **The zero remainder's sign.** No case looked at it, so one was added.
- **The plan-time `IsIn` refusal.** The evaluator asks the same resolver, so removing
  the plan-time check still refused, at Collect. `CollectSchema` and `Explain` are now
  checked.

Both cases came in their own commit, after the teeth.

## 5. Behaviour changes

- **`Epoch` floors**, so instants before 1970 with a fraction move back a second.
- **`IsIn` and `list.contains` answer what `Eq` answers.** A value of a type `==`
  refuses is refused at plan time, with KindType. A value the column cannot hold
  matches nothing, with no error. Values compare at the promoted type, so
  `IsIn(0.1)` on Float32 is false. A mixed-type `IsIn` casts the column per batch.
- **`FloorDiv` floors and `Mod` takes the divisor's sign**, for signed integers,
  floats and Duration. Unsigned answers are unchanged.
- **A regex `Replace` expands `$1`, `${1}` and `${name}`.**
- **`SplitN(sep, 0)` is unlimited.**
- **An empty literal `CountMatches` is len_chars + 1.**
- **`StripCharsStart("")` and `StripCharsEnd("")` trim Unicode whitespace.**
- **`Truncate(Every(...))` floors the column's wall clock at every size**, and
  `GroupByDynamic`'s first window does too. That moves answers in zones whose offset
  is not a multiple of the interval. UTC, naive columns and `time.Duration` are
  unchanged.
- **A truncated instant, or a dynamic window bound, the type cannot hold is a
  KindValue refusal.**

## 6. Still open

- **S26, new:** MinInt // −1 wraps silently, as in Go and Polars. Int128 `//` and `%`
  are not implemented.
- **S14–S25**, unchanged.
- **The float `%` differs from Polars** at a ±Inf divisor and in the sign of a zero.
  That is a choice, recorded in §3.
- The rest of the road to 0.3: I/O's silent wrong answers next (I8–I10 and the CSV
  rows).
