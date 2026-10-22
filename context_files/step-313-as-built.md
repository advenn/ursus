# Step 313 — as built

**A rolling window is placed as Polars' `offset` places it.** Not a scope item:
step 312 found that `RollingOptions.Offset` moved a window's end, where Polars'
`offset` is its start. The maintainer's goal of 2026-10-22, to match Polars' features
(`polars-parity.md`), decided it, as the maintainer chose Polars' `When` at step 311.
A break.

## 1. What Polars 1.44.1 does, probed

- **A row's window starts at its instant plus the offset.** It is a period long,
  `(t + offset, t + offset + period]` under the default `closed="right"`.
- **The default offset is minus the period,** the window up to the row.
- **An offset of `"0d"`** is a window wholly after the row.
- **The end, at a month's end, is not always the start plus the period.** For March 31
  at a period of `1mo`, the default window takes March 31, where February 29 plus a
  month is March 29. Polars ends a window at its row when the offset is negative and
  as long as the period, measuring both as `Duration::duration_ns` does: a month 28
  days. So `"-28d"` and `"-4w"` against `"1mo"` end at the row too.
- **Otherwise the end is the start plus the period,** calendar arithmetic and all.

A script tested that rule against Polars:

- 12 period and offset pairs, four closures each, 38 dates;
- "end at the row when equal by Polars' length" matched every case;
- "end at the row when exactly minus the period" missed `"-28d"` and `"-4w"`.

## 2. How

- **`RollingOptions.Offset` is Polars' offset.** Its zero value stays Polars' default.
- **`NoOffset()` is an offset of none.** Go's zero value cannot be both "unset" and
  "zero", so `dtype.Interval` gains a `given` flag, set only by `GivenZero`:
  - `Given()` is true for it and for any interval that spans something;
  - everything else sees a zero interval;
  - `Neg` keeps the flag.
- **The plan shows an offset of none,** `offset=0s`, and a given offset as before. The
  default is not shown, so no golden plan changed.
- **The sink's `rollRow`** starts the window at the instant plus the offset, minus the
  period unless one is given. It ends a period after that, or at the row when `behind`.
- **`behind` is computed once a group:** the offset unset, or negative and as long as
  the period. `sameLength` measures both Polars' way, a month 28 days, in big integers,
  since it runs once.

**The stream's other logic needed nothing.** Both ends of a row's window still rise
from row to row, so the active windows and the rows kept in reach work as they did.
Forward windows already worked, from the old positive offsets.

## 3. Tests

**`TestRollingOffsetAsPolars`,** each window listed by its values, Polars' answers:

- a month: the default, `-1mo`, `NoOffset()`, `-2d`, `-28d`, `-4w`, `-40d` wholly
  behind, closed both;
- three days: `-1d`, and `1d` closed left;
- an integer index: the default, `NoOffset()`, `-3i`, `1i`, `-2i` and `-1i`;
- the plan shows `offset=0s` and `offset=-1i`, and no offset by default.

**`TestTemporalGroupsAnswerAsTheBufferedSinkDid`** holds the old buffered sink's
answers for 40 rolling configurations drawn with a seed. Its offsets were drawn as the
window's end. Each is now passed as Polars' start, the end less the period. No drawn
case meets the ends-at-the-row rule, and all 40 answers are unchanged. The case names,
and the golden file, are as they were.

**`TestGivenZero`** covers the flag.

## 4. Docs

- **Dates and times:** a paragraph on `Offset` and `NoOffset()`.
- **Coming from Polars:** a row for `offset="0d"`.
- **The CHANGELOG:** the break, with the translation, `e` less the period.
- **`feature-gaps.md` §1:** the row is fixed.
- **`polars-parity.md`:** the item is struck.

## 5. Teeth, all 7 biting

- the default a period ahead;
- never ending at the row;
- a month counted 30 days;
- the end a period after the row, not after the start;
- `GivenZero` not given;
- the plan hiding an offset of none;
- `Neg` dropping the flag.

**Gate:** test-all 125 ok, race 25 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
