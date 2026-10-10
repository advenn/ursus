# Step 160 — as built

**Item 4 of `v0.6-scope.md`, its group-by half: a group-by of one integer column
keys its integers.**

## 1. Evidence first

**A q18 profile** on step 159's build, SF=1, three iterations after a warm-up, 5.2 s
of samples:

- `Consume`'s insert flush, `KeyTable.getOrInsert` and its encoding: 27%;
- `keyChunk.add`, which encodes each key: 9%;
- `KeyTable.grow`: 6%.

q18 groups lineitem's six million rows by `l_orderkey`, 1.5 million groups. The
group-by's keys were about a third of it.

**A q13 profile** found its keys a small part, and something else (§5).

## 2. What changed

**`kernel.IntKeyTable` gains a null key** (`internal/kernel/intkeys.go`):

- `GetOrInsertNull` gives it the next id, as any new key gets, but no slot, since no
  value can find it. Its `keys` entry is a placeholder, and `grow` skips it.
- `GetOrInsertNullable` gives ids in order where a chunk's nulls fall among its
  keys. A null between two new keys is numbered between them, which first-appearance
  order needs.
- `KeyAt`, `HashAt` (the null's is a fixed hash, so every worker's null meets in one
  partition), `GetOrInsert` and `NullID`.

**`physical.groupKeys`** (`internal/physical/groupkeys.go`) is the sink's table in one
of two forms:

- integers for a key of one integer field, or one stored as an integer;
- encoded otherwise.

The form is chosen from the key schema (`newGroupKeys`), so a sink's workers, its
spill sub-sinks and the fold's partitions share it. `insertFrom` moves keys between
two tables of one form: `Merge` uses it, now a chunk at a time, and so does the
partitioned fold's `mergeShare`.

**`Consume`** (`consumeInts`) reads the column's values and inserts them 64 at a time.
A chunk without nulls takes `GetOrInsertMany`; one with nulls takes
`GetOrInsertNullable`.

**A frozen sink** looks keys up and routes a new one by `partitionOf` of its encoded
key. Only routed keys are encoded, and only after a freeze. Routing needs only to be
a pure function of the key, so that all of a key's rows land in one file. A null row
is resident if the null group is, and is never looked up by its slot's value.

**What stays encoded:** keys of several columns, and of floats, strings, booleans or
Int128; a global aggregate.

## 3. Measured

Step 159's runner against this one, alternated, three rounds of five iterations. CPU
time per iteration is from rusage, warm-up included:

| query | CPU per iteration | median wall |
| --- | --- | --- |
| PDS-H q18, SF=1 | 1,782 → 1,593 ms (−11%) | 334 → 307 ms |
| PDS-H q13, SF=1 | 1,596 → 1,523 ms (−5%) | 446 → 444 ms |
| PDS-H q17, SF=1 | 1,489 → 1,491 ms | 277 → 273 ms |
| h2o gb4, 1M rows | 150 → 118 ms (−21%) | 31 → 30 ms |
| h2o gb5, 1M rows | 186 → 150 ms (−19%) | 38 → 34 ms |
| h2o gb8, 1M rows | 200 → 159 ms (−20%) | 33 → 29 ms |

q17's group-by follows its join and sees few rows. These are the session after a
reboot. Step 159's q17, 1,078 ms this morning, read 1,489 ms here, so only
comparisons within a session are read.

## 4. Tests

- **`TestIntegerGroupKeysAnswerAsEncodedOnes`** (root): Int8 to Uint64, Date and
  Datetime keys, with repeats, nulls and every width's extremes. Each is grouped by
  the key itself and by the key cast to String, which takes the encoded path and is
  one-to-one. They must agree:
  - on one thread, row for row in first-appearance order, with `First` and `Last`;
  - on eight, through the workers' merge, as a set;
  - under `MaintainOrder`, row for row.
- **`TestASpilledIntegerGroupKeyKeepsItsNull`** (root): a null resident from the first
  row, and its slot holding 0, also a resident key, spilled at 32 KiB against
  unbounded.
- **`TestAPartitionedFoldOfIntegerKeys`** (physical): 200,000 rows, about 110k groups,
  with nulls, folded partitioned on four threads, against the String cast's serial
  group-by.
- **`TestIntKeyTableNumbersANullInOrder`** (kernel): 400 chunks with key 0 and nulls
  interleaved, against a map, through growth. The null comes first and the table
  grows before any 0 arrives, so a null misplaced in the slots would be met as 0.
- **`TestAnIntegerGroupKeyIsNotEncoded`** (physical): which key schemas take which form.
- **The existing spill tests** group by an Int64 key, so they now run the integer form
  frozen, spilled and replayed.

## 5. Found on the way

- **`LIKE` with two wildcards runs as a regular expression.** q13's
  `o_comment NOT LIKE '%special%requests%'` goes through Go's backtracking regexp:
  `tryBacktrack` and `backtrack` are about 18% of q13's CPU. Literals between `%`s
  are a sequence of substring searches.
- **`First` and `Last` are slow over many groups.** Over 182k groups, `First` took
  0.8 s and `Last` 1.3 s, where `Sum` took 30 ms, in both key forms. `positionAcc`
  keeps a one-row column per group:
  - each batch builds a map and a `Take` per group;
  - `NBytes` walks every group;
  - `Finish` concatenates one part per group.

  It is why this step's tests run `First` and `Last` over 5,000 rows only.

Both are in the scope's "found during the steps".

## 6. Teeth

| tooth | result |
| --- | --- |
| a null row grouped by its slot's value | **bites:** `TestIntegerGroupKeysAnswerAsEncodedOnes`, `TestAPartitionedFoldOfIntegerKeys` |
| a frozen sink's resident null routed | **silent at first;** bites since `TestASpilledIntegerGroupKeyKeepsItsNull` |
| a frozen sink's null looked up by its slot's value | **silent at first;** bites since the same test |
| first appearance not recorded under `MaintainOrder` | **bites:** `TestGroupByMaintainOrderSurvivesSpilling`, `TestMemoryLimitIsNotASemanticKnob` and more |
| a merged null taken for key 0 | **bites:** the 8-thread cases, `TestAPartitionedFoldOfIntegerKeys` |
| a null numbered after its chunk's keys | **bites:** `TestIntKeyTableNumbersANullInOrder`, the 1-thread cases |
| growth that places the null | **bites:** `TestAPartitionedFoldOfIntegerKeys`, the root test's cases; the kernel test only once its null came first, before any 0 |
| every group-by keyed by its encoded bytes | **silent at first,** since the answers do not change; bites since `TestAnIntegerGroupKeyIsNotEncoded` |

**One change bites nothing, and rightly:** the null's `HashAt` as `IntHash(0)`, run
and silent. Any fixed hash routes every worker's null to one partition, and the
answer does not depend on which.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's. The kernel test's first
chunk was reordered after the gate, and its package rerun, with `-race`.
