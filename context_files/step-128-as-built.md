# Step 128 — as built

**The default memory budget holds.** This step changes the default behaviour,
decided with the maintainer. No benchmark was run, by request: the change is checked
by tests only.

## 1. What was wrong

ursus's default budget is half the memory ceiling, the smaller of the cgroup's
limit and the machine's RAM. The other half was meant for the Go heap's slack: with
GOGC's default, the heap grows to about twice what is live before the collector runs.

Measured, that margin did not hold:

- **Under an 8 GB cgroup** (step 127's report), h2o's six-key group-by `gb10` over
  CSV peaked at 7.60 GB against a 4 GB budget. A join of two ten-million-row tables,
  `j5`, had peaked at 8.19 GB in step 117's run.
- **Under a 4 GB cgroup,** in a run started after step 127 and stopped part way by
  the maintainer, `j5` over CSV was killed by the kernel at the cap, and `j5` over
  Parquet peaked at exactly 4 GB. That is the run that raised GNOME's "killed for
  memory" notifications.

**Three things made up the difference,** read from the code:

1. **Nothing bounded the heap's slack** unless the program called
   `SetProcessMemoryLimit`, which step 97 made opt-in.
2. **`Collect` closed the operator tree only after assembling the result.** A join's
   table and its whole build side were alive while every result batch, and then the
   concatenated result, were too. None of the result is charged to the budget.
3. **`kernel.Concat` held every input until its last column was copied,** so the
   input and the answer were both alive in full. That is twice the result in
   `Collect`, and twice the build side when a join freezes.

## 2. What changed

**The Go soft memory limit is set under the default budget.** The first query that
runs under the default budget calls `SetProcessMemoryLimit`, once per process, which
sets nine tenths of the same ceiling. Each of these keeps ursus from setting it:

- `GOMEMLIMIT` in the environment, "off" included;
- a limit already set with `debug.SetMemoryLimit`;
- a budget the caller gave with `WithMemoryLimit`;
- `LeaveProcessMemoryLimit()`, new.

The limit is the whole process's, which is why step 97 left it to the caller. It is
now set at the point where ursus already decides how much memory the process may
use, when it applies its default budget, and not on import.

**`Collect` closes the tree before it assembles the result.** `drain` pulls every
batch and closes the tree as it returns, then `ConcatOwned` concatenates.

Nothing a batch points into is freed by a `Close`:

- Arrow import copies its data;
- the Parquet reader copies each value out of its page before the next read;
- spill files are read into fresh buffers.

**`kernel.ConcatOwned`** is `Concat` for a caller that gives its batches up. It clears
the slice it is handed and lets go of each column's pieces once that column is
copied, so input and answer overlap by one column. `Collect` and the join's `freeze`
use it. The other `Concat` callers are left as they were; each would need its own
check that it does not read its parts afterwards.

**Documentation:** `SetProcessMemoryLimit`'s "why it is not automatic" became "why it
is automatic". `WithMemoryLimit`'s doc, the README's opening and the changelog say
the same, and the changelog lists it as a behaviour change.

## 3. What is not known

**No run has shown that `j5` now passes under 4 GB:** the maintainer asked for no
benchmarks.

The three changes each remove a measured part of the gap:

- the slack the collector leaves;
- the build side alive during the result's assembly;
- the result held twice.

How much each contributes, and whether `j5` now passes, takes one targeted run of
`j5` and `gb10` under a 4 GB cap, whenever the maintainer chooses.

### Measured afterwards, at the maintainer's request

After step 129, ursus alone, `gb10` and `j5` at ten million rows, one timed run
each, under a 4 GB scope:

| query | time | peak | before this step |
| --- | --: | --: | --- |
| `gb10`, CSV | 7.4 s | 3.88 GB | passed |
| `j5`, CSV | 8.6 s | 3.87 GB | killed at the cap |
| `gb10`, Parquet | 4.9 s | 3.87 GB | passed |
| `j5`, Parquet | 4.4 s | 3.87 GB | passed at exactly the cap |

**Nothing was killed.** Each peaked at nine tenths of the 4 GiB cap, where the soft
limit now sits: the collector worked harder there instead of letting the heap grow.

**The times match the 8 GB run of step 127:** `j5` over CSV 8.5 s, `gb10` 7.6 s.

The limit is soft. A query whose live state outgrows it can still be killed.

**Under a 3 GB scope:**

| query | time | peak |
| --- | --: | --: |
| `gb10`, CSV | 7.4 s | 3.05 GB |
| `j5`, CSV | 8.4 s | 2.92 GB |
| `gb10`, Parquet | 4.8 s | 3.22 GB, the whole 3 GiB |
| `j5`, Parquet | 4.4 s | 2.91 GB |

**Nothing was killed, but `gb10` reached the cap over Parquet.**

- **`j5`** sits at the soft limit, as under 4 GB.
- **`gb10` groups ten million rows into about ten million groups.** This record first
  said `Collect` held its result uncharged on top of the group-by's budget. **That
  was wrong** (step 130): the group-by lets go of its state before it emits, and
  charges its answer while `Collect` holds slices of it. What is uncharged is a
  second copy of its keys while it assembles that answer, measured in step 130.
- **It survived** because the kernel reclaimed page cache first. Under a smaller cap,
  or a busier one, it would likely be killed.

Step 130 made `Collect` refuse, under the default budget, a result too large to
hold. That does not change `gb10`, whose result is well under the limit.

## 4. Tests and teeth

- **`TestTheDefaultBudgetSetsTheProcessMemoryLimit`** (root, internal) stubs the
  ceiling and the default budget, then checks:
  - the first default-budget query sets nine tenths of the ceiling;
  - only once, so a limit changed afterwards is not set back;
  - not under a limit the caller gave;
  - not after `LeaveProcessMemoryLimit`;
  - not with `GOMEMLIMIT=off`;
  - not over a limit already chosen.
- **`TestConcatOwnedIsConcat`** (`internal/kernel`): the same answer as `Concat` over
  zero, one, two and seven batches of Int64, String and Float64 with nulls, and the
  slice cleared.
- **`TestDrainClosesTheTreeBeforeTheResultIsAssembled`** (`internal/exec`): a spy
  operator is closed by the time `drain` returns its batches.

| tooth | result |
| --- | --- |
| the default budget sets no limit | **bites** |
| an explicit limit sets it too | **bites** |
| `LeaveProcessMemoryLimit` ignored | **bites** |
| the owned batches stay held | **bites** |
| `drain` leaves the tree open | **bites** |

**Gate:** test-all 110 ok, race 22 ok, levels, vet ×3 and the bench engine tests
clean. The PDS-H run is left out: it goes through the benchmark harness, and the
maintainer asked for no benchmarks.
