# Step 298 — as built

**C1: a frame's cast** (`v0.6.7-scope.md` §2), as Polars' `DataFrame.cast` and
`LazyFrame.cast`.

## 1. The API

Polars' `cast` takes a mapping whose keys are names, types or selectors, or one type
for every column. Go has no mapping of mixed keys, so it is three methods on both
frames:

- **`Cast(map[string]DataType, opts...)`:** by name. A name the frame does not have is
  refused with `uerr.UnknownColumn`, its near matches and the frame's columns, as
  Polars raises `ColumnNotFoundError`.
- **`CastTypes(map[DataType]DataType, opts...)`:** by type. A type no column has
  changes nothing, as in Polars.
- **`CastAll(DataType, opts...)`:** every column.
- **`CastStrict(bool)`:** Polars' `strict`. True by default: what does not convert is
  an error, as `Expr.Cast` answers. With false it is a null, as `Expr.CastLossy`
  gives, an overflow included.

Each is a `WithColumns` of casts, built in the frame's column order. So:

- a column keeps its place;
- the plan is the same whatever order a map hands its keys in;
- a column already of its type is left out of the plan, as Polars' plan shows
  `col("a")` alone.

**A type key matches exactly.** A `Datetime` key matches its unit and its zone, so
`Datetime(us)` casts a µs column and `Datetime(ns)` does not, as Polars'
`Datetime("ns")` does not. Polars' bare `pl.Datetime` matches every unit; ursus has no
unparameterised Datetime to be a key, and the types page says a frame of two units
needs two keys.

## 2. Held to Polars 1.44.1

**Equal:**

- the schemas of a cast by name, by names in either order, by type, by a type of no
  column, by either Datetime unit, and of every column to text;
- the values of numbers as text;
- a lossy parse and a lossy overflow, null both;
- a float with no fraction as an integer;
- the refusals of an unknown name, a strict parse failure naming `"x"`, and a strict
  overflow naming 300.

**Not equal, inherited from `Expr.Cast`.** Neither difference was in
`feature-gaps.md` §9, and both are now:

- **A float with a fraction cast to an integer** is refused, "not representable", and
  is null when lossy. Polars truncates it toward zero, strict or not: 1.5 is 1, −1.7
  is −1. ursus's casts are exact or refused by design; `Truncate(0)` first gives
  Polars' answer, as the types page shows.
- **An instant cast to text** is ISO 8601's, `2026-01-01T00:00:00.000000`, where
  Polars writes a space for the `T`. A naive instant also gains a `Z`, §1's open row,
  which now names the cast too.

The Polars page lists the first, and maps the frame's `cast` to the three methods.

## 3. Spill and stream

A cast is a per-row expression in a `WithColumns`, so it streams.
`TestFrameCastUnderAMemoryLimit` casts 20,000 rows lossily, in batches of 64, under
16 KiB, and holds them to the run without a limit.

## 4. The site

`docs/concepts/types-and-nulls.md` has "A frame's casts", marked new in 0.6.7, on
`dev` only. Its example, `Example_typesFrameCast`, casts by name, by type, and shows
`Truncate(0)` before an integer cast.

## 5. Tests and teeth

**`TestFrameCast`:** the schemas, the values, the refusals, the plan with no
same-type cast and with the frame's order over 20 runs of one map, and the eager
forms. With it, `TestFrameCastUnderAMemoryLimit`.

**Teeth, all biting:**

- `CastStrict` ignored;
- same-type casts kept in the plan;
- unknown names ignored;
- the plan built in the map's order;
- a type key matched by its kind, which casts the ns column too.

**Gate:** test-all 125 ok, race 25 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
