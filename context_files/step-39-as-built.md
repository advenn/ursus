# Step 39 — as built

**`n_unique` stopped keeping a Go map per group.** q21's peak RSS nearly halves. It
also got 1.35x slower, which was a decision rather than a surprise, and the decision
is recorded here with the number that bought it.

Authoritative where it disagrees with [`step-38-as-built.md`](./step-38-as-built.md),
the vision docs and [`design/`](./design/).

**1533 test cases green** — 1529 before this step — under `GOEXPERIMENT=simd` ×
`GODEBUG=simd={512,256,128,0}`, with the experiment off, and under `-race`.
`make levels` and `go vet` clean in all three modules. PDS-H SF=0.1 validates 22/22.

---

## 1. What changed

```go
type nuniqueAcc struct {
    sets []map[string]struct{}   // one Go map PER GROUP
}
```

became one `KeyTable` keyed by a four-byte group id followed by the encoded value,
plus `counts []int32`. Distinctness per group falls out of `GetOrInsert` reporting
`inserted`. It is the structure the six group-identifying operators already use, and
the one `joinTable`'s doc chose over exactly the layout being replaced here.

`NBytes` stopped estimating 48 bytes an entry and became `tab.NBytes() + 4*groups`.

---

## 2. The trade, measured in both directions

PDS-H q21 at SF=1, through step 38's instrument:

| | before | after |
| --- | --: | --: |
| accounted (`MemoryStats.Peak`) | 0.81 GB | 0.59 GB |
| peak live heap | 3.86 GB | **2.03 GB** |
| VmHWM | 3.94 GB | **2.07 GB** |
| total allocated | 12.68 GB | 14.79 GB |
| collections | 41 | 70 |
| time | 3.7 s | **5.0 s** |

**1.9x less memory, 1.35x more time.** A targeted benchmark isolates why —
`BenchmarkNUnique`, 300k groups of 4 values, no IO:

| | old (map per group) | new (one table) |
| --- | --: | --: |
| time | 86 ms | 213 ms |
| bytes allocated | 115 MB | 189 MB |
| **allocations** | **1,800,043** | **161** |

Eleven thousand times fewer allocation *objects* for more allocated *bytes*. That
pair is the whole result: RSS is driven by what the collector has to trace and has not
yet swept, and 1.8M live map objects per query is what made the heap large. The maps
are *faster* because each one is tiny and hot, where one 5.5M-entry table means random
probes — the same memory-latency profile step 27 measured on the join's `KeyTable`.

A hypothesis that was wrong and is worth recording: the extra 2.1 GB of allocation
looked like table-growth rehashing, so `KeyTable` gained a `Reserve`. It changed
`TotalAlloc` **not at all** and recovered perhaps a tenth of the time. `Reserve` was
kept — skipping seventeen rehashes on the way up from 64 slots is right regardless —
but it did not explain anything.

### The decision

Taken deliberately: memory is the number the project could not defend, this is its
worst query on that axis, and the suite-wide figure moves from 4.7x polars to about
2.7x. The 1.35x is booked as debt against a named payer — `KeyTable.Get` is four
dependent random accesses and is already on the backlog, and `n_unique` now shares
that structure with the join, so fixing it would pay twice. **Step 27 already tried
the obvious version of that fix and it was 57% slower**, so the debt is real and might
not be paid.

Worth one more note against overclaiming: measured against the *published* q21 time of
5.08 s this costs nothing, because the baseline it regresses from is the newly
improved one from steps 36–37. That framing is true and would be spin; the honest
comparison is against the current tree, and against the current tree it is slower.

---

## 3. `nuniqueAcc.Merge` had no test at all

`TestParallelAggregationMatchesSerial` runs `n_unique` at every thread count and
**never calls `Merge` once** — instrumenting it to print on entry produced no output
for the whole test. The same method is 28% of q21's heap profile. So this step
rewrote a method that nothing exercised, which is the state step 13 and step 17 kept
finding methods in.

The fixture that reaches it: 200,000 rows over 5,000 groups at batch size 512 —
enough batches to spread across sinks, and two sinks that saw different keys first
number their groups differently. It fires seven times with a non-identity remap, and
without the remap 4,990 of 5,000 rows come out wrong.

**It only became reachable in step 37.** Before that, memsrc handed back one batch
however large, so an in-memory frame produced one sink and there was nothing to fold.
The coverage gap and the memory bug had the same cause.

---

## 4. A claim in a comment that the teeth disproved

The first version of this code said the four-byte prefix had to be fixed width
because *"group 1 with value `0x` and group 16 with value `x` encode to the same
bytes"*. The tooth for it — spell the prefix with `strconv.Itoa` — **passed**.

`GroupKeyEncoder` frames its own output: a validity byte, then a big-endian length
for variable-width types. `"0x"` encodes to `01 00 00 00 02 30 78`. So the byte after
any decimal prefix is always `00` or `01`, never a digit, and the collision cannot
happen.

Fixed width is still right — it makes this key's framing independent of how another
type frames its own, rather than correct by a coincidence between two decisions
nothing links — but the comment now says that instead of a bug that cannot occur.

---

## 5. Teeth

| tooth | result |
| --- | --- |
| drop the group prefix | **bites** — every group reports the global distinct count |
| spell the prefix in decimal | **does not bite** — §4 |
| `Merge` without the remap | **bites** once a fixture reaches `Merge` at all — §3 |
| drop `Merge`'s out-of-range source skip | does not bite; the branch is defensive and no test produces a short remap |
| restore an estimating `NBytes` | not a correctness tooth. On a 20,000-group fixture the exact number is 15.2 MB against 1.3 MB for a per-group-only estimate. `n_unique` is one of the two aggregates that REFUSE to spill, so this number decides when the refusal fires |

---

## 6. What is still open

- **`KeyTable.Get`'s memory latency** — now the payer for this step's debt, and it
  would benefit the join and `n_unique` together. Step 27's obvious layout fix is
  refuted; what is left is reducing the arena access, not the indirection.
- **`quantile` and `median`** keep every value of a group and have the same shape of
  problem, untouched.
- **`JoinWhere`**, the other half of step 38's finding: q21 only runs `n_unique`
  because ursus cannot express the non-equi join.
- **The allocation rate** — now 14.79 GB for a ~2 GB working set, which wants an
  `alloc_space` profile.
- **Re-running the suite**, whose timings predate steps 36–37 and whose memory
  numbers now predate this one.
- The standing list: the join build side (`Merge`, still never called), Right/Full
  parallel probe, nested writing and `as_struct`, `.list` set operations, Map/Array,
  Pivot/Unpivot, SQL, cloud stores, join reordering.
