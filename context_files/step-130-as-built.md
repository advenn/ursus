# Step 130 — as built

**Under the default budget, `Collect` counts its result, and refuses one too large to
hold.** It used to grow until the kernel killed the process. No benchmark was run,
by request: the change is checked by tests, and one measurement of a few million
rows in-process.

## 1. Why

After step 128, `j5` and `gb10` passed under 3 GB and 4 GB containers. Nothing in
ursus, though, counted the result `Collect` assembles: a query whose result alone
outgrew the container was killed, not refused. `CollectBatches`, `SinkParquet` and
`SinkCSV` stream; only `Collect` holds the whole result.

## 2. Three choices, each made against what the obvious version breaks

### The result is counted apart from what the operators hold

**The obvious version** retains the result in an account like any operator's. That
puts it in the total that `Over` reads, and an operator that can spill decides to
by `Over`.

A spilled group-by or join replays its partitions while the answers of the earlier
ones sit in `Collect`. Charged with them, each partition's sub-sink starts over
budget, freezes after its first batch, partitions again, and reaches
`maxSpillDepth` on data that is not skewed at all. `hashAggSink.releaseState`
documents the same trap, for the group-by's own answer. Spilling cannot help in any
case: a result cannot be spilled.

**So the ledger keeps two totals:**

- **`total`:** what the operators hold. `Over`, `Check`, `Used` and `Peak` read only
  this, so operators spill exactly as they would if the result were streamed, and
  `MemoryStats.Peak` means what it meant.
- **`kept`:** what only a result holds.

Each entry counts how many of its references are a result's. An allocation an
operator holds is the operators', whoever else holds it too. When the operator lets
go, it moves to `kept`. A group-by's answer is the case: it is charged to the
group-by while `Collect` holds slices of it, and becomes the result's when the
group-by closes. `Holding` is the two together, each allocation once.

### The limit is three quarters of the ceiling, not the budget

**The obvious version** refuses at the budget, half the ceiling. That would refuse
the `j5` runs that pass.

A join's build side is alive while its result arrives. From the tables' column
widths, not measured:

- `j5` holds about 0.85 GB of build side and 1.3 GB of result;
- about 2.15 GB in all;
- the default budget is 1.6 GB under a 3 GB cap, and 2.15 GB under 4 GB.

**The limit is three quarters of the ceiling, the budget and half as much again:**

- under the default budget, ursus sets the Go soft limit at nine tenths of the
  ceiling (step 128);
- a query holds up to about a fifth more than the ledger counts (step 129 measured
  1.09× to 1.2× on its joins and `Collect`s);
- three quarters, and a fifth more, is the soft limit. Past it the collector cannot
  keep the heap under the soft limit, and a container's limit kills the process.

`j5` under 3 GB is then estimated at 2.15 GB against 2.41 GB. That is a margin of a
tenth on an estimate. One run of `j5` under 3 GB would settle it.

### Only under the default budget

`WithMemoryLimit`'s doc promised that **a limit the caller gives bounds the
operators, and `Collect` holds whatever it is asked to.** The caller who sorts 10 GB
under a 512 MB limit and collects the answer is asking for exactly that. A limit of
the caller's own, or 0, therefore turns the check off, as it already kept ursus from
setting the soft limit (step 128).

## 3. What changed

**`internal/execopt`:**

- `entry.kept`, `counted`, `move`;
- `Budget.Holding`;
- `Budget.ResultLimit`: 1.5 × the limit under the default budget, else 0;
- `Budget.Result`: the result's account, or nil, a working no-op, when there is no
  result limit;
- `Account.CheckResult`.

**`internal/exec`:** `Collect(ctx, root, held)`. `drain` retains each batch in `held`
and checks it as the batch arrives. It refuses once the result is too large, not
once the stream ends: by then the memory has filled.

**`lazy.go`:**

- `Collect` passes `cfg.budget.Result()`;
- `Collect`'s doc has a section, "A result too large to hold is an error";
- `WithMemoryLimit`'s "Without it" and "What it does not bound", and
  `MemoryStats.Peak`, say the same.

