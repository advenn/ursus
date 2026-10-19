# Step 289 — as built

**R1 of `v0.6.7-scope.md`: `Unique` with a keep.** Polars' `unique(subset, keep,
maintain_order=True)`.

## 1. What changed

**`UniqueKeep(keep, subset...)`** on both frames (`unique.go`), with a
`KeepStrategy` of `KeepFirst`, `KeepLast`, `KeepAny` and `KeepNone`:

- **The kept rows are in their own order,** as Polars' `maintain_order=True` keeps
  them. `KeepLast` of a key whose rows are 0 and 2 keeps row 2 where row 2 was, so
  the probe frame's keys (a, b) keep rows 2, 3, 4 and 5 in that order.
- **`KeepFirst` and `KeepAny` are `Unique`,** which streams, a key's first row
  emitted as it is met. Polars' `any` is whichever row its hash table meets first;
  kept in order, that was the first row in every case probed.
- **`KeepLast` and `KeepNone` are a filter by a window over the keys.** A row cannot
  be known to be kept until its key's rows have all been read:
  - a key's last row is the one a reversed `CumCount` numbers 1;
  - a key seen once is the one whose partition's `Len` is 1.

  Windows key rows by `GroupKeyEncoder`, as `Unique` and `GroupBy` do. So a null keys
  with a null, NaN with NaN and −0 with 0, as Polars keys them, and the four keeps
  agree on what a key is.
- **No subset is a whole row:** the input's schema is resolved when the plan is
  built, as the zipper resolves it, and every column is a key.
- **A spill:** the window spills its partitions past a memory limit, as `Over` does.
- **A keep that is none is refused.**

**A gap it found, wider than it:**

- A List or a Struct column cannot be a key anywhere here: `Unique`, `UniqueKeep`,
  `GroupBy`, `Over` and `Join` each refuse one, where Polars compares them. Its
  `unique` of a list subset keeps the last of `[1, 2]`, `[1, 2]`, `[3]` as rows 1 and
  2.
- It is a gap of the types, not of this step: `feature-gaps.md` §3 records it now.

**The scope's claim is corrected:** §8 said `UniqueKeep` spills as the group-by. It is
a window's filter, and spills as a window does.

## 2. Tests and teeth

Every answer is Polars 1.44.1's.

- **`TestUniqueKeep`:**
  - four keeps over (a, b), and first, last and none over a;
  - whole rows, the frame stacked on its first two, first, last and none;
  - null keys, NaN and the zeros, each first, last and none;
  - no rows;
  - the eager form;
  - an unknown column and a keep that is none, refused.
- **`TestUniqueKeepSpills`:**
  - last and none over 20,000 rows of 2,000 keys, some seen once;
  - under 16 KiB they spill and equal the answer without a limit;
  - ten runs at each SIMD width, five scalar-only and two under the race detector.

| tooth | result |
| --- | --- |
| last as first | **bites** |
| none keeps every key | **bites** |
| a whole row its first column | **bites** |
| any as last | **bites** |
| a key of the first column only | **bites** |
| no keep refused | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
