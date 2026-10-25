# Step 362 — as built

**A unique on several threads, in the group-by's tables:** `Unique` keeps each key's
first row, in the rows' order, as before. Its set of the keys it has seen was a Go
map of each encoded key: the map's growth, the hashing and the collector's walks of
it were nine tenths of a unique of a million distinct integers.

## 1. How

**`uniqueKeys`** (`internal/physical/uniquekeys.go`) is the set, in a group-by's
forms (`groupKeysOf`, which `newGroupKeys` now calls):

- an integer key of one column, the integers themselves, `IntKeyTable`;
- a String, Binary or Categorical key of one column, its own bytes, a raw
  `KeyTable`;
- any other key encoded, a `KeyTable` of `GroupKeyEncoder`'s keys.

A null is a key of its own, never a value's, and a float's key is its `OrderKey`,
so NaN equals NaN, as with the map.

**On several threads it is a table a thread,** up to 32, a key in the one its hash's
`PartitionOf` names. A batch goes in three steps (`insert`):

1. **`prepare`:** each row's key is read and hashed, and its table named, a range of
   rows a goroutine. An encoded key is written by an encoder of the range's own into
   a buffer of the range's own. A null's hash is 0, the tables' `HashAt` of a null,
   so every null meets in one table.
2. **`byTable`:** each table's rows are listed, in order. Each range counts its rows
   a table, and lays them out after the rows of the tables before and of the ranges
   before it in theirs, as the radix passes lay out their buckets.
3. **The inserts,** a table a goroutine (`eachPart`), each table's rows in the batch's
   order, a chunk of 64 at a time by `GetOrInsertMany`, `GetOrInsertHashed` or
   `GetOrInsertNullable`.

A key's first row is the one that inserts it, as with one table, so the answer is
one thread's. The goroutines run when the batch has 4,096 rows or more and the set
holds 65,536 keys or more. Below that the caller runs the same steps on the same
tables: a small table is found in cache, and a batch's goroutines cost more than
they save there. On one thread the set is one table and steps 1 and 2 take no
goroutine.

**`distinctOp`:**

- **`Next`** takes the rows `insert` calls new by `takeBatch`, or the batch itself
  when every row is new.
- **The account** is the tables' `NBytes`, where it was an estimate of 48 bytes and
  the key a key.
- **A frozen unique** asks `held` which rows' keys the set has, adding none, and
  routes the others by the radix of their encoded key, as before. Only a routed
  row's key is encoded.
- **A spilled file** is deduped a level deeper by a unique of one table of the same
  form (`single`). A table a thread at each level put about a kibibyte a table under
  the memory limit before any key, and `TestDistinctIsAccounted`'s 4 KiB limit
  refused at the deepest level.

## 2. Tests

- **`TestUniqueOnThreadsMatchesOneThread`:** 150,000 rows, on four and seven threads
  against one, in batches of 8,192 and of 1,000 rows:
  - an integer with nulls, of 3,000 keys and of 95,000;
  - an Int32, a Date, a Boolean;
  - a float of NaN and both zeros, of 500 keys and of 95,000 with nulls;
  - a string of nulls and empties, of 2,000 keys, and one of 95,000 with nulls;
  - a Categorical;
  - three columns, two of which one has 95,000 keys, and every column;
  - the first ten rows.
  - Each again past a memory limit it spills under, 16 KiB for a few keys and
    256 KiB for many.
  - `Unique(k)`'s rows are each key's first, against a Go map, a null a key.
- The unique, distinct, spill and memory tests of the root and `physical` pass as
  they were.

**Teeth: 16 of 18 bite.**

- **The forms:** an integer null hashed by its slot; a null inserted by its slot or
  as the empty string; a held null looked up by its slot or as the empty string.
- **The tables:** every row new; a range's last row not hashed; an encoded key's
  range unwritten; the rows laid out range-major; a range's count kept from the last
  batch; the last table's end stale; a table's last row dropped.
- **The spill:** a deeper level given the held keys; a held key routed; never
  frozen; the tables never charged.

The two silent ones are equivalent: a table given the next table's rows renumbers
the tables, and the rows not counted before a freeze leave the routed rows' ordinals
in their order.

## 3. Performance

A frame of a million rows in memory, median of seven, with the machine under the
user's other work (a load of 7 to 8, so a run varies by a third), Polars 1.44.1
measured alongside:

| | before | 1 thread | 4 threads | 8 threads | Polars, its order kept | Polars |
| --- | --- | --- | --- | --- | --- | --- |
| distinct Int64s | 430–640 ms | 80–105 | 62–76 | 54–80 | 119–121 | 49–61 |
| distinct strings of 8 bytes | 410–920 | 93–188 | 79–110 | 60–147 | 149–182 | — |
| Int64s of 1,000 keys | 40–58 | 16–25 | 18–26 | 19–23 | 8.0–8.2 | — |
| a key of 1,000 and a Float64 | 35–43 | 16–18 | 16–20 | 19–21 | 8.5–27 | — |

- **Distinct keys** are five to eight times as fast, and faster than Polars' unique
  that keeps the rows' order, which ursus's does. A profile of four threads is the
  integer table's probes, three fifths, and its growth, a fifth: the misses of a
  table past the cache.
- **A few keys** are twice as fast and half Polars' speed. Their tables are in cache,
  so one goroutine inserts them on any thread count; eight goroutines a batch took 20
  to 30 ms where one took 16. What is left is reading and hashing each row, a table
  a row, and the frame's batches.

**Gate:** the light gate, one package at a time on four procs: the tests at v3, 28
packages ok; levels, vet ×3 and the bench engine tests clean; the 22 PDS-H answers
match DuckDB. The other widths, the scalar build and the race detector are CI's.