The error, from the test:

```
ursus: collect: the result does not fit in memory: the query holds 416.0KiB,
416.0KiB of it the result so far, and may hold 384.0KiB, three quarters of the
512.0KiB this process may use
  CollectBatches streams the result a batch at a time, and SinkParquet and SinkCSV
  write it to a file; neither holds it whole
  this check comes with the default memory budget: WithMemoryLimit, with a limit of
  your own or 0, turns it off, and Collect then holds whatever it is asked to
```

## 4. What it does not do: `gb10`

**Step 128's record said `gb10` reached the 3 GB cap because `Collect` held its
ten-million-row result uncharged.** Read again, that is wrong:

- the group-by lets go of its state before it emits (`releaseState`);
- it charges its answer while `Collect` holds slices of it;
- `ConcatOwned` lets go of each column once it is copied.

**Measured in-process,** with internal/memcheck's `liveDuring` in a scratch test that
was not kept: the live heap above baseline, exact at every column a concatenation
copies, of a streamed group-by over a copying filter.

| group-by, 2,097,152 rows, one group per row | live | counted peak |
| --- | --: | --: |
| one Int64 key, `sum` | +142.6 MB | 122.3 MB |
| `gb10`'s shape: three String keys, three Int64, `sum` and `len` | +517.2 MB | 375.4 MB |

The six-key case is 142 MB over what it counts, about the size of its keys. Its
largest reading came inside a concatenation. A streamed serial group-by
concatenates in one place: `residentResult`'s `kernel.Concat(s.keyParts)`, which holds
every key part and the concatenated keys together.

**So the likely cause of `gb10`'s peak is its keys held twice while its answer is
assembled.** The fix would be `ConcatOwned` there, as step 128 did for `Collect` and
a join's freeze; `releaseState` drops the parts straight after. **It is not done
here:** this step was asked for `Collect`, and `gb10`'s result, about 0.85 GB, is
well under the new limit.

## 5. Tests and teeth

- **`TestAResultIsHeldApartFromTheOperators`** (execopt). An allocation shared with
  an operator counts once. One only the result holds moves neither `Used`, `Over`
  nor `Peak`. Each moves between the totals as the operator lets go and takes hold
  again.
- **`TestOnlyTheDefaultBudgetChecksAResult`** (execopt): the result limit under the
  default budget, a caller's limit, none, and a default with no ceiling.
- **`TestCheckResultRefusesPastTheResultLimit`** (execopt). An operator's holding
  counts toward the limit, and the error names `collect`, `CollectBatches`,
  `SinkParquet` and `WithMemoryLimit`.
- **`TestCollectRefusesAResultTooLargeToHold`** (root): 64 batches of 32 KiB under a
  256 KiB default budget.
  - It is refused after at most 16 batches read, of 64.
  - Streamed, the same query passes.
  - Under a caller's limit, or none, `Collect` holds all of it.
  - A ten-batch `Head` is collected.
- **`TestCollectSpillsAsTheStreamDoes`** (root): a 240 KB frame's rows reach `Collect`
  before a 48 KB sort reads. Collected and streamed, the sort spills nothing.
- **`TestParallelAggregationSwitchesToSerialUnderALimit`** now streams its result. A
  16 KiB default budget lets `Collect` hold 24 KiB, and its 14 KiB result with the
  spilled replay's state is more. What it checks is unchanged.

| tooth | result |
| --- | --- |
| `CheckResult` never refuses | **bites:** the execopt and root refusal tests |
| the result counted with the operators | **bites:** the ledger test and `TestCollectSpillsAsTheStreamDoes` |
| a caller's limit checks `Collect` too | **bites:** both |
| an allocation shared with an operator held twice | **bites:** the ledger test |
| refused only once the stream ends | **bites:** the batch count |
| `Collect` not given the account | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. The new test file was then gofmt'ed, one space, and its tests re-run with and
without `-race`. No PDS-H run, by request.
