# Step 432 — as built

**A group-by's release of each aggregate's state as its column is made is measured.**
The plan's 441, M2 of `v0.7.2-plan.md` (H3f4): a silent tooth from step 131, "a
median's and an implode's memory unmeasured". It is measured, and nothing needed
fixing: the state is let go.

## 1. What was open

Step 131 made `residentResult` let go of each piece of a group-by's state as soon as
the answer has what it needs from it (`internal/physical/agg.go`). Each accumulator
is dropped as its column is made, `accs[i] = nil`. The step's memcheck tests measured
the keys and the key parts, but not the accumulators. A median or an implode holds
every value of every group, and its tooth went silent.

The plan's condition: if the state is not released today, that is a resource bug,
fixed in the same pull request. It is released.

## 2. The change

- **`physical.AggColumnDone(i)`** is a nil-checked hook, called once `residentResult`
  has made aggregate i's column and dropped its accumulator. It follows
  `kernel.ConcatColumnDone`'s pattern, and nothing but memcheck sets it.
- **`TestAGroupByLetsGoOfEachAggregateAsItFinishes`** (`internal/memcheck`) groups a
  million rows, two a key, by their key, with a median, a sum and an implode, on one
  thread and without a limit.
  - It reads the live heap after two collections at two points: at the last column
    the keys' copy makes, before any aggregate finishes, and at each aggregate's
    hook. `Collect`'s own copies of the result come after the aggregates and are left
    out.
  - Two runs read the same: 59.8 MB once the keys are copied, then 44.0 MB after the
    median, 43.3 MB after the sum, and 37.0 MB after the implode.
  - The median's column making the heap fall by 15.8 MB is its state, less its
    answer's 4 MB.
  - The bound is a fall of at least 8 MB, half of that. Had the state been kept, the
    heap would rise by the answer.
- **Plan size, reduced:** the plan asked for 2,000,000 rows by 1,000,000 keys. It runs
  a million rows by 500,000 keys, about 60 MB, in a third of a second: the critic's
  concern that memcheck runs in every local gate.

## 3. Teeth

| mutation | |
| --- | --- |
| each accumulator kept to the end of the loop | **bites:** the heap rose 3.8 MB |
| the hook never called | **bites:** no readings, the guard |
| the sink keeping its accumulator slice | silent, an equivalence |

The equivalence: `residentResult` takes `accs := s.accs`, and the two share a backing
array. `accs[i] = nil` clears the sink's element too, so keeping the slice keeps no
accumulator.

**Gate:**
- `./internal/...` and the root package in full;
- vet and levels;
- on the pull request, CI's every width, the scalar build and the race detector.

The public API is unchanged.
