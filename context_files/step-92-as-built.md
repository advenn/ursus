# Step 92 — as built

**A group-by under a limit you set is serial again, so its row order is
reproducible.** This was found by CI on the commit tagged v0.3.1:
`TestGroupBySpillIsDeterministic` failed, because two runs at one limit gave two
group orders. It failed on two of the three runs since step 91, and never locally.

One fix with its tests, and this document.

## 1. What was wrong

Step 91 let a group-by under any budget run parallel until half of it. Where it
then switches to serial depends on which worker crosses half the budget first, and
how many batches were queued — that is, on thread scheduling.

An unordered group-by's row order depends on where it switched: which groups were
resident, and which went to spill files. So the order moved from run to run. The
contents never did.

`WithMemoryLimit` documents that the order depends on the limit, which is true; the
test held it to more than that, the same order at the same limit. That stronger
property is worth keeping where the caller chose the limit.

## 2. The fix

- **Under a limit the caller set:** serial, exactly as in v0.3.0. The order is the
  same on every run, at v0.3.0's speed.
- **Under the default budget (step 90):** parallel until half the budget, as step 91
  built it. A switch there needs half the machine or the container, and is rare.
  `WithMemoryLimit`'s doc says that the order of such a group-by can differ between
  runs; `MaintainOrder` or a limit of your own makes it reproducible.
- **The budget records whether its limit is the default** (`MarkDefault`), and
  `aggWorkers` reads it.

## 3. Tests and teeth

**Tests:**

- **The order, under an explicit limit:** a spilling group-by run six times on eight
  threads gives one order. With the gate removed it fails here, and not only on CI:
  the original test raced only sometimes, and this one is built to.
- **The marking:** with no option the budget is marked default; with
  `WithMemoryLimit(n)` and with `WithMemoryLimit(0)`, it is not.
- **The switch test** runs under a default budget lowered through a test hook,
  `SetDefaultMemoryLimit`. An explicit 16 KiB limit would now keep it serial and
  test nothing.

**Teeth: 2 of 2 bite.** The explicit gate removed, run 20 times. The default not
marked: silent at first, because the switch test still spilled serially; it bites
since the marking test was added.

## 4. Still open

**The v0.3.1 tag contains the flaky test.** A v0.3.2 carries this fix.
