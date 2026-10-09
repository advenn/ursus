# Step 151 — as built

**Item 3 of `v0.5-scope.md`: a partitioned, parallel join build.**

## 1. Evidence first

**Step 141:** q12's build was 17% of its CPU, and step 126 found it half of q12's
time.

**Measured on its own:** a join of four million probe rows against two million
distinct build keys, on eight threads.

| | per run | build's CPU |
| --- | --: | --: |
| before | 387 to 401 ms | 0.61 s over three runs, about 0.20 s a run |

**The build was about 55% of the wall clock,** all of it on one goroutine:
`joinBuildSink.admit` inserted each row's key, row by row, into one `KeyTable`. The
probe after it already runs on every core: 3.17 s of CPU over three runs, about
130 ms of wall clock a run.

## 2. What changed

### Deferring the build (`internal/physical/joinbuild.go`, `join.go`)

**`planJoin` sets `deferred` (`deferBuild`)** when the streaming build would hold
every row anyway and nothing could need to spill it from the first batch:

- several threads;
- a key;
- every build row retained, which leaves out Semi and Anti without a residual,
  since they keep keys only;
- no limit the caller set.

**`Consume` then only retains the batches.**

**Past half the budget, `catchUp` replays the retained batches through `admit`,**
in order, and the build streams from there. Only the streaming build can split and
spill, so it is checked before the escalation check could need it.

**`freeze` calls `buildPartitioned`:**

1. **Route.** Each batch's keys are evaluated and hashed, a batch per goroutine, and
   each row is routed by the top half of its hash (`kernel.PartitionOf`, now shared
   with step 150's fold). A null key, unless nulls are equal, goes to no partition.
2. **Insert.** One goroutine per partition inserts that partition's rows into a
   `KeyTable` of its own, in global row order, 64 keys at a time through
   `GetOrInsertHashed`, re-encoding the keys from the evaluated columns. Global row
   order is what makes a key's local id its first row's, and a duplicate met at the
   row the serial build meets it.
3. **Renumber.** The tables are read as one, `kernel.KeyParts`, whose ids are a
   table's base plus its local id, and every row's key is rebased.

**What this keeps.** `counts` and `rowKey` then have the streaming build's shape,
so these are unchanged:

- `freeze`'s CSR, so a key's rows stay ascending, which a Left join's match order and
  the flush depend on;
- the probe;
- a Right or Full join's flush.

**The probe** reads through `joinKeys`, an interface `KeyTable` and `KeyParts` both
satisfy. `KeyParts.GetMany` hashes each key once, reads its first slot in its own
table before comparing any, and adds the base.

**Validation.** Under `ValidateManyToOne` or one-to-one, each partition records its
first duplicate row. The refusal names the least of those, the row the streaming
build refuses at, with the same message.

**Accounting.** The tables, `counts` and `rowKey` are charged as `reaccount` charges
the streaming build's, before `freeze` rebases. `TestJoinAccountsItsHashTable`
caught the first version leaving them off the ledger.

## 3. Measured

The same probe, the two builds alternated, three runs each:

| | per run |
| --- | --: |
| before | 387 to 401 ms |
| partitioned build | 266 to 341 ms |

**−22 to −30%.** The build is now about 58 ms of wall clock, at about 2.5 times the
CPU. Its inserts wait on memory as the probe's do, and a fifth of it is one table
doubling.

**PDS-H q12 at SF=1,** against step 150's commit, alternated, six rounds of five
iterations:

| | round medians | peak RSS |
| --- | --- | --: |
| before | 325, 328, 370, 324, 343 and 375 ms | 343 to 353 MB |
| partitioned build | 277, 323, 345, 284, 279 and 324 ms | 338 to 361 MB |

**About −10 to −17%,** in a query that also reads, filters and probes.

Reserving each table for half its rows is the compromise:

- a build of distinct keys doubles once more;
- one of a few keys over many rows does not hold slots it never fills.

## 4. Tests

**`internal/physical/joinbuild_internal_test.go`,** through `planJoin` over memory
scans in batches of 512:

- **`TestThePartitionedBuildAnswersAsTheStreamingOne`:**
  - Inner, Left, Right and Full, with nulls equal and not;
  - a probe side of 9,000 rows and a build side of 12,000 rows, about two build rows
    a key, and one key in forty null;
  - on four threads, against one: the same rows in the same order;
  - the build deferred, and the probe read `KeyParts`, holding as many keys as the
    streaming build's table.
- **`TestThePartitionedBuildRefusesADuplicateAtTheSameRow`:** eight fixtures under
  `ValidateManyToOne`, each refused with the streaming build's message, row included.
- **`TestADeferredBuildPastHalfItsBudgetStreams`:**
  - a budget of 96 KiB, marked default, so the join defers and then catches up;
  - it spills;
  - its rows, sorted, are the streaming build's, and its probe read one `KeyTable`.
- **Under `-race`:** all three, clean.

## 5. Teeth

| tooth | result |
| --- | --- |
| never deferred | **bites:** the differential and the catch-up test |
| ids not rebased | **bites:** the differential |
| the probe's ids not rebased | **bites:** the differential |
| null keys put in a partition | **bites:** the key count |
| the first duplicate is any partition's | **bites:** the duplicate test |
| no catch-up | **bites:** the catch-up test |
| the partition tables not charged | **bites:** `TestJoinAccountsItsHashTable` |
| rows inserted out of row order | **bites:** the duplicate test |

**Two were silent at first, and the tests grew:**

- **A null key put in a partition** changes no answer, since a null probe key looks
  nothing up. The differential now compares the two builds' key counts.
- **The first duplicate taken from partition 0** was, on one fixture, also the first
  of all. The duplicate test now runs eight.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's; every join in it has a
key and defers.
