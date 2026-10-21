# Step 301 — as built

**C4: a frame's nulls and NaN** (`v0.6.7-scope.md` §2):

- a fill strategy's limit, Polars' `fill_null(strategy=, limit=)`;
- a frame's `DropNans`;
- a frame's `Interpolate`.

On the way, `Interpolate` of instants, durations and times, as Polars computes them.

## 1. A strategy's limit

**`FillForward.Limit(n)` and `FillBackward.Limit(n)`** cap how many consecutive nulls
one value fills. Each works:

- for an expression, `Col("x").FillNull(FillForward.Limit(1))`, `FillNullWith` too;
- for every column of a frame, `lf.FillNull(FillForward.Limit(1))`, the frame's
  `FillNull` already taking a strategy.

**A strategy carries its limit, so the call sites are unchanged.** Polars passes
`limit` beside `strategy`, but the frame's trailing parameters are the subset's names,
and a value that is not an `Operand` cannot reach `FillNull[T Operand]`.

- `FillStrategy` became a `uint64`, from a `uint8`. A `uint64` is still a literal type,
  so a strategy is still an `Operand`.
- Its low byte is the rule. The bits above hold whether `Limit` was called, why a
  limit cannot be taken, and the limit.
- Every comparison goes through `kind()`: `Expr.FillNull`'s switch, the frame's
  column choice, the name table. `String()` gives `forward(limit=3)`.
- The CHANGELOG lists it under breaks. A conversion to `uint8` no longer compiles,
  and `FillForward.Limit(1) != FillForward`.

**What is refused,** when the fill is used, since a strategy is a value and carries
no error:

- **a limit of another strategy,** "a limit is for FillForward and FillBackward, not
  mean", as Polars refuses `limit` with `mean`;
- **a limit below 1, and a second `Limit`.**
  - Polars' `limit=0` fills nothing. But ursus's `ForwardFill(0)` and
    `BackwardFill(0)` mean no limit, and have since before 0.6.
  - So a 0 here is refused rather than meaning either.
  - Polars' `forward_fill(limit=0)` fills nothing too, probed. The older difference
    is in §9 now, beside this one.

## 2. `DropNans`

`DropNans(subset...)` drops the rows holding a NaN in a named column, or in any float
column when none is named, as Polars' `drop_nans`.

- A null is not a NaN, so its row stays. The predicate is `IsNull().Or(IsNan().Not())`,
  since an `IsNan` of a null is null and a `Filter` would drop it.
- A named integer column, an Int128 too, holds no NaN, and drops nothing, as Polars'.
- A named Decimal, Boolean, date or text column is refused, "which has no NaN", as
  Polars refuses `is_nan` of each.
- A frame with no float column is returned unchanged.
- It is a `Filter`, so it streams.

## 3. `Interpolate`

`Interpolate()` interpolates every column a line can fill, as Polars' frame
`interpolate`:

- an integer answers a Float64;
- a float keeps its width;
- an Int128 is cast to Float64 first, Polars' answer, since `Expr.Interpolate` reads
  64 bits;
- a date, a datetime, a duration and a time keep their type.

Text, Booleans, lists and structs are left as they are, and a frame with nothing to
interpolate is returned unchanged.

**A Decimal answers a Float64,** as `Expr.Interpolate`'s does, where Polars' frame
`interpolate` keeps the Decimal. A cast back could fail, a Decimal cast being exact or
refused, so the difference is recorded in §9 rather than papered over.

## 4. `Interpolate` of instants: Polars' integers

`Expr.Interpolate` refused a date, a datetime, a duration and a time; Polars' answers
keep the type.

**The first attempt went through floats,** as the numeric line does, and truncated
back. A probe of Polars found that wrong:

- the middle of day −2 and day −1 is day −2 in Polars, where a float's −1.5 truncated
  gives −1;
- Polars' `linear_itp` computes `low + step·(high − low)/span` on the physical integers,
  the quotient truncated toward zero.

**ursus now does the same in both of its paths:**

- the in-memory window, `winInterpolateTicks`;
- the spilled walk, `InterpolateFrom`'s temporal branch;
- the shared `interpTick` takes the product in 128 bits, `bits.Mul64` and `Div64`,
  where Polars' wraps past its width;
- the float branch first added to `floatsAs` was taken out again.

**The typing** returns the type for `WinInterpolate` of a temporal column. The ewms
and the rolling functions are not touched.

`TestInterpolateTemporal` holds a time, a datetime 10 µs over three steps, a date over
three, a date before 1970, a duration, a Boolean and an Int128 to Polars' answers. It
also holds 20,000 datetimes and dates, interpolated past a 32 KiB limit, to the run
without one; it asserts the run spilled.

## 5. The site

`docs/guide/missing-data.md` has the limit, `DropNans` and the frame's
`Interpolate`, each new in 0.6.7, on `dev`, with three examples, the last across a date
gap. The Polars page maps all three.

## 6. Tests and teeth

**The tests:**

- `TestFrameNullsAndNaN`: limits forward and backward, of a frame, a column and an
  expression; `DropNans` of every float, one float, an integer; `Interpolate`; the
  refusals; the strategies' names; the unchanged plans; the eager forms;
- `TestInterpolateTemporal`.

**Teeth, all biting:**

- the limit ignored;
- a frame's columns chosen by the whole strategy, not its kind;
- a mean's limit let through;
- a limit of 0 taken as none;
- `DropNans` dropping nulls;
- an integer's `DropNans` refused;
- a float's line truncated, which only the date before 1970 catches;
- the spilled walk giving its own value;
- an Int128 not cast.

The spilled walk's tooth first failed to compile, and was rewritten so that it
compiles.

**Gate:** test-all 125 ok, race 25 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
