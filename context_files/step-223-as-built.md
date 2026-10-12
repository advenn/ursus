# Step 223 — as built

**F1 of `v0.6.3-scope.md`, second part: the list set operations and `Gather`.**

## 1. Why

Polars' list namespace compares two lists as sets, and picks a list's elements at
a list of indices. Each takes a second List, a column rather than a parameter. The
calls' families take a receiver and literal parameters, except the horizontal
family (concat_str, struct, cut), which is typed and evaluated from every operand.
So these join that family.

## 2. What changed

**`internal/expr/call.go`:**

- six constants after `FnStructWithFields`: `list.set_union`,
  `list.set_intersection`, `list.set_difference`,
  `list.set_symmetric_difference`, `list.gather`, and `list.gather_or_null`;
- **a set operation is typed by `listSetOut`:**
  - both operands must be Lists;
  - their elements meet by `PromoteExact`, as a join's keys and a Concat's columns
    do. An Int64 against a Float64 is refused, where Polars compares them as
    rounded floats;
  - the meeting type must be hashable;
- **gather** takes a List and a List of integers, and answers the first's type.

**`internal/kernel/listset.go`:**

- **`listSet`:**
  - casts each list's own elements (its window of its child) to the answer's
    element type, and lays them in one column, so one group key encoder compares
    across both and one Take builds the answer;
  - a row takes Polars' order: the first list's distinct elements as they first
    appear, then the second's;
  - a null list on either side is null.
- **`listGatherAt`:**
  - a negative index counts from the end, a null index gives a null element;
  - an index out of range is a value error naming the index, the list's size and
    the row. `gather_or_null` gives null instead.
- **`HorizontalCall`** dispatches both.

**Elsewhere:**

- **`internal/plan/fallible.go`** counts `list.gather` as fallible, so pushdown
  does not run it on rows a join removes.
- **`expr_list.go`:** `SetUnion`, `SetIntersection`, `SetDifference`,
  `SetSymmetricDifference`, and `Gather(indices, nullOnOOB)`.
- **`names_test.go`** counts 96 functions.

## 3. Tests and teeth

- **`TestAListsSetOperationsAnswerAsPolars`:**
  - seven pairs of lists against Polars 1.44.1's answers: repeats, a null element
    on each side, empty lists, and a null list on each side;
  - floats where NaN meets NaN and −0 meets +0, keeping the first list's −0;
  - an Int32 list against a Float64 list meets at Float64;
  - an Int64 list against a Float64 list is refused, naming the function.
- **`TestAListsGatherAnswersAsPolars`:**
  - Polars' answers with nullOnOOB;
  - the refusal without it;
  - in range, the two agree.
- **`TestAListsSetOperationsReadSlicedLists`:** batches of one and three, whose
  lists are slices of their children, answer as one batch.
- **`TestAGatherRefusalIsNotPushedPastAJoin`:** a gather filter after a join that
  removes the row it would refuse.

| tooth | result |
| --- | --- |
| union skips the second list | **bites:** the Polars test |
| intersection keeps what the second lacks | **bites** |
| symmetric difference drops the second's half | **bites** |
| a null second list ignored | **bites** |
| duplicates given out | **bites** |
| a list's window not rebased | **bites:** the sliced test |
| a negative index not from the end | **bites:** the gather test and the sliced test |
| gather_or_null refuses | **bites** |
| a null index read as its slot | **bites** |
| element types meet by Promote | **bites:** the Int64 and Float64 refusal |
| gather not fallible | **bites:** the join test, written for it when the tooth was first silent |
| (the test) the −0 kept is +0 | **bites:** the comparison sees −0's sign |

**A list's window ignored**, taking the whole child from 0, is silent and
equivalent. Absolute offsets into the whole child are as right as relative ones
into the window. The window saves casting and concatenating other batches'
elements.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
