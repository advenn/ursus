# Step 229 — as built

**F5 of `v0.6.3-scope.md`, second part: `DataFrame.Pivot` and
`DataFrame.Transpose`.**

## 1. Why

Both make columns of a frame's values, so their schema is known only once the data
is. A lazy plan's schema cannot wait for data, which is why `Pivot` was on the
README's "not done" list. A `DataFrame` has its data, so both are eager only.

## 2. What changed

**`pivot.go`.** `PivotOptions` names On, Index, Values, an Agg, a Separator and
SortColumns. `Pivot` works in three stages.

1. **One row a pair:** `GroupBy(Index..., On).MaintainOrder()` reduces each value
   column over each (Index, On) pair, by Agg or, without one, by First with a
   count beside it. A cell of two values or more without Agg is refused, naming
   the row and asking for an Agg.
2. **Numbering:** `groupsOf` numbers each pair row's Index group and its On value
   by the group key encoder, so a null is a group, in the order each first appears.
   - That is also the order each On value first appears in the frame: the pair
     holding a value's first row comes first among the pairs holding it.
   - SortColumns sorts the On values instead.
3. **Spreading:** each value column is spread by a Take per On value, from a pick
   of where each Index group meets that value, null where they never meet.

**The rules, as Polars' pivot has them:**

- A column is named after its On value, cast to String, "null" for a null. With two
  value columns or more it is `value + Separator + on`, Separator "_".
- An Index or Values left empty is every other column. Both empty is refused, as
  is a column named twice, and a new column whose name is taken.

**`Transpose`:**

- column j of the answer holds row j, every column cast to the type all of them
  meet at by `Promote`;
- **with IncludeHeader** a first column of the names;
- **with ColumnNames** the new columns' names, one a row;
- the columns are laid end to end and each new column is one Take with a stride of
  the height;
- **one difference from Polars:** a String beside a number is refused, where Polars
  casts every column to String.

## 3. Tests and teeth

- **`TestPivotAnswersAsPolars`:**
  - Polars 1.44.1's answers: a null index, first-appearance and sorted columns, two
    value columns, Values left out, Agg's sum, a null On value;
  - a cell of two values refused;
  - four refusals by their message.
- **`TestTransposeAnswersAsPolars`:**
  - with the header, with names, a name too few;
  - a String beside an Int64 refused.

| tooth | result |
| --- | --- |
| pairs grouped without their order | **bites** |
| every pair row put in the first row | **bites** |
| a cell of two values admitted | **bites** |
| a null On value named empty | **bites** |
| SortColumns ignored | **bites** |
| the separator dropped | **bites** |
| a name taken twice admitted | **bites:** once the test asked for pivot's message. The schema refuses two names alike anyway, without naming pivot |
| transposed by the wrong stride | **bites** |
| the header left out | **bites** |
| columns cast to the first's type | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
