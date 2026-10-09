# Step 138 — as built

**The Parquet reader's fixed-width columns keep their values slice, and a join's
`rowKey` and `counts` double as they grow.** These are items 1 and 2 of step 137's
profile of h2o `j5`. No benchmark was run, by request.

## 1. The Parquet reader: step 136's mistake, the second copy

**`fixedCol.finish` and `fixedElems.finish`,** the latter for a List's elements,
dropped their values slice every batch. Their comment said `data.NewFixed` wraps it.
`NewFixed` copies, so each batch's values were allocated twice: 0.74 GB of `j5`'s
profile over Parquet.

**Both now keep the slice,** as step 136 made the CSV reader do. The comment says
why it is safe.

**One more stale comment,** left by step 136: the CSV `stringBuilder.finish` still
said `fixedBuilder` must drop its slice. It now says the opposite.

**`TestReadingAllocatesAboutWhatItProduces`** (`internal/source/parquet`), as the CSV
reader's:

- 400,000 rows of three Int64 columns, serially and on four threads;
- measured from the second batch on, against the bytes produced;
- it then checks the first batch is intact after the rest was read.

| run | before | after |
| --- | --: | --: |
| serial | 2.13× | 1.13× |
| four threads | 1.99× | 1.06× |

The bound is 1.3.

**`TestAListColumnsFirstBatchSurvivesTheRest` is new.** It reads 5,000 List(Int64)
rows in batches of 64 and decodes the first batch only after the rest. No test did:
`readLists` decodes each batch as it arrives. So nothing checked that the List
elements' reuse is safe.

## 2. The join: `rowKey` and `counts`

A join's build keeps one key id per build row, `rowKey`, and one row count per key,
`counts`. Both grew by `append`, a row or a key at a time, at four sites:

- `admit`;
- the no-key path;
- twice in `Merge`.

Past 256 elements that is quarter steps, about five times the final size: about
0.8 GB of `j5`'s profile.

**Each site now makes room once with `kernel.Extend`,** which doubles:

- a batch's rows, in `admit` and the no-key path;
- a chunk's new keys, after each `insert`;
- the merged sink's new keys and the other sink's rows, in `Merge`.

`extend` from step 134 is exported as `Extend` for this.

**`TestAJoinBuildAllocatesAboutTwiceWhatItHolds`** (`internal/physical`) builds an
inner join on a million distinct keys, in batches of 8,192, through `joinFixture`.
It reads the bytes allocated against the state the sink counts: the key table,
`rowKey` and `counts`.

| | before | after |
| --- | --: | --: |
| allocated / held | 117.0 / 50.9 MB, 2.30× | 90.3 / 50.1 MB, 1.80× |

The bound is 2.0, as step 134's accumulators allocate 2.0.

## 3. Not measured

- **Speed.** No benchmark was run.
- **`j5` at ten million rows.** At step 137's figures, two things should go:
  - the Parquet reader's 0.74 GB;
  - about 0.5 GB of the join's 0.8, since doubling allocates two times the final
    size where `append` allocated five.

  That is 8% of the Parquet profile and 5% of the CSV one. An estimate.

## 4. Teeth

| tooth | result |
| --- | --- |
| the Parquet column drops its values slice | **bites:** the allocation test |
| a List's elements drop theirs | **silent** |
| `NewFixed` wraps its slice instead of copying | **bites:** both new Parquet tests |
| `rowKey` grows by `append` | **bites:** the join allocation test |
| `counts` grow by `append`, a key at a time | **bites:** the same |
| `Merge` makes no room for its new keys' counts | **bites:** `TestJoinBuildSinkMergeRemapsIds` |
| `Merge` writes the other sink's rows over its own, either kind | **bites:** the same, both |

**The silent one** costs allocation only, and nothing measures a List's
allocations. The List test checks that keeping the slice is safe, which is what
could go wrong.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. No benchmark, by request.

## 5. Measured afterwards, at the maintainer's request

`gb10` and `j5` at ten million rows, ursus alone, one timed run each, under a 3 GB
scope, at `97ab4fc`. Beside each, step 136's run, at `55fb091`. CPU is the scope's,
from the journal.

| query | time | CPU | VmHWM | heap in use | allocated | counted | collections |
| --- | --: | --: | --: | --: | --: | --: | --: |
| `gb10`, CSV | 6.5 s (6.6) | 19.7 s (20.1) | 2.75 GB (2.75) | 2.65 GB (2.71) | 14.9 GB (14.9) | 1.65 GB (1.65) | 34 (31) |
| `j5`, CSV | 8.2 s (8.8) | 44.5 s (46.9) | 2.85 GB (2.89) | 2.83 GB (2.89) | 11.7 GB (12.0) | 1.26 GB (1.21) | 25 (27) |
| `gb10`, Parquet | 3.8 s (3.7) | 15.0 s (14.8) | 2.72 GB (2.85) | 2.77 GB (2.78) | 16.0 GB (16.4) | 1.61 GB (1.61) | 31 (32) |
| `j5`, Parquet | 4.2 s (4.4) | 40.7 s (41.1) | 2.92 GB (2.89) | 2.78 GB (2.79) | 16.3 GB (17.4) | 1.09 GB (1.04) | 29 (33) |

**`j5` allocates less, by less than §3 estimated:**

- over Parquet, 1.1 GB less, 6%, against an estimated 8%;
- over CSV, 0.3 GB less, 2.5%, against 5%.

The estimate took shares of the profile, which counted a warm-up and a timed run, and
applied them to these runs' totals. The join's half of it came in under its share.

**`gb10` over Parquet allocates 0.4 GB less,** from the Parquet reader's half of the
change. Over CSV it does not touch this step's code, and did not move.

**What the join counts rose** by 0.05 GB for `j5`: `rowKey` and `counts`' unused
capacity, now counted.

**CPU and time are within the noise,** the largest change being `j5` over CSV at 5%
less CPU.
