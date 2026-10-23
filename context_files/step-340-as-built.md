# Step 340 — as built

**`Mode` in an aggregation is an aggregate of its own, and spills,** the second half of
`polars-parity.md` §2's last row, after step 339's Spearman: §2 is built.

## 1. What changed

In an aggregation a group's `Mode` was the list kernel over the group's values
imploded, `list.mode(implode(x))`: it kept every value of every group, counted each
list in a Go map of strings, and past a memory limit it refused, as implode does. It is
`expr.AggMode` now, with `AggParams.MaintainOrder`, a `List` of its input's type,
refused for a Struct or a List as `list.mode` refuses them. `expr.ModeAggs` rewrites
the list kernel over an implode into it after `LowerReshapes`, in every frame
operation's lowering: a group-by's `Agg`, a temporal group's, a `Select`'s aggregate.
A mode of another reshape, `x.Sort().Mode()`, keeps the kernel, which reads that
reshape's list, and so does one in a window, whose partitions implode as they did.

**`kernel.modeAcc`** numbers each distinct value as it comes: by its bits where its
type is stored as an integer or a float, a float's made one value of every NaN and of
±0 as grouping makes them (`IntKeyTable`); by a text's own bytes, its null numbered
apart (`KeyTable`); or by the group-key encoder's bytes. A row is then its group and
its value's number, and a float's own bits besides, whose first in a group is the
answer's: Polars answers −0 where −0 came first, and a NaN's payload is its first
row's. A number's first value is kept for every other type, whose values of one number
are one value bit for bit.

**At `Finish`** the rows are sorted by group, by counting, reading the rows in turn,
and each group's values counted in an array by number, kept at zero between groups:
its modes are the values of the most rows, in the order they first appear, or sorted
by value, nulls first. A mode depends on the order of its rows, so it is
order-dependent: its group-by runs on one worker, as implode's did, and `Merge` takes
a later portion.

**Past the limit** the held rows are counted so, a group at a time, into an entry for
each distinct value of each group, its rows, its first row and its value, and the
entries go to a `ModeSpill` as a run, sorted by group and the encoder's bytes of the
value (`physical.modeSpill`, whose schema waits for the first values' type); the rows
and tables start afresh, the next rows numbered after them. At `Finish` the rest go
too, and the runs are read back twice (`groupRuns.kept`, then `each`): first each
key's entries are a run, its rows summed and its first row the least, so each group's
modes are found by their first rows, a group's list cut when a key of more rows comes;
then the entries whose first rows those are give the values, which are sorted by first
row, or by value.

**Two drafts first.** An entry for each distinct value of each group, keyed by the
group and the encoder's bytes in one `KeyTable`, as `n_unique` keys its values, was
slower than the implode it replaced, 197 ms where it took 126 by groups: a byte hash
and a `bytes.Equal` through the arena a row. Keyed by group and number in an
`IntKeyTable`, it was faster, 71 and 89 ms, but text by 1000 groups, 500 words, was
500,000 keys, a cache miss a row, and no faster than before (163 ms, 170). Counting a
group's rows in an array does not miss.

## 2. Tests

**`TestModeInAnAggregation`,** Polars 1.44.1's answers: by group, in the order values
first appear and sorted, over floats (`-0.0` first, NaN, a null of the most rows),
text with nulls, Booleans, dates and integers, a mode's first; over the frame; in a
dynamic group; the plan's `mode(maintain_order=true)` and no implode; a mode of a sort
by the list kernel; a Struct refused; and the order values first appear over forty
batches on one thread and four.

**`TestAHotGroupsModeSpillsItsCounts`:** over five hot keys and none, 997 integers and
1999 strings with ties of the most rows, all-distinct integers, every one a mode,
integers and text with nulls, a float of NaN, ±Inf and ±0; keyed, maintaining order or
not, and keyless; one thread and four under 8 KiB, and a dynamic group under 32 KiB:
each spills, equals the answer without a limit, and leaves the spill directory empty.
A few values a group no longer need to spill: an entry a value is small.
`TestV066UnderAMemoryLimit`'s mode moved from refused to answered.

**Kernel:** `TestAModesSpilledEntriesGiveItsModes` (floats with two NaN payloads and
±0 either first, nulls, ties, spilled at random, held, spilled and the list kernel
over each group's values all to the bit; a spilled mode refuses a merge),
`TestASpilledModeKeepsTheOrderValuesFirstAppear` (ties spilled between batches, in
first-seen order and sorted), `TestAMergedModeSpillsAsOnePass`; the merge cover over
every family, and the one-row classification (`classImplode`).

**Teeth (13 of 13 bite):**

- modes left imploded;
- mode not order-dependent;
- a mode's rows not spillable;
- the most rows the last value's;
- a value's first row its place in the group;
- a float's null mode a value;
- a spilled key's first row its last entry's;
- a group's modes not cut by more rows;
- a spill's first rows not past the spilled ones;
- a text after a null numbered as the null;
- a merged value keeping its own number;
- spilled modes in key order;
- modes without order unsorted.

"Spilled modes in key order" was silent until the ties test: the random one rarely
ties the most rows. Two first drafts only failed to build and were redrawn.

## 3. Performance

A million rows, the median of seven, against Polars 1.44.1:

| | before | now | Polars |
| --- | --- | --- | --- |
| an Int64 of 100 values by 1000 groups, in first-seen order | 174 ms | 38 ms | 19 ms |
| the same, sorted | 126 | 35 | 17 |
| text of 500 words by 1000 groups, in first-seen order | 170 | 53 | 20 |
| an Int64 of 100 values over the frame, `Mode(true).First()` | 91 | 18 | 5 |

What is left is a thread's: a mode's group-by is serial, where Polars computes a group
to a thread (§8).

**Gate:** test-all 135 ok, race 27 ok (the root package 671 s here), levels, vet ×3
and the bench engine tests clean; the 22 PDS-H answers match DuckDB.
