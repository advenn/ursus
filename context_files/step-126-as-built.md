# Step 126 — as built

**The key table waits on main memory once per chunk of keys, not once per key.**
Every join build and probe, and every group-by, goes through it.

## 1. What the profile said

After step 125, q12's wall time was bound by its join build: `KeyTable.GetOrInsert`
inserting 1.5 million `orders` keys on one thread, about 250 ms of 490, which is
about 170 ns an insert.

A line profile of the key table's own insert benchmark, a million keys into a
reserved table, put two thirds of an insert on one line, the first read of the key's
slot. A million keys need 16 MB of slots, more than the cache holds, so each insert
waited on memory for its slot, one after another.

## 2. What changed

**Tags in the slots.** A slot is now a 64-bit word: the top 32 bits of the key's
hash, its tag, over the id+1.

- A probe passes several occupied slots on its way, near the 3/4 cap about eight for
  a new key. Each was checked against `hashes[id]`, a read at a random place. The tag
  rejects nearly all of them from the ring itself, which is read in order.
- On a tag match the bytes decide. The stored-hash comparison is gone from the probe:
  it would cost a random read on every match to reject about one key in four billion.
- `hashes` stays, for growing without reading keys.
- Slots cost eight bytes, not four.

**Chunks.** `GetOrInsertMany` and `GetMany` take up to 64 keys at once:

1. hash them all;
2. read each one's first slot, in a loop whose reads do not depend on one another, so
   the processor has many in flight;
3. insert or look up each, in order.

Ids, inserted flags and lookups come out exactly as one key at a time would give
them; a key repeated within a chunk finds its own first sighting.
`GroupKeyEncoder.AppendKey` encodes a chunk's keys into one buffer.

**The callers:**

- **the join build** (`admit`): `rowKey` has a place per row, null-keyed ones
  included, filled as each chunk is answered;
- **the group-by:** both the resident path and the frozen one, which routes the keys
  it does not find;
- **the join probe:** `startBatch` looks up the whole batch's keys a chunk at a time.
  `enter` reads the answer, and encodes a row's key again only to route it to a spill
  bucket or to police Validate.

The spilled join's classification and the window's partitioning keep single lookups:
they are not where the time goes.

## 3. Measured

**`BenchmarkKeyTable*`,** a million keys:

| | before | tags only | tags and chunks |
| --- | --: | --: | --: |
| insert into a reserved table | 155–161 ms | 146–148 ms | 63–64 ms |
| a hit in a large table | 398–421 ns | 339–345 ns | |
| a miss in a large table | 182–184 ns | 143 ns | |

**Queries:** the step 125 runner against this one, alternated, three rounds each,
median of five:

| query | before | after | |
| --- | --: | --: | --- |
| PDS-H q4, SF=1 | 445–452 ms | 323–326 ms | −28% |
| PDS-H q12, SF=1 | 487–506 ms | 371–408 ms | about −20% |
| PDS-H q9, SF=1 | 796–829 ms | 637–648 ms | −20% |
| PDS-H q21, SF=1 | 1,766–1,817 ms | 1,420–1,453 ms | −19% |
| h2o gb3 (100,000 groups) | 1,065–1,122 ms | 945–964 ms | −13% |
| h2o gb10 (10 million groups) | 5,710–5,842 ms | 5,361–5,464 ms | −6% |
| h2o j1 (a small build side) | 1,224–1,239 ms | 1,208–1,239 ms | unchanged |

j1 is unchanged, as it should be: its table is small enough to stay in cache. Peak
memory rose about 5%, from the wider slots and the probe's per-row ids.

## 4. Tests and teeth

**`internal/kernel/keytablemany_test.go`:** `TestGetOrInsertManyIsGetOrInsertInOrder`
compares a table filled by chunks against one filled a key at a time. It covers
twenty random universes, chunks of every size, keys repeated within a chunk, the
doublings a table goes through mid-chunk, and lookups of present and absent keys. An
empty table finds nothing.

The callers are covered by the existing join and group-by suites: outer joins'
`rowKey`, `MaintainOrder` and spilling group-bys' first-seen order, batch-size
invariance, and Validate.

| tooth | result |
| --- | --- |
| a slot keeps its id, not id+1, so id 0 reads as empty | **bites** |
| `GetMany` reports a miss as found | **bites** |
| the build's `rowKey` placed by chunk position, not by row | **bites:** full and spilling joins |
| the group-by's new rows by chunk position | **bites:** `MaintainOrder` and the spilling group-bys |
| the probe's answers by chunk position | **bites:** batch-size invariance, every kind |
| the probe skips Validate on a found key | **bites** |

## 5. The race the gate found

The first gate of this step failed its `-race` leg. `GetMany` read each key's first
slot in a loop and stored the sum of the reads in a field of the table, so the
compiler could not drop them. Probe workers share one frozen table, so they wrote
that field at once. That broke the promise `Get` makes and joinTable relies on:
lookups mutate nothing.

`GetMany` now keeps the words it reads in a local array, and each lookup starts from
its key's word (`getFrom`). The reads are used, and nothing is stored. Only
`GetOrInsertMany` stores a sum: it mutates the table already, and its table has a
single owner.

`TestGetManyWritesNothing` runs four goroutines on one table, calling `GetMany`. With
the write put back it fails under `-race`, which the gate's race leg runs.

**Gate:** steps 125 and 126 together, after the fix: test-all 110 ok, race 22 ok, levels, vet ×3 and the bench engine tests clean; PDS-H SF=0.1 22/22 against DuckDB.
