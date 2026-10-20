# Step 293 — as built

**R5 of `v0.6.7-scope.md`: the row shortcuts.** Polars' `shift`, `gather_every`,
`clear`, `first`, `last`, `is_empty` and `equals` of a frame (`framerows.go`).

## 1. What changed

**On `LazyFrame`, each with its eager form on `DataFrame`:**

- **`Shift(n)`:** every column's values n rows later, a negative n earlier, nulls in
  the vacated rows: `All().Shift(n)`, a window function, so it streams.
- **`ShiftFill(n, fill)`:**
  - **The fill meets each column** as `Expr.ShiftFill`'s does: 0 into an Int64 is an
    Int64, and 1.5 into one makes it a Float64, as Polars'.
  - **A text column takes the fill's text,** `"0"`, as Polars' does.
  - **A text fill into a number column is refused, naming the column,** where
    Polars makes the column text. ursus's numbers do not meet text (`Append`'s rule,
    step 282). It was found failing as "when: the then and otherwise branches have no
    common type", which named neither the shift nor the column, so the shortcut
    refuses it first.
  - The frame's schema is resolved as the plan is built, one expression a column, as
    `UniqueKeep`'s whole rows are.
- **`GatherEvery(n, offset)`:** every nth row from offset, a filter by the row's index,
  so it streams. n must be positive, as Polars says.
- **`Clear(n)`:** the schema with n rows of nulls, `Repeat` of a typed null a column,
  or `Head(0)` for none.
- **`First()` and `Last()`:** `Head(1)` and `Tail(1)`, as Polars' `LazyFrame.first()`
  and `last()`, no row for no rows.

**On `DataFrame`:**

- **`IsEmpty()`:** no rows; a frame of no columns has none, as Polars'.
- **`Equals(ctx, other, nullEqual)`:** the same columns by name and in order, and the
  same values row for row, as Polars' `equals`:
  - values compare through Arrow's `ApproxEqual` at a tolerance of 0 with NaNs equal:
    NaN with NaN, −0 with 0, and a List or a Struct element by element;
  - two numbers of different types are cast to their common type first, as Polars
    finds an Int64 equal to an Int32 or a Float64 of the same values; other
    differing types are unequal;
  - with `nullEqual` false, a frame holding any null equals no frame, as Polars'
    `null_equal=False`, as a null there is unequal even to itself;
  - **a difference:** Polars finds a Date equal to its day count; here their types
    differ and they are unequal.

## 2. Tests and teeth

Every answer is Polars 1.44.1's.

- **`TestRowShortcuts`:**
  - shift 2, −1, 0 and 9;
  - shift with 0 and 1.5, and their types;
  - every 2, every 2 from 1, every 3 from 9;
  - clear, clear 2 and its schema;
  - first, last, first of none;
  - every 0, a text fill and clear −1 refused;
  - is_empty; the eager shift.
- **`TestFrameEquals`:**
  - NaN and the zeros; itself;
  - nulls unequal, of itself too;
  - another name, order, row count or value;
  - Int32 and Int64, Int64 and Float64, text and a number;
  - Lists and Structs; no rows.

| tooth | result |
| --- | --- |
| NaN unequal | **bites** |
| nulls always equal | **bites** |
| numbers of two types unequal | **bites** |
| names not compared | **bites** |
| a text column's fill as it is | **bites** |
| gather_every from 0 | **bites** |
| clear of rows none | **bites** |
| last as first | **bites** |
| shift backwards | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
