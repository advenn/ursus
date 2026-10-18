# Step 280 — as built

**M3 of `v0.6.6-scope.md`, second half: `unique_counts` and `value_counts` on an
expression.** This finishes M3.

## 1. What changed

**Two reshapes,** on `mode`'s pattern (step 277). Each is a column of its own length
in a `Select` and a list a group in an aggregation, as Polars' are.

- **`UniqueCounts()`** (`ReshapeUniqueCounts`): each distinct value's count, in the
  order the values first appear, a null among them, a UInt32.
- **`ValueCounts(sort, normalize, name...)`** (`ReshapeValueCounts`):
  - each distinct value beside its count, a Struct of the value, named as the
    column, and the count, a UInt32;
  - with `sort`, the most frequent come first, ties in the order they first appear,
    as Polars' sort leaves them;
  - with `normalize`, the count is its share of the rows, nulls among them, a
    Float64;
  - the count is named `name`, else `count`, or `proportion` with `normalize`, as
    Polars names it. The name is the `Reshape`'s new `Label`, which its text renders,
    so two names never share one plan node.
  - **A difference:** without `sort`, the values come in the order they first
    appear. Polars' order is its hash table's: `[y, x, z, None]` for Polars' frame.
- **Both are counted** by the keys a group-by takes, so NaN is one value and −0.0 is
  0.0. A Struct or a List is refused, as for `mode`.

**In a `Select`,** the zipper's frame form:

- a row index, then a group-by of the value counting its rows and keeping its first
  row's index;
- sorted by that index, or for a sorted `value_counts` by the count, most first, then
  the index;
- the share is the count over the counts' sum;
- the Struct is built by `Struct` of the value and the count, named.

**In an aggregation,** `list.unique_counts` and `list.value_counts`
(`internal/kernel/listmode.go`):

- **Shared walk:** both use `counted`, which walks each list's window through
  `listGather`, so a sliced list counts only its own elements. It keeps each
  distinct element's first place and its count, in step, sorted stably by count when
  asked.
- **The value's name:** a list's type has no other way to carry it, so it is the
  call's argument beside the count's.
- **A share** is over the list's length.
- **CallFns:** both join the list block at 700.
- **The sweeps:**
  - the names test counts 203;
  - the sliced-list sweep's floor is 35 of the 36 list functions, all but `item`;
  - the contract drives `list.mode`, and `list.value_counts` sorted and in shares.

**The contract found one thing.** The typing first promised the count field
non-nullable, where a Struct ursus builds has nullable fields. The field is typed
nullable now.

## 2. Tests and teeth

Every answer is Polars 1.44.1's, but its hash order, named above.

- **`TestUniqueAndValueCounts`:**
  - unique_counts of integers, of text, of NaN and the zeros, by group;
  - value_counts unsorted and sorted, ties as they first appear, shares named;
  - by group sorted;
  - a group whose sorted order is not its first-seen one, sorted and in shares;
  - a share's name and type, a UInt32;
  - beside a column of another length, refused as Polars refuses it.

| tooth | result |
| --- | --- |
| unique_counts by value | **bites** |
| sorted, least first | **bites** |
| a share over two | **bites** |
| a list's counts unsorted | **bites** |
| a list's share over one | **bites** |
| a list's repeat counted twice | **bites** |
| the count unnamed | **bites** |

The two list teeth on sorting and shares were written after their tests: the first
group cases sorted alike either way and held no shares, so a group of `b, a, a` was
added before the teeth ran.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
