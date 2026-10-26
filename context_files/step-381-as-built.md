# Step 381 — as built

**Integer keys of two words.** G1 of `v0.7-scope.md`: a group-by's key of several
integer columns, and n_unique's (group, value) of an integer column, held as two
words where they were encoded to bytes.

## 1. How

The group-by keeps a key of one integer column as the integer (step 160) and one of
a string column as its bytes (step 205); a key of several columns was encoded,
`GroupKeyEncoder` writing each row's validity tags and values through a closure a
column, into a `KeyTable` that hashed the bytes and compared them through its arena.
In h2o gb6, by two Int32 columns, that was a third of the CPU: the table 21%, the
encoding 11%. n_unique keyed its table by the group's four bytes and the value
encoded, and in PDS-H q16, a count of distinct suppliers by brand, type and size,
that table and its doublings were a fifth of the CPU.

- **`PairKeyTable`** (`kernel/pairkeys.go`): `IntKeyTable` for a key of two words:
  hashed by two `Mix64`s, compared with `==`, held by id with its hash, so a
  doubling places each key without hashing it again.
- **`KeyPacker`:** a key of integer columns, each in a field of its type's width, a
  signed value by its two's complement's low bits, and a null bit a column, the
  fields placed in order in the first word with room. A null's field is cleared,
  whatever its slot holds, so two rows' words are equal exactly when their keys are.
  It packs a chunk of rows a column at a time. Two Int64 columns and their nulls,
  130 bits, do not fit, nor do four Int32s, and stay encoded.
- **The group-by's fourth form** (`physical/groupkeys.go`, `consumePairs`): a key of
  two or more integer columns that fit. A frozen sink's new key is routed by its
  encoded bytes, as on every path, so it is encoded only then. The workers' merge and
  the partitioned fold take the keys and hashes by id, as for the other forms.
  `unique` keeps its three forms.
- **n_unique of an integer column** (`nuniqueAcc`): (value, group) in a
  `PairKeyTable`, the value widened as `IntKeys` widens it. The first rows decide the
  table by their column's type, and an accumulator that holds nothing may still take
  the other, so a merge of one that saw no rows into one that did, or the reverse,
  agrees. A spilled run writes an integer's key as its eight bytes, big-endian, so the
  runs order by value as the table's entries are sorted.

## 2. Measured

**Against step 379's runner,** alternating, rounds of five, at a load of 3 to 8 from
other work:

| | step 379 | here | |
| --- | --- | --- | --- |
| h2o gb6, median and sd of v3 by id4, id5, ten million rows | 615–767 ms | 523–713 | 0.85 to 0.93 |
| PDS-H q16 at SF=1 | 55.5–63.0 | 49.5–59.5 | 0.89 and 0.94 |

gb6 read 0.86, 0.86, 0.85 and 0.93 in four runs, the last at a load of 5.6. h2o's
other group-bys take no new path: gb9 read 1.18 and then 1.04, gb4 1.05, 1.08 and
then 0.97 over six rounds, gb1 0.99, gb5 1.07 and 1.01, gb2 0.97, gb10 0.93.

**PDS-H at SF=0.1,** all 22, three rounds of five at a load of 6.4 to 7.4: 1.02 in
all (617.0 ms to 631.3), q13 1.46, q3 1.13 and q17 1.11, none of which takes a new
path; alone, four rounds, they read 0.90, 0.95 and 1.02, and at SF=1 0.97, 1.01 and
1.00.

**Against Polars 1.44.1,** on four threads, alternating, two rounds of five at a
load of 7.7 to 8.7: gb6 1.41 of Polars' time, where it was 1.63 at step 379 (load 5
to 6); q16 1.86, where it was 1.91. What is left of gb6 is the median, a quarter of
its CPU, and the sd, a tenth; of q16 n_unique's table, a seventh, and the runtime's
collections, a quarter.

## 3. What was tried and left

- **Slots holding their key's words,** so a probe reads one place where it read the
  slot and then the key by id: gb6 1.03 of the first form's time, no faster.
- **The hash computed again at each doubling,** as `IntKeyTable` does: q16 1.06 of
  step 379's time, the doublings 10% of its CPU, n_unique's keys being nearly all
  distinct; with the hash kept, 5% and q16 0.89.
- **Wider keys:** q3's key of an Int64, a Date and an Int32 is 128 bits and three
  null bits; a key without nulls could be packed in two words, the rows with a null
  kept apart in a table of their own, drawing ids from one count. Not built: no other
  suite query has such a key.

## 4. Tests

- **`TestPairKeyTableAgainstAMap`** (`internal/kernel`): 20,000 keys that repeat,
  differ in one word alone, swap their words and reach both words' extremes, inserted
  in chunks of 1 to 64 through growth, from a fresh table and a reserved one; ids,
  keys and hashes as a map's; lookups of present keys and absent ones.
- **`TestKeyPackerKeepsKeysApart`:** every pair of 600 rows of six key shapes, every
  width signed and not at their extremes, nulls whose slots hold values, a column of
  no payload, sliced columns, packed from an offset: equal words exactly when equal
  keys. Two Int64s, four Int32s, a Uint64 and two Int32s, a float, a string and a
  Boolean are refused.
- **`TestAnNUniqueOfIntegersCountsAsAMap`:** Int8, Int64, Uint32 and Date values with
  nulls, a column of no payload first; spilled at random points into two or more
  runs; merged from three workers through no remap, a dense one and a sparse one: each
  group's count is a map's. One of encoded keys refuses a merge of one of integers.
- **`TestPackedGroupKeysAnswerAsEncodedOnes`** (root): five key shapes, h2o gb6's two
  Int32s, Int8 Uint16 Int64, Uint64 Int32, Date Int16 Int8, and two Int64s that do
  not pack, against the same keys cast to String, the encoded path: on one thread row
  for row, on eight as a set, in first-appearance order kept, and spilled under 48 KiB
  on one thread and four, the spill directory empty after.
- **`TestAPartitionedFoldOfPackedKeys`** (`internal/physical`): 200,000 rows of two
  Int32 keys with nulls fold partitioned to the encoded keys' answer.

**Teeth: 12 of 12 bite:** a null's field keeping its slot's value; a null's bit left
unset; a signed value's bits past its width; no room kept for the null bits; a
doubling placing a key under the next id; n_unique counting a null row's slot;
n_unique's key without its group; a merged key kept under its source group; a spilled
integer's bytes little-endian; a chunk's first new group not taken; a frozen sink's
new key folded into group 0; a merge reading its own keys.

**Gate:** the light gate, one package at a time on four procs: the tests at v3, 28
packages ok; levels, vet ×3 and the bench engine tests clean; the 22 PDS-H answers
match DuckDB.
