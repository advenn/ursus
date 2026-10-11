# Step 205 — as built

**G1 of `v0.6.2-scope.md`: a group-by on one string key keeps its values' own
bytes.**

## 1. The profile

Step 204's runner, h2o at 10 million rows over Parquet, five iterations, classified
by stack (the classes overlap):

| query | key | CPU | group-by | keys | Parquet |
| --- | --- | --: | --: | --: | --: |
| gb1 | `id1`, 100 strings | 4.25 s | 69% | 41% | 21% |
| gb2 | `id1, id2` | 6.45 s | 69% | 51% | 25% |
| gb3 | `id3`, 100,000 strings | 23.48 s | 76% | 53% | 22% |
| PDS-H q16 | three keys, after a join | 0.77 s | 31% | 23% | 19% |

**gb1's key work is per row:**

- `GroupKeyEncoder.AppendKey` (14%) copies each string behind a validity byte and a
  length;
- `probeHash` (7%) reads the copy again;
- then `getOrInsert` and `keyAt` probe and compare.

**gb3's is memory.** `memeqbody` is 28% and `keyAt` 12%: one comparison a row, but
each of the eight workers holds all 100,000 groups. After the slot, a match reads
`refs[id]` and then the key's bytes, two dependent cache misses.

## 2. What was tried and dropped

**Reading each likely match's key ahead,** a stage at a time across the 64 keys of
a chunk, as `touch` already reads their slots:

- gb3's time moved rather than fell: `memeqbody` went from 6.5 to 3.0 s and `keyAt`
  from 2.8 to 1.9 s, but `touch` grew to 4.0 s;
- gb1 cost 9% more CPU, its table being in cache;
- limited to tables of 64k slots or more, gb3 measured 4,790 → 4,818 ms, no gain.

The bound is memory across eight workers, not the order of the reads. The patch is
not kept. A table a worker can keep in cache needs the rows partitioned by key
before they are aggregated, a larger change.

**A dictionary side-car** was weighed too: a String column read from a Parquet
dictionary carrying its indices, so a group-by finds each distinct index's group
once. It pays only where a batch of 8,192 rows meets few distinct values:

- gb1's 100, but not gb2's 10,000 pairs or gb3's 70,000 values a row group;
- a filter or take would have to carry it, and CSV has no dictionary.

It is left for when a String column refers to its dictionary throughout.

## 3. What changed

- **A third form of `groupKeys`, `raw`:** for a key of one String or Binary column,
  a `KeyTable` of each value's own bytes, read in place from the column
  (`StringAccessor.Bytes`, new), never copied before the hash.
- **`consumeStrings`** mirrors step 160's `consumeInts`:
  - a chunk of 64 values at a time, through `GetOrInsertMany`, or
    `GetOrInsertNullable` when the batch has nulls;
  - a frozen sink looks resident keys up, and routes a new one by `partitionOf` of
    its encoded key, as the other paths do. Spill files are keyed by the encoding,
    which must not change.
- **`KeyTable`'s null placeholder:**
  - a value's own bytes can be any bytes, so a null cannot be a key;
  - `GetOrInsertNull` gives it the next id, an empty key, the fixed hash
    `nullKeyHash` and no slot, as `IntKeyTable`'s null has;
  - `NullID` names it, and `grow` skips it;
  - the table stores the id plus one, so a zero table has none.
- **`insertFrom`,** for the workers' merge and the partitioned fold, moves a raw
  table's null as the null, by `GetOrInsertNullable`, never as its empty key.

## 4. Tests and teeth

- **`TestAStringKeyGroupsAsTheEncodedKeysDo`** (root): a String and a Binary key,
  with nulls and empty strings among the values. A second key, the same in every
  row, makes the same groups through the encoded path, and the two must agree:
  - on one thread;
  - on eight threads over 300 keys, which the workers' merge folds into one table;
  - on eight threads over 40,000, for the partitioned fold;
  - in first appearance under `MaintainOrder`;
  - frozen and spilled under 256 KiB, unordered and in first appearance.
- **`TestAKeyTablesNullIsNoValue`** (kernel):
  - the null's id falls in order between new keys, and it is not the empty string;
  - its id, hash and key stay put through 5,000 inserts and many growths;
  - moved by hash, it is still the null.
- **`TestAnIntegerGroupKeyIsNotEncoded`** now also checks which keys are raw: one
  String or Binary field, not two.

| tooth | result |
| --- | --- |
| no raw form, ever | **bites:** the form test |
| a null keyed as the empty string | **bites** |
| validity ignored | **bites** |
| a frozen sink's null looked up as the empty string | **bites:** the spilled cases |
| a new group's first row not kept | **bites:** about forty aggregate tests |
| a new group's first appearance not kept | **bites:** the ordered cases |
| a merged null moved as the empty string | **bites:** the 300-key case, added for it; the integer test's String baseline |
| `NullID` one off | **bites** |
| the null's hash not kept | **bites** |
| a nullable run's keys inserted after its null | **bites** |
| a string's bytes one short | **bites** |
| `grow` gives the null a slot | silent, and equivalent: the slot's tag is 0 and its key empty, and the one key it could equal, the empty string, has a hash whose tag is not 0 |

**The merged null was silent at first.** The test's eight-thread case had 40,000
keys and went to the partitioned fold. That routes the null by its fixed hash, to
partition 0, and the empty string by its own; with eight partitions they land in
different ones, though one partition count in eight would put them together. The
workers' merge always puts the two in one table, and a 300-key case reaches it.

## 5. Measured

Step 204's runner against this step's, alternated three rounds, h2o at 10 million
rows:

| query | CPU | wall |
| --- | --: | --: |
| gb1, Parquet | 1,373 → 941 ms, **−31%** | 260 → 199 ms, **−23%** |
| gb3, Parquet | 5,127 → 4,840 ms, −6% | 771 → 768 ms, ±0% |
| gb1, CSV, one iteration | | 1,001 → 918 ms, −8% |

- **gb3** is bound by memory, as section 1 found, so the encoding it no longer
  makes is a small part of it.
- **gb1 over CSV** spends most of its time parsing.
- **gb2, q16 and PDS-H's group-bys** have several keys, and keep the encoded path.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
