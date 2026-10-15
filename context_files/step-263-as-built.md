# Step 263 — as built

**G1 of `v0.6.6-scope.md`: integer and linear ranges, `Repeat`, `Ones` and
`Zeros`,** Polars' generators. Each has its per-row form too: `IntRanges`,
`LinearSpaces` and `Expr.RepeatBy`, the last M4's.

## 1. What changed

**Three call functions in the horizontal block,** each a List a row
(`internal/kernel/generate.go`):

- **`int_ranges(start, end, step)`:** the integers from start towards end, end
  excluded.
  - The count is taken in unsigned arithmetic, so any span of int64 counts.
  - A step pointing away from end gives an empty list.
  - A step of zero is refused, naming its row. Polars panics there.
- **`linear_spaces(start, end, n, closed)`:** Polars' formula, so every value
  agrees to the bit. Each value but the last is `first + i·step`, the last is the far
  end exactly, and `first`, `step` and the far end follow `closed`.
  - The element type is Float32 for two Float32 ends and Float64 for other numbers.
  - Two ends of one temporal type give that type, spaced over their ticks as float64
    and truncated toward zero.
  - Two Dates give Datetime(us), as Polars gives them.
  - **The product is rounded before the sum,** by a conversion, `F(F(i)*step) +
    first`. The gate's first run failed three of the cases in their last bit: built
    with `GOAMD64=v3`, as test-all builds, the compiler fused the two into one
    multiply-add, rounded once, which the Go spec allows and an explicit conversion
    prevents. Polars does not fuse. A plain `go test` builds for v1, where there is
    nothing to fuse with, and passed.
- **`repeat_by(value, n)`:** the value n times, a null n a null list. A negative n is
  refused, as Polars refuses it.

A batch of generated lists holds at most 2³¹ - 1 elements, the most their int32
offsets hold, and past it is refused naming `WithRowIndex`, which streams.

**The scalar forms** are the per-row form's list for one row of the arguments,
exploded (`generate.go`):

- **`IntRange(start, end, step)`, `LinearSpace(start, end, n, closed...)`,
  `Repeat(value, n)`, `Ones(n)` and `Zeros(n)`;**
- **arguments** are constants or aggregates, so
  `WithColumns(IntRange(0, Len(), 1))` is a row index;
- **`ReshapeExplode` gains a flag,** `Args [1]`, for a generator's explode. An empty
  list is no row, where `Expr.Explode` makes it a null row as Polars' explode does.
  `IsGenerator` reports it, and its text is the generator's, `int_range(lit(0),
  lit(5), lit(1))`, apart from an explode of the same list;
- **in `Select` and `WithColumns`** the zipper takes it, with two additions:
  - one List value exploded stands on `oneRow`, a frame of one row that reads none of
    the input;
  - a generator's row is dropped while its list is empty;
- **in an aggregation** the list is the group's own, since it is one value a group:
  `lowerReshape` returns it unexploded;
- **an aggregate over a generator** (`IntRange(0, 5, 1).Sum()`) lowers to a list
  function of a literal. `Select` now asks whether its expressions were one value
  before lowering, so that is one row, as Polars has it.

**Names:**

- the ranges are named after their start, `literal` for a constant, as Polars names
  them;
- `Repeat`, `Ones` and `Zeros` are named `repeat`, `ones` and `zeros`.

**A length-changing expression in `.Over`** is refused with its own message.
Polars maps a partition's list back onto its rows; here that is not there yet. The
hint names `CumCount(false).Over(...)` for a row's number in its partition. It had
failed with a message about a temporary column.

**Differences from Polars,** in `feature-gaps.md` §9:

- **a float argument to `IntRange`** is refused, where Polars casts it;
- **a null argument to a scalar generator** gives no rows, where Polars refuses it;
- **a literal integer in `Repeat`** is Int64, where Polars makes it Int32.

## 2. Tests and teeth

**`TestGenerators`:** 28 selects at batch sizes 1, 2 and 8192, every answer Polars
1.44.1's:

- ranges by one, by two, counting down and empty;
- a row index in `WithColumns`;
- a range from aggregates, a range beside a column, and a range summed;
- linear spaces in all four `closed`, among them one where the steps fall short of
  the far end;
- one sample, none, and Float32;
- dates, instants, ticks truncated toward zero, and durations;
- `Repeat`, `Ones` and `Zeros`.

**`TestGeneratorsAsLists`:** the per-row forms with nulls, and a range in an
aggregation, against Polars.

**`TestGeneratorsRefuse`:**

- a step of zero;
- a negative repeat, and a negative count in a row;
- a float start;
- ends of two kinds;
- a range of another length beside a column;
- a range in `.Over`.

| tooth | result |
| --- | --- |
| a range's count is not rounded up | **bites** |
| the far end is reached by steps | **bites,** once eight samples from 0.2 to 0.9 were in the test; silent before, as the steps of the others land on the end |
| an open space steps by n | **bites** |
| ticks are rounded, not truncated | **bites** |
| a null count repeats nothing | **bites** |
| an empty range is a null row | **bites** |
| an aggregate of a generator is a row a frame row | **bites** |
| the product and the sum are fused | **bites,** under `GOAMD64=v3` |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
