# Step 527 — as built

**The 0.6.6 memory-limit test gives `NanMin`'s group-by room for step 523.** A
test-only change.

## What it was

`TestV066UnderAMemoryLimit` (step 287's report check) runs four group-by queries over
2,000 keys under 64 KiB, each required to spill. Its comment records the edge: "its
states sit at 16 KiB's edge". Since step 523, a float `NanMin` keeps the row of a zero
or NaN it picks, so the partial state can tie-break in row order, and "M1, the bits
and odds" (BitwiseXor, NanMin, HasNulls, ApproxNUnique by key) holds a little more.
On PR #79, CI's scalar-only leg failed it:

```
under 64 KiB: ursus: group_by: memory limit of 64.0KiB exceeded: the query is holding
64.2KiB, 1.2KiB of it in this operator
```

## What it is

That query's limit is now 80 KiB, set per query in `byKeyLimit`. The other three stay
at 64 KiB, because "M3, unique_counts" no longer spills at 96 KiB, and the test
requires every group-by query to spill.

| query | limit | spills |
| --- | --- | --- |
| M1, the bits and odds | 80 KiB, was 64 | yes, at 80; failed at 64 on CI |
| M3, unique_counts; M1, spearman by k; M1, mode by k | 64 KiB | yes; unique_counts not at 96 |

## Checks

The whole test ran 30 times scalar (`GOEXPERIMENT=` `GOAMD64=v1`), 30 times with simd
and v3, and 3 times under `-race`, with no failure. vet, gofmt and `testinventory
-check` pass.
