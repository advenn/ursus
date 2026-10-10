# Step 159 — as built

**Item 4 of `v0.6-scope.md`, its join half: a key of one integer column is never
encoded.**

## 1. Evidence first

Step 157 found encoding keys and looking them up a fifth to half of six of seven
PDS-H queries:

- **in the build,** `GroupKeyEncoder` writes each key through a closure per row: a
  validity byte and the value, 9 bytes for an Int64;
- **`KeyHash` hashes those bytes,** a byte at a time past the first eight;
- **the table compares them** with `bytes.Equal` through its arena, two dependent
  reads from the slot.

Nearly every PDS-H join is on one integer column, where none of that is needed.

## 2. What changed

**`kernel.IntKeyTable`** (`internal/kernel/intkeys.go`):

- the key is the int64 itself, hashed with one `Mix64` (`IntHash`), compared with
  `==`, and kept in a flat slice by id;
- its slots keep `KeyTable`'s layout, the hash's top half over id+1, its load factor
  and its batched reads, a chunk's first slots read before any is compared;
- a miss is `-1` from `GetMany`, with no second answer.

**`kernel.IntKeyParts`** reads several tables as one, each table's base plus its local
id, as `KeyParts` does.

**`kernel.IntKeys` widens a column to int64 by its bits.** Signed types are
sign-extended, unsigned ones zero-extended, and so are the types stored as integers
(Date, Datetime, Duration, Time). Each width is a bijection, and both sides of a join
are cast to one key type first, so equal keys widen equal. An Int64 column's values
are its own, with no copy.

**The join's partitioned build** (`physical/joinbuild.go`) uses them when the key is
one integer column and nulls match nothing (`intKeyed`):

- the batches' values are routed by `IntHash` and inserted into one `IntKeyTable` a
  partition;
- the duplicate check, the counts and the rebase are the encoded build's;
- `joinTable.ints` holds the tables, in place of `ids`.

**The probe** (`startBatch`) reads the batch's integers from the column and looks
them up 64 at a time, with no encoder and no chunk of copied keys. A null row is
looked up like the rest and then marked, rather than branched on per row.

**What stays on encoded keys:**

- the streaming build, the one that can spill: a spill routes by the key's bytes;
- a key of several columns, or of a float, string or Int128;
- a join under `NullsEqual`, where a null is a key.

The probe still builds its encoder each batch, since `Validate`'s left-uniqueness
check encodes a key. Semi and anti joins build serially today, so they get the integer
path when item 3 defers them.

## 3. Measured

Step 158's runner against this one, alternated, three rounds of five iterations at
SF=1. CPU time per iteration is from rusage, warm-up included:

| query | CPU per iteration | median wall |
| --- | --- | --- |
| q17 | 1,510 → 1,078 ms (−29%) | 244 → 193 ms |
| q16 | 366 → 312 ms (−15%) | 99 → 88 ms |
| q7 | 3,044 → 2,599 ms (−15%) | 522 → 481 ms |
| q12 | 1,180 → 1,033 ms (−12%) | 333 → 307 ms |
| q2 | 318 → 284 ms (−11%) | 66 → 67 ms |

q17's first round was noisy, step 158's runs spreading from 1,512 to 2,331 ms; the
table gives its second round, whose runs agree within 1%.

**A q17 profile afterwards:**

- ZSTD is 39%;
- the integer lookups are 17%, with nothing encoded or byte-hashed;
- step 157 had keys at 40% of q17.

**What the lookups still cost.** q17's build is a few hundred parts, so its
partition tables are tiny and almost every lineitem row misses. A miss still pays a
hash, a partition and a probe chain of unpredictable length, about 23 ns a row.
Runtime filters (item 6), or a dense array or bitmap over a narrow key range, would
reject those rows sooner. Not here.

## 4. Tests

- **`TestIntKeyTableNumbersAsAMap`:** keys drawn to repeat and to reach every width's
  extremes, from 0 to 50,000 of them, numbered as a map numbers them, through growth;
  and keys never inserted, not found.
- **`TestIntKeyPartsNumbersAcrossTables`:** five tables routed by `PartitionOf`, read
  as one.
- **`TestIntKeyTableComparesTheKeyNotItsTag`:** 107450 and 189447 share their hash's
  top 32 bits and its low six, so in a new table one's search meets the other's slot
  first. They were found by search; random keys almost never do both.
- **`TestIntKeysWidensByBits`:** each width's extremes, an Int64 column read without
  a copy, and a payload-free column read as zeros.
- **`TestTheIntegerKeysAnswerAsTheEncodedOnes`:** inner, left, right and full joins
  over Int8 to Uint64, Date, Datetime, Int32 against Int64 and Uint32 against Int64,
  all against the streaming build's encoded keys, row for row.
- **The step 151 tests** now check which table the probe read: integers unless nulls
  are equal.
- **`TestJoinAccountsItsHashTable`** checks both tables:
  - on one thread, the streaming build's, with its 1 MiB floor as before;
  - on four, the integer tables: 0.97 MiB at the peak when charged and 0.48 MiB when
    not, so their floor is ¾ MiB.

## 5. Teeth

| tooth | result |
| --- | --- |
| a null probe key looked up and kept | **bites:** `TestJoinKeysByHand`'s J5 control, the spilling join's four kinds |
| a null build key inserted | **bites:** `TestTheIntegerKeysAnswerAsTheEncodedOnes`, the spilling join's tests |
| integer keys under `NullsEqual` | **bites:** `TestJoinNullKeysDoNotMatch`, `TestInnerJoinIsAFilteredCross`, step 151's test |
| the parts read without their bases | **bites:** `TestIntKeyPartsNumbersAcrossTables`, `TestFullJoinCoalescesItsKeys` and more |
| a key found by its tag alone, reading or inserting | **silent at first;** bites since `TestIntKeyTableComparesTheKeyNotItsTag` |
| growth that keeps the old slots | **bites:** `TestIntKeyTableNumbersAsAMap`, the spilling join's tests |
| an unsigned key sign-extended from its width | **bites:** `TestIntKeysWidensByBits` |
| a payload-free column's stale scratch | **bites:** `TestIntKeysWidensByBits` |
| the integer tables not charged | **bites:** `TestJoinAccountsItsHashTable`, on four threads |

**The sign-extension tooth bites only the kernel test, and rightly.** A join never
sees two widths in one table: both sides are cast to their meet type first. So any
bijection would answer the same.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
