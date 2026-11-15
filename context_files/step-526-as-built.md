# Step 526 — as built

**Step 523's spilled float-pick test no longer depends on scheduling.** A test-only
change.

## What it was

`TestAFloatPickIsTheSameSpilled` (step 523) runs its query under a memory limit low
enough that the group-by spills, which `collectPicks` asserts. The limit is query-wide,
and at 4 and 8 threads the batches in flight vary with scheduling. "many groups" at
512 KiB failed on CI's `test (128)` leg on PR #76:

```
threads 4 batch 7 ordered false limit 524288: ursus: group_by: memory limit of 512.0KiB
exceeded: the query is holding 513.8KiB, 4.0KiB of it in this operator
```

Step 523 had already raised it once, from 256 KiB.

## What it is

Each shape's limit is chosen inside the window where the group-by spills, with room
for the batches in flight:

| shape | spills up to | fails to spill at | limit |
| --- | --- | --- | --- |
| few groups (1,500 rows, 41 groups) | 96 KiB | 128 KiB | 80 KiB, was 64 KiB |
| many groups (6,000 rows, 2,000 groups) | 2 MiB and more | not reached | 2 MiB, was 512 KiB |

## Checks

The test ran 40 times at `GOMAXPROCS=4`, 20 times at 2, and 3 times under `-race`,
with no failure, each spilling. `go vet`, gofmt and `testinventory -check` pass.
