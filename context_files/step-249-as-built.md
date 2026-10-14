# Step 249 — as built

**B1–B3 of `v0.6.5-scope.md`: the bugs and stale documents `feature-gaps.md` §1
found.** The first step of feature completeness.

## 1. B1: `FillNull` with a strategy

**The bug.** `lf.FillNull(ursus.FillForward)` compiled and filled every null with the
constant 2:

- `FillStrategy` is a `uint8`, so it satisfies `Operand` (whose constraints are `~`
  types, so that a user's named numeric types fill too);
- the frame-level `FillNull[T Operand](v T, …)` took it as a literal value;
- `Expr.FillNullWith(ursus.FillForward)` fell into the same trap.

**The fix:**

- **both generic entry points recognise a `FillStrategy`** (`any(v).(FillStrategy)`)
  and fill by it: the frame through `Expr.FillNull(s)` on each column, the expression
  by calling it.
- **a strategy's columns, when no subset is named** (`FillStrategy.frameColumns`):
  - forward and backward fill every column;
  - min and max every column of an ordered type, through `expr.FieldMatcher`, which
    nothing had built until now;
  - zero, one and mean every numeric column, as `lf.FillNull(0)` does.
- **Where Polars differs,** checked against 1.44.1 on the same frame: Polars applies
  each strategy to every column. Its `zero` fills a String with `""` and a Bool with
  `false`, and its `one` and `mean` refuse a String. Here those columns are left
  alone.
- **The limit:** Polars' frame `fill_null(limit=)` has no form here. It stays on
  `Expr.ForwardFill(n)` and `BackwardFill(n)`, per column. `FillStrategy` is an enum,
  and the frame form's arguments are its value and a subset.
- **`fillEach`** takes a matcher (`typesMatcher` for a list of types) instead of a
  list of types.

## 2. B2: `Dt().Date()` and `Dt().Time()`

- **What they are:** Polars' `dt.date` and `dt.time`, the date and time of day on the
  column's zone's wall clock. 2024-01-01 23:30 UTC, read in Asia/Tashkent, is
  2024-01-02 04:30. `Cast(Date)` gives the UTC date, as Polars' `cast` does, and
  stays as it is. `feature-gaps.md` had listed it as a bug, and is corrected.
- **The functions:** `FnDtDate` and `FnDtTime` in `internal/expr`.
  - Date takes a Date or Datetime and answers a Date.
  - Time takes a Time or Datetime and answers a Time in the Datetime's unit. Polars
    answers nanoseconds; the unit keeps the value exact and the type the user chose.
    A Date is refused: it has no time of day.
- **The kernel:** `localDateTime` reads each instant through `ToTime` and
  `locationOf`, as the component extractors do. A Date's date, and a Time's time,
  are themselves.

## 3. B3: stale documents

- **`DynamicOptions`:** every field is implemented, three since step 187. It said
  three were refused.
- **`MapExplode`:** said to work, and is refused while the plan is built. Its doc
  now says so.
- **`JoinWhere`:** named a `JoinSuffix` it cannot take. It now gives the cross join
  and filter that set one, which the optimizer plans the same way. Options for
  `JoinWhere` belong to the frame-operations release.
- **`WithThreads`:** said a join stays serial; its build and probe run in parallel.
- **The spill format's header:** said Parquet refuses Int128 and temporal types,
  and that there are no List or Struct columns. It now gives its history and what
  it writes.
- **`Cut`:** said an Enum cannot be built. The doc now gives the `Cast(Enum(...))`
  that makes one of its bins, ordered as they are, checked by hand.
- **`LazyFrame.Count`:** says it is the row count, not Polars' `count()`, and gives
  the form that is.

## 4. Tests and teeth

- **`TestAFrameFillsNullsByAStrategy`:**
  - each of the seven strategies over an Int64, a Float64, a String and a Bool column
    with nulls, against Polars 1.44.1's answers, but where §1 says it differs;
  - a subset;
  - `Expr.FillNullWith(FillForward)`.
- **`TestDtDateAndTimeReadTheLocalWallClock`:**
  - Polars' four answers on a zoned instant: `dt.date`, `dt.time`, and the casts;
  - a naive Datetime;
  - a Date's date;
  - a Date's time of day refused.
- **`names_test.go`** counts 112 call functions.

| tooth | result |
| --- | --- |
| a frame's strategy taken for its value | **bites** |
| an expression's strategy taken for its value | **bites** |
| min and max fill only numbers | **bites** |
| zero fills every column | **bites** |
| dt.date read in UTC | **bites** |
| dt.time in nanoseconds whatever the unit | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
