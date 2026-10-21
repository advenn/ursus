# Step 304 — as built

**P2: `Unpivot` without `On`** (`v0.6.7-scope.md` §3), as Polars' `unpivot(on=None)`.

## 1. What Polars 1.44.1 answers, probed

| call | Polars |
| --- | --- |
| `unpivot(index="id")` | every other column melted |
| `unpivot()` | every column melted, no index |
| `unpivot(on=["a", "b"])` | no index column at all |
| `unpivot(index=every column)` | no rows; the value column of the Null type |
| `unpivot()` of a frame of no columns | `variable` String and `value` Null, no rows |
| text beside numbers | the value column String, the numbers as text |
| a Boolean beside numbers | a number, `true` as 1 |
| an Int64 beside a Float64 | Float64 |

Polars' `on=[]`, an empty list rather than None, melts nothing; Go cannot tell an
empty slice from no slice in an options struct, so ursus has only the None meaning.

## 2. What changed

**`LazyFrame.Unpivot`** with `On` empty writes it out against the frame's schema
when called: every column not in `Index`, in the frame's order. With `Index` empty
too, every column. The plan node then names what it melts, as before, so projection
pushdown, which keeps `On` whole, needs nothing new. A `DataFrame`'s `Unpivot` is the
lazy one.

**`plan.Unpivot` accepts an empty `On`,** which is now only Index naming every
column, or a frame of none. Nothing is melted, no row comes out, and the value
column is of the Null type, as Polars answers. The physical operator builds that
column empty rather than concatenating no columns. The node's label reads
`UNPIVOT nothing`.

**The refusal "unpivot needs at least one column to melt" is gone.**
`TestUnpivotRefusals`' case of no options now melts every column, and is refused for
its text beside numbers, "no common type for Int64 and String". Polars would write
the numbers as text: §9.

**The docs** of `UnpivotOptions`, `Unpivot` and `plan.Unpivot` said On was required,
for Unnest's reason: a column the source adds would start being melted. The scope
chose Polars' default, and the docs now say what it costs. On is written out when
`Unpivot` is called, so which columns melt is the source's, not the query's. Unnest,
which consumes its columns, still refuses to default them.

## 3. Recorded in §9

Both differences were there before; neither was recorded:

- **`Index` left out keeps every column `On` does not melt,** where Polars keeps
  none. `Unpivot`'s doc chose it, so a column added to the source is kept.
- **The melted types meet exactly,** as `ConcatStrict` meets them. An Int64 beside a
  Float64, a Boolean beside a number, and text beside a number are refused, where
  Polars' supertype makes a Float64, a number and text.

With `On` defaulted, the second comes up more often. A frame of integer and float
columns melted whole is refused. The refusal names both types and how to meet them:
cast first.

## 4. Found on the way

**`TestUnpivotKeepsItsColumnsAliveThroughPushdown` logged** "projection pushdown now
prunes through Unpivot; update this test", and did so at HEAD too. `pushdownUnpivot`
has pruned an unnamed column for many steps, and the test only logged. It now
asserts the pruning: `region`, neither melted nor named, is gone from the plan.

## 5. The site

`docs/guide/reshaping.md` on `dev`:

- `On` left out, new in 0.6.7;
- the exactness of the value type, with Polars' text for text beside numbers;
- `Example_reshapeUnpivotRest`: three quarter columns, an Int32 among two Int64s,
  melted by naming only the index.

## 6. Tests and teeth

**`TestUnpivotWithoutOn`,** each answer Polars 1.44.1's, in ursus's row order:

- every column not in `Index`;
- every column;
- named columns;
- the plan naming what it melts;
- an index of every column, eager and lazy, and a frame of no columns: no rows, the
  value column Null.

**Teeth, all biting:**

- On not defaulted;
- Index melted too;
- nothing melted refused;
- nothing melted a String;
- nothing melted concatenated;
- pruning through Unpivot lost, which the reworked assertion and three older tests
  catch.

**Gate:** test-all 125 ok, race 25 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.

**The first two test-all runs failed,** each time on `TestTemporalMediansSpillTheirValues`
under `GODEBUG=simd=0`: "the live heap grew by 12 MB under a 2 MB limit", 14 MB the
second time. Other work held the machine at a load of 18 to 24.

- The test samples heap object bytes every millisecond with `GOGC` at 10, so garbage
  the collector has not reached counts.
- Run alone in the same configuration, it passed at a load of 17. So did the whole
  test-all, run again at a load of 3 to 9.
- Nothing in this step touches it. It is a flake under contention, in §8's row,
  which step 292 opened; step 306 folded a duplicate §1 row into it.
