# Step 426 — as built

**A join past its deepest split names what holds the memory.** The plan's 434, M2 of
`v0.7.2-plan.md` (H3d1). `feature-gaps.md` row 66, found at step 307.

## 1. What was wrong

A join's sub-sink past `maxSpillDepth` that holds one key cannot split it, and
refuses (`oneKeyOver`, `internal/physical/extjoin.go`). It is reached when the
account is over, and an account is over when the query is over the limit, whoever
holds the memory. Step 180 taught it two causes:

- the key's rows;
- the sink's own spill writers.

It never asked whether the sink was over the limit at all. A key that fit the limit
alone, beside other operators holding the rest of it, was refused as "a single join
key has more build rows than the limit". If a writer was open, it was blamed instead.

The record of it: step 307's three frames aligned under 64 KiB, 20,000 keys each held
once but a few, refused about one key. The other join held 64.0 KiB of the 65.4, and
this one 1.4.

## 2. The change

**`oneKeyOver` names three causes, asked in order.**
1. If the sink holds no more than the limit, the other operators are refused, a new
   text naming the query's bytes and theirs:

   > memory limit of 64.0KiB exceeded: the query is holding 65.4KiB, 64.0KiB of it in
   > its other operators, beside this join's 1.4KiB of one key

2. If the rows fit without the writers, the writers.
3. Otherwise, the key's rows, as before.

**What is refused is unchanged,** and only the text differs: with one key past the
deepest split, nothing below can divide it.

## 3. Tests

- **`TestOneKeyOverNamesTheOtherOperators`** (`internal/physical`): a sub-sink at
  `maxSpillDepth` holding one key under a 64 KiB limit. Each case must name its cause
  and not the other:
  - the key's 80 KiB names the key;
  - sixteen writers name the writers;
  - another account's 60 KiB names the other operators and their 60.0 KiB;
  - the same 60 KiB with a writer open still names the other operators.
- **`TestAlignedFramesUnderASmallLimitBlameNoKey`** (root): step 307's frames under
  64 KiB. It either answers, equal to the run without a limit, or is refused naming
  the other operators and not one key. Unfixed it named the key.
- The J11 tests (`joinspill_j11_test.go`) and `memory_test.go`'s join cases keep
  their texts, the key's and the writers'.

**Teeth: 3 of 3 bite.**
- the third case removed;
- the query's bytes named as the other operators';
- the writers asked first.

**Gate:**
- `./internal/...` and the root package in full;
- vet and levels;
- on the pull request, CI's every width, the scalar build and the race detector.
