# Step 315 — as built

**J1, finished:** an as-of join's numeric tolerance and its coalesce, and
`JoinWhereSuffix` (`v0.6.7-scope.md` §5). Two breaks, each toward Polars.

## 1. What Polars 1.44.1 does, probed

**A numeric key's tolerance is a number.** Keys `[1, 5, 10, 20]` against
`[0, 4, 9, 30]`:

- tolerance 1 matches `a b c`, and 20 nothing;
- 0 matches nothing, an exact match only;
- forward with 1 matches nothing; nearest with 2 matches `a b c`;
- 1.5 on integer keys is 1.

A float key's distance is inclusive: 1.0 meets 0.9 under 0.1.

**Zero is exact for a time too:** `"0s"` and `timedelta(0)` match only an equal
instant.

**Polars takes and does odd things with:**

- a negative tolerance, which it searches with as though it were positive;
- a number on a Datetime key, which it reads as ticks;
- a time on an integer key, where it panics.

**Coalesce:**

- the ordering key is merged when both sides name it alike and `coalesce` is true,
  the default;
- `coalesce=False` keeps the right one, `k_right`;
- keys named apart are both kept, whatever `coalesce` says;
- the by-keys are always merged, `gg` dropped beside `g`.

**`join_where(suffix="_r")`** names a colliding right column `x_r`, which the
predicates use.

## 2. How

### The tolerance

`AsOfTolerance` is generic over `AsOfToleranceValue`, which is `Interval`,
`time.Duration`, `int`, `int64` or `float64`. Existing calls compile unchanged.

The plan carries:

- `Tolerance` and `ToleranceGiven`;
- `NumTolerance`, an `AsOfNumber`: an integer exactly, or a float.

`resolveAsOfJoin` refuses:

- a negative or NaN number;
- a number on a key that is not numeric, which Polars would read as ticks;
- a given interval on a non-temporal key, the zero one included.

The physical sink:

- A given zero interval leaves `tolTicks` 0, an exact match. Unset, it is unbounded,
  as before.
- A number on a float key compares `floatDistance` with the float.
- A number on an integer key compares the absolute difference with the bound,
  floored, as a distance of integers is whole. An integer bound is exact; a float
  one saturates past 2^64.

### The coalesce

`AsOfCoalesce(b bool)` maps true to `CoalesceAuto` and false to `CoalesceOff`.

`AsOfJoin.Layout` merges the ordering key only when `mergesOn` holds: not
`CoalesceOff`, and both sides' bare columns of one name. Otherwise:

1. it lays the join out with the by-keys alone as its pairs, so both ordering keys
   stay, the right one suffixed on a collision;
2. it keeps the full join's `KeyTypes`, the ordering key's first, for the physical
   sink.

### The label

It shows a number, a given zero, and `no coalesce`.

### `JoinWhereSuffix(other, suffix, preds...)`

`JoinWhere`, with a `JoinSuffix` passed to its cross join. Both go through
`joinWhere`. The godoc's old advice, to write a cross join and a filter by hand, now
points to it.

## 3. Breaks

- **A zero tolerance is exact.** `AsOfTolerance(FromDuration(0))` admitted any
  distance, Go's zero being "unset".
- **Ordering keys named apart are both kept.** `AsOfLeftOn(ts)` with
  `AsOfRightOn(quote_ts)` merged `quote_ts` into `ts`. No test held the old shape.

## 4. Also fixed

`JoinAsOf`'s godoc said forward and nearest hold the whole right side. They have
streamed since step 194, holding the rows read ahead. The joins guide said only that
a backward as-of join streams.

## 5. Tests

**`TestAsOfToleranceAsPolars`:**

- integers at 1, 0, forward 1, nearest 2, 1.5, an `int64`;
- floats at 0.1 and 1;
- a zero time met and missed, `time.Duration(0)` and `FromDuration(0)`, and none;
- the plan's `tolerance=5`, `no coalesce` and `tolerance=0s`;
- refused: negative, a number on an instant, a time on an integer.

**`TestAsOfCoalesceAsPolars`:**

- `AsOfCoalesce(false)`'s answer, `k_right` and all;
- the columns of the default, of `true`, of keys named apart with and without
  `true`, and with by-keys named apart or alike.

**`TestJoinWhereSuffix`:** Polars' answer, and an empty suffix refused.

## 6. Teeth, all 12 biting

- a given zero unbounded;
- a float measured in ticks;
- an integer's bound one more;
- a number on an instant taken;
- a negative number taken;
- the key always merged;
- coalesce off ignored;
- keys named apart merged;
- the key types the by-keys';
- a duration's zero not given;
- `JoinWhereSuffix`'s suffix dropped;
- the plan hiding a number.

Two first failed the build, an import and two variables left unused, and were
rewritten.

**Gate:** test-all 125 ok, race 25 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
