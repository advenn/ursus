# Step 419 — as built

**A business day's `Add` past any Date or Datetime is refused, not wrapped.** H2 of
`v0.7.2-scope.md`, confirmed by its prober, the plan's 427, decided as D10
recommends: ursus refuses, as it refuses a Date's add past its range, where Polars
1.44.1 answers null.

## 1. What was wrong

`BusinessDays.Add` jumps whole weeks, `nd := d + 7*w`, with no check: a count of
`MaxInt64` business days wrapped the sum, and `build` then wrapped `day*86_400` again,
so a Datetime came back a wrong value and no error. H2's prober found
2023-12-31T04:44:24 from Monday 2024-01-01. A Date's day past ±2^31 was already
refused by `build`'s range check, where the wrapped day landed outside it.

## 2. The change

`n` business days are at least `n` days, so a count past 2^40 is past every Date and
every Datetime: `Add` stops at 2^40 days from `d`, where `build`'s own range checks
refuse it, before anything can wrap. A guard in `build` itself was written and taken
out: `dt.replace`, its other caller, bounds its year to ±1,000,000 first, so no caller
reaches it, and untested code is not kept.

## 3. Tests

- **`TestAddBusinessDaysPastTheRangeIsRefused`** (root): from Monday 2024-01-01, a Date,
  a Datetime(ns) and a Datetime(ms) plus 10^18, −10^18, MaxInt64, MinInt64+1 and 2^41
  business days, each refused as an `ErrValue`; plus 5, the next Monday. Before the
  change both Datetimes plus ±MaxInt64 answered.

**Teeth: 1 of 1 bites:** `Add`'s stop gone answers the Datetimes again.

**Gate:** the root and kernel tests of business days, `dt`, dates and replace; vet.
CI's every width, the scalar build and the race detector on the pull request.
