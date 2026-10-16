# Step 268 — as built

**§3 of `v0.6.6-scope.md`: `List().Eval` and `Element()`,** an expression run over
each list's elements, the list its group: Polars' `list.eval`, with `List().Agg` and
`List().Filter` built on it.

## 1. What changed

**`expr.LowerListEval`** (`internal/expr/listeval.go`) rewrites the body by what each
part is to a list, as 0.6.5's E3 rewrote an aggregation's reshapes. It first runs
`LowerReshapes(body, true)`, so a reshape is a list kernel over an implode, then
lowers each part:

- **`Element()`** is a value an element;
- **an implode of a value an element** is a `ListMap`: the body evaluated over the
  lists' elements, a list a list;
- **an aggregate** is one value a list: `lowerAggOver`, the list kernel of the same
  reduction over the `ListMap` of its input;
- **a filter** is a gather of the list by `list.arg_true` of its predicate's
  `ListMap`, step 267's kernel;
- **a value a list beside one list** (`Element().Sort().Mul(2)`) is a `ListMap` over
  that list;
- **another column** is refused, as Polars refuses it.

The result is one of:

- **elementwise:** a `ListMap` over the list. A constant body is repeated along each
  list;
- **a list:** itself;
- **a value a list:** under `Agg`, the value. Under `Eval`, a list of one:
  `concat_list` of the value beside no element of a list of it, `list.head(…, 0)`,
  which is null where the list is. A conditional over a List is not implemented, and
  the list of one must be null for a null list.

**`expr.ListMap`** holds its list, its body and `Outer`, the values a list the body
reads, each made an `OuterRef` in the body:

- its children are the list and `Outer` alone. The body is its own, so no pass over
  the frame's expressions reaches it, and a window function in it is the list's, not
  the frame's;
- `Scope` types the body against the elements, `ElementName`, and the outer values,
  `OuterName(i)`: names no frame's column has.

**`evalListMap`** (`internal/physical/listeval.go`) evaluates a `ListMap`:

- the lists' elements end to end, beside each element's outer values, taken by
  `kernel.Take` from the frame's rows;
- **a window function in the body** (`bodyWindows`) runs a list at a time through
  `kernel.WinCall`. The segments are the lists, sorted within each by rank's child or
  the window's order keys, by `ArgSortColumns`, as the frame's windows are. A window
  partitioned inside a list's body is refused;
- the answers keep the lists' offsets and nulls.

`Element` and `OuterRef` evaluate to their columns of the body's batch, and `Rebuild`
takes all three nodes.

**Public:** `Element()`, `List().Eval(body)`, `List().Agg(body)` and
`List().Filter(pred)`. Each is named after its list, as Polars names it.

**Differences from Polars:**

- the sum of an empty list is null, as every list sum here is (`feature-gaps.md` §9);
- an Int64 sums to Int128, as every sum here does.

**Found: `Rank` ranks a null as the smallest value.** Polars leaves it null.
`[5, null, 4]` ranks `[3, 1, 2]`, in a window as in a list. This is
`feature-gaps.md` §1's first finding since step 249, for a later step: the
frame's rank and its streamed form are both involved.

## 2. Tests and teeth

**`TestListEval`:** 22 bodies over `[[3, 1, 2], None, [], [5, None, 4]]`, each list
read by its length and its first three elements, which tells a null list from an
empty one and from a list of a null. Every answer is Polars 1.44.1's but the empty
sums:

- doubled;
- a sum, a mean, centred on its mean;
- filtered, sorted, sorted then doubled, a head, a filtered sum;
- ranked after a drop of nulls, a running sum, shifted;
- cast, `is_null`, a constant;
- count, len, first;
- `Agg` of values, of a filter and of aggregates;
- `Filter`, twice;
- strings from a split.

**`TestListEvalRefuses`:** another column, `Element` outside a list's body, and a
partitioned window inside one.

| tooth | result |
| --- | --- |
| a window runs across the lists | **bites** |
| a null list's aggregate is a list of a null | **bites** |
| a value a list is the first list's | **bites** |
| a filter keeps every element | **bites** |
| another column is read | **bites** |
| a reshaped list's elements are the original's | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
