# Step 180 — as built

**R2 of `v0.6-scope.md`, its last row:** J11, time-boxed to one step. Its cause was
found, and the step fixed it.

## 1. The row

> The spilled join refuses with "a single join key has more build rows than the limit"
> when no key has more than two. The real cause was not found.

It had been open since the audit at step 69. The audit kept no reproduction.

## 2. Reproduced

Probes over sizes, joins and limits, through an overlay so the tree was untouched:

- **No refusal** in 30 shapes of narrow rows: Inner and Left joins, 2,000 to 200,000
  build rows two to a key, limits from 16 KiB to 4 MiB. Each spilled cleanly, with up
  to 8,736 files.
- **No refusal** with string keys, two-column keys, Right and Full joins, or batches
  of one row, at limits down to 4 KiB.
- **Refused**, as the row says, with **400-byte rows under 4 KiB**: 20,000 build rows,
  two to a key.

## 3. Traced

An instrumented copy of `extjoin.go`, again through an overlay, printed each split's
state:

1. **When each sub-join starts, nothing is held:** the budget's use is 0 at every
   `newSub`.
2. **At level 3 a sub-join holds six rows, three keys,** and uses 4,112 bytes against
   4,096: 2.4 KB of rows and 1.2 KB of key table. It splits.
3. **Once it has split, its open partition writers keep it over the limit by
   construction,** at `partBufBytes`, 8 KiB, each. A level-3 sink used 24,576 to 32,768
   bytes with no rows held, so it spilled everything.
4. **At level 4, `newSub` refused the next level on the depth alone,** before reading a
   row of a bucket that held one key and about 800 bytes. Its message blamed a key.

Two causes, then:

- **The refusal rule.** `maxSpillDepth`'s own comment says termination does not
  depend on it, and that only the recursion can tell a bucket that did not narrow.
  The code checked no narrowing.
- **The message.** Under a limit smaller than a few of its own spill writers, every
  sub-join that spills is over the limit whatever its keys, and that was reported as
  a key too large.

## 4. Fixed

- **`newSub` refuses only past `maxSpillLevels`, 32.** Each level hashes afresh, so
  two distinct keys share a bucket at a level with probability 1/16, and 32 levels are
  not reached while they remain.
- **`oneKeyOver`:** a sub-sink at `maxSpillDepth` or deeper that must split again is
  refused only when it holds one key. That is the evidence partitioning cannot help.
  - If its rows fit once its open writers are left out, it is the writers that are
    over: "the join's own spill writers need more, 3 of them buffering 8.0 KiB each,
    before any row is held", with a hint of the 128 KiB a spilling join needs.
  - Otherwise it is the key: "a single join key has more build rows than the limit".
- **The writers are still charged.** Leaving them out would make the limit a soft
  one, and those bytes are real memory.

**Left: the group-by.** `hashAggSink.newSub` refuses on the depth the same way. A
sub-sink holding one group freezes, and is refused by `overBudget` with an accurate
message. So only a bucket still meeting new keys reaches the fifth level, and
partitioning divides those. But a test needs hundreds of keys that share buckets
through five levels, millions of rows. The change was made, its tooth was silent, and
it was reverted. The comment beside `maxSpillDepth` says so.

## 5. Tests

- **`TestASpilledJoinDividesPastFourLevels`:**
  - keys 1 and 248,238, found by searching four million keys, share a non-zero bucket
    at each of levels 0 to 4 and part at level 5;
  - with 60 rows of 400 bytes each under a 40 KiB limit, every hybrid split spills
    both, and the sixth level divides them;
  - it answers 121 rows, where it was refused at the fifth level.
- **`TestJ11KeysShareFiveLevels`** (internal) pins that pair, so a change of hashing
  fails here rather than leaving the first test vacuous.
- **`TestASpilledJoinNamesWhatIsOverTheLimit`:**
  - one key of 80 KiB under 40 KiB is refused as a key;
  - keys of two rows under 4 KiB are refused as the writers, not as a key.

| tooth | result |
| --- | --- |
| the join refusing at a fifth level again | **bites:** the descent past four levels |
| one key's evidence not asked for | **bites:** both join tests |
| the writers' floor reported as a key | **bites:** the naming test |
| the group-by refusing at a fifth level again | **silent:** no affordable shape reaches it, so the change was reverted (§4) |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
