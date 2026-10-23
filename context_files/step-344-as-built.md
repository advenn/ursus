# Step 344 — as built

**A List, an Array or a Struct as a key,** `polars-parity.md` §3's third row: a group-by,
a unique, a join, a window, a partition, a mode, a run and a row's hash key one, as
Polars keys them. Each was refused.

## 1. How

**The group-key encoder writes them** (`kernel.listKey`, `structKey`): a list's
length, four bytes, then each element tagged as a key's field is, `0x00` a null and
`0x01` before the value's own key; a struct's fields tagged and written in turn, as a
key of several columns is. So two lists are equal bytes exactly where their elements
are equal keys: NaN NaN's, `-0.0` `0.0`'s, a null element a null's; an empty list is
its length alone, apart from a null list, whose tag is the field's; and no two
values' bytes run together. Every operation that encodes its keys takes them so:
the group-by, its spill partitions and replay, `Unique`, a join's encoded keys,
`Over`'s partitions, `PartitionBy`, `HashRows`, `RleID`, `Rle`, `Mode`.

**`dtype.IsHashable`** answers for them now, where it was `IsOrdered`: a List or an
Array of a hashable element, a Struct of hashable fields. The two questions were kept
apart for this: "a Struct could be hashable field-by-field long before anyone defines a
total order over it". A list has no order still, so no sort key.

**A mode of them without `maintainOrder`** comes in the order its values first appear,
having no order to sort by (`modeAcc.list`, `listMode`); the merge sweep's pair
families found it.

| Polars 1.44.1 | ursus |
| --- | --- |
| `group_by` a List: `[1, 2]`, `[]`, `null`, `[1, null]`, `[2, 1]`, `[null, 1]` | six groups, as Polars |
| `[nan]` and `[nan]`, `[0.0]` and `[-0.0]` | one group each, its first the key |
| `group_by` a Struct: `{a: 1, b: "x"}`, `{a: null, b: "x"}`, a null struct | three groups |
| `unique`, `over`, `join` on a List | as Polars; a null key joins none |
| `n_unique` of a List column | 5 where Polars' is 6: the null passed over, as ursus's n_unique of any type (§9) |
| `partition_by` a List | a part a list |
| `rle_id` of a Struct, a Struct's `mode` | as Polars |

## 2. Tests

**`TestNestedKeys`,** Polars 1.44.1's answers: a group-by of a List of eight rows,
`[]` apart from null, a null element a value, `[1, null]` apart from `[null, 1]`;
unique, over and join on it, a null key joining none; its n_unique; a List of floats,
NaN with NaN and `-0.0` with `0.0`; a Struct's groups and a join on one; an Array's
groups; a group-by of 1500 List keys spilling under 32 KiB, equal to the one without a
limit; a sort by a List still refused.

**Flipped from refusals to Polars' answers:** `TestArrayType`'s Array key,
`TestHashRows`' List (two rows alike hash alike), `TestPartitionBy`'s List key,
`TestModeInAnAggregation`'s Struct mode (both orders), `TestRle`'s Struct `rle_id`,
and `TestGroupByAListRefusesReadably`, now `TestGroupByAList`, the groups of a split.

**Also:** the hot-group spill tests of steps 339 and 340 run one thread under the race
detector, as other spill tests do: the root package's race run took 1037 s at step
343's gate, the machine shared, against CI's 723.

**The heap-growth tests measure the live heap** (`peakHeapGrowth`): they sampled the
heap's objects, which count garbage not yet swept, and
`TestTemporalMediansSpillTheirValues` failed at this gate, as at step 339's, a
megabyte past its 10 MB bound: the machine's eight cores were loaded to 9 or 10 by
other work, and the collector fell behind. They sample `/gc/heap/live:bytes` now, the
heap each collection found held. On a quiet machine the two read alike, the live heap
a megabyte the less: the dynamic median grows 19.2 MB by it and 20.1 by the objects
without a limit, 3.6 and 4.5 under 2 MB; the rolling median 7.8 and 8.7 without, 3.8
and 4.9 under, so its bound told the two apart by neither.

**Teeth (6 of 6 bite):** a nested type no key; a struct no key; a list's null element
untagged; a list's elements not written; a struct's fields not written; a mode of
structs sorted. Two first drafts failed to build, a variable unused, and were redrawn;
the struct's twice.

## 3. Performance

A million rows, a List of two integers, 7000 keys, in memory, the median of seven:

| | ursus | Polars |
| --- | --- | --- |
| a group-by and a sum | 84 ms | 57 ms |
| `Unique` | 84 | 51 |

Each row's list is encoded, its elements tagged, and hashed as bytes; Polars hashes
its lists' values in place.

**Gate:** race 27 ok (the root package 768 s here), levels, vet ×3 and the bench engine
tests clean; the 22 PDS-H answers match DuckDB. test-all failed its first run on the
heap-growth test above, and passed once that measured the live heap: 135 ok.
