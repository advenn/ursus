# Step 174 — as built

**F2 of `v0.6-scope.md`: `ReplaceTimeZone`,** Polars' `dt.replace_time_zone`.

## 1. Evidence first: what Polars does

Asked of Polars 1.44.1 in the bench's environment, offline:

| question | Polars' answer |
| --- | --- |
| a naive 2024-03-31 00:30 into Europe/London | 00:30 London, 00:30 UTC: the wall clock kept, the instant moved |
| 01:30 that day, which London skips | raises "non-existent"; `non_existent="null"` gives a null |
| 2024-10-27 01:30, which London reads twice | raises "ambiguous"; `earliest` is 00:30 UTC, `latest` 01:30 UTC, `null` a null |
| 01:00 that spring day | non-existent: London goes from 01:00 GMT to 02:00 BST |
| 12:00 UTC into Asia/Tokyo | 03:00 UTC |
| 12:00 UTC, read in Tokyo, into no zone | a naive 21:00 |
| London's two 01:30 instants into London again | both kept: the same zone is the values as they are, with no fold refused |
| the same two into Europe/Paris | both 23:30 UTC, since Paris reads 01:30 once |
| New York's two 01:00 instants into London | both 01:00 UTC |
| Apia's 2011-12-30, a day the island skipped crossing the date line | every clock that day is non-existent |
| Lord Howe Island's half-hour fold, 01:30 to 02:00 | earliest at +11:00, latest at +10:30 |
| a Date; an unknown zone | refused |
| `ambiguous="foo"`; `non_existent="earliest"` | refused: the words are raise, earliest, latest, null; and raise, null |
| the unit | kept: ms stays ms, ns stays ns |

**Where Polars differs from what is built:** Polars also takes `ambiguous` as a
column, one policy a row. Here a policy is the whole column's.

## 2. What changed

**The API** (`expr_dt.go`):

- `Dt().ReplaceTimeZone(tz string, opts ...ZoneOption)`; tz `""` makes the values
  naive.
- Two string types are the options: `Ambiguous`, with `AmbiguousRaise`,
  `AmbiguousEarliest`, `AmbiguousLatest` and `AmbiguousNull`; and `NonExistent`, with
  `NonExistentRaise` and `NonExistentNull`. Both are public types in the root
  package, not aliases of internal ones, as the midpoint audit's 4.4 asks of new API.

**The plan** (`internal/expr/call.go`):

- `FnDtReplaceTimeZone` is appended to the dt block, named `dt.replace_time_zone`.
- Its args are the zone, then the two policies in Polars' words (`ZoneRaise` and the
  rest).
- `replaceZoneOut` types it `Datetime(unit, tz)`, and refuses at plan time:
  - an operand that is not a Datetime;
  - a zone the system does not know;
  - a policy outside its list.

**The kernel** (`internal/kernel/tzreplace.go`):

- Every value moves by a whole number of seconds, the old offset less the new. So the
  ticks are shifted in integers, and the time package is asked only for offsets.
- **`zoneClock`** answers a zone's offsets. It keeps the last period of constant
  offset it was asked about, so a column of nearby values asks the time package about
  once per transition, not once per row.
- **`instants(w)`** finds the instants at which the zone's clock reads w: none in a
  gap, one, or two in a fold.
  - **The fast path:** if w's reading at the kept period's offset lies more than two
    days inside that period, it is the only reading. Any other offset's reading would
    lie within two days of it, in a period where that offset is not in force.
  - **Otherwise,** every offset in force within two days of w is tried, and kept where
    it holds at its own instant. Two days covers Apia's day-long gap.
- **The same zone in and out** returns the column relabelled, as Polars answers it.
- **A null from a policy** clears that row's validity. A refusal names the wall clock
  and the option that would answer it.
- **Seconds-unit values near int64's ends** are checked: the wall clock's add and the
  final shift both. Zone lookups are clamped to about 34,000 years either side of 1970,
  which keeps the time package's own arithmetic in range.

**Not built on `wallTime`,** which the scope named. Step 145's `wallTime` resolves a
wall clock to one instant: in a fold it prefers the input's own offset, and in a gap it
gives the gap's end, which is right for `Truncate`. `ReplaceTimeZone` must instead count
the readings, so that it can raise or apply a policy. It reuses `wallTime`'s idea, an
offset kept where it holds at its own instant, and not its code.

## 3. Tests

- **`TestReplaceTimeZoneAnswersAsPolars`:** sixteen cases of Polars' answers, read back
  in UTC:
  - London's spring gap and autumn fold under each policy, in one column with nulls;
  - Apia's missing day;
  - Lord Howe's half hour;
  - zoned into zoned, zoned into naive, and New York's fold into London;
  - London's fold kept in London and read in Paris;
  - 1900, and nanoseconds.

  It also covers four plan-time refusals: a Date, an unknown zone, and a policy word of
  each kind.
- **`TestReplaceTimeZoneRaisesByDefault`:** a gap and a fold each refuse the query
  as a user error, naming the date, the clock and the option to pass.
- **`TestReplaceTimeZoneKeepsTheUnit`:** Datetime(ms) stays ms.
- **`TestZoneClockReadsEveryWallClockAsTheZoneDoes`** (kernel):
  - the fixture: wall clocks around every transition of six zones from 2010 to 2025,
    shuffled so the kept period is often the wrong one;
  - the reference: a brute force over every quarter-hour offset within fourteen
    hours;
  - it asserts that the fixture found gaps and folds in each zone that has them.
- **The contract tables:** `TestCallFnFamiliesDoNotOverlap` counts 87, and the
  evaluation contract drives the call with a zone, none, both null policies, an
  unknown zone and a wrong policy word.

## 4. Teeth

| tooth | result |
| --- | --- |
| the kept period trusted to its very ends | **bites:** the folds of London and Lord Howe, and the brute force |
| the walk starting an hour before the clock | **bites:** Lord Howe, east of UTC, and the brute force |
| earliest answering the later reading | **bites:** London's and Lord Howe's earliest |
| the same zone resolved again | **bites:** the same zone keeps a fold's readings |
| a dropped row left valid | **bites:** the spring gap, Apia, and the mixed column |
| the source zone ignored | **bites:** the three zoned inputs |
| any policy word accepted | **bites:** both policy refusals |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
