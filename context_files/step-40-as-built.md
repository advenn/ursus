# Step 40 — as built

**The optimisation did not land, again — and this time the instrument that says so
is the thing that shipped.** `KeyTable` is byte-for-byte as it was. What is new is
`BenchmarkKeyTable`, which step 27 asked for by name three steps before anyone could
use it, and a measured answer to the question it was asked.

Authoritative where it disagrees with [`step-39-as-built.md`](./step-39-as-built.md),
the vision docs and [`design/`](./design/).

No `internal/` change. **1533 test cases still green** under `GOEXPERIMENT=simd` ×
`GODEBUG=simd={512,256,128,0}`, with the experiment off, and under `-race`.
`make levels` and `go vet` clean in all three modules. PDS-H SF=0.1 validates 22/22.

---

## 1. The profile still points here, which was worth checking first

Step 27's "71% of the join is `KeyTable.Get`" was measured on a **serial** probe, and
steps 36–37 put the probe on eight workers. Re-profiled before touching anything:

```
joinProbeOp.enter                   2430ms  67.31%
  kernel.(*KeyTable).Get            2040ms  56.51%   flat 1300ms
    memeqbody                        420ms  11.63%
    kernel.(*KeyTable).keyAt         200ms   5.54%
```

56.5% rather than 71%, and still the largest thing by a wide margin. The plan
survived its own first check, which is not what happened to step 24's.

---

## 2. The plan's design was wrong, and the baseline said so before any code

The approved plan proposed a **control byte** per slot: a one-byte tag read before
anything else, so a rejection costs a sequential byte instead of two random accesses.
The new benchmark, run first, made that the wrong target:

| | hit | miss |
| --- | --: | --: |
| 64k keys | 59 ns | **34 ns** |
| 4M keys | 474 ns | **206 ns** |

**A miss already costs half what a hit does.** It stops at `slots[i] < 0`; a hit walks
the whole chain to the arena. A control byte speeds up rejections and adds an access
to hits — and at this table's load factor there is roughly one third of a rejection
per lookup. It optimises the cheap half.

That is the value of building the instrument before the change rather than after.

---

## 3. What was tried instead, and what it cost

`hashes []uint64` and `offs []int32` are **both indexed by id, both read on every
hit, and in separate arrays** — two dependent cache misses where the two values fit
in one line together. Merging them shortens the chain from
`slots → hashes → offs → arena` to `slots → entries → arena`.

This is emphatically not step 27's refuted change. That one merged the **slot**-indexed
arrays, so every EMPTY slot carried eight bytes it had no use for and 1.85 MB of index
became 4.20 MB. This grows per **occupied key** only — the property step 27's own
post-mortem identified as worth keeping.

Two versions were measured. The first stored the full 64-bit hash, which Go pads to a
16-byte entry against the 12 the two arrays cost, and it regressed small and
miss-heavy tables for exactly that reason. Truncating the stored hash to 32 bits makes
the entry **12 bytes — memory-neutral** — and is safe twice over: it picks the slot
(`hash & mask`, and a table would need four billion slots before 32 bits ran out) and
it filters a probe, where a false positive is 1 in 4 billion and `bytes.Equal` decides.

| ns/op, medians of 3 | 2 arrays | 16B entry | 12B entry |
| --- | --: | --: | --: |
| **Get, 4M keys** | 474 | 423 | **409** |
| Get, 64k keys | 59 | 63 | 62 |
| **GetMiss, 4M keys** | **206** | 219 | 220 |
| GetMiss, 64k keys | 34.2 | 34.4 | 34.8 |

**A trade, not a win.** Hits lose a dependent miss and gain 14%. Misses pay 7%,
because a rejection used to touch an 8-bytes-per-id array and now touches a
12-bytes-per-id one — the rejection working set grew by half even though the total
did not.

And end to end it is invisible. Three matched pairs on `opbench -only join -threads 8`:

```
before 205.1  after 175.9      after 14% FASTER
before 170.4  after 235.6      after 38% SLOWER
before 152.8  after 187.1      after 22% SLOWER
```

The sign flips between adjacent pairs. This is the ~30% noise floor that has shaped
every performance step since 23, and the join is now fast enough (150–235 ms) that it
is worse, not better: a shorter run is noisier relative to fixture and GC variance.

---

## 4. Why it was reverted rather than kept

The plan committed to this outcome **before the data existed**:

> If the end-to-end number does not move, the step's result is "it did not land",
> recorded with the mechanism, and the array comes back out rather than staying
> because it is clever.

The end-to-end number does not move. The microbenchmark is a shape-dependent trade —
better on hit-heavy probes, worse on miss-heavy ones — and a group-by build is
miss-heavy where a join probe is hit-heavy, so there is no single answer about which
half matters more. 14% on one and −7% on the other, invisible in wall clock, is not
enough to change a data structure six operators depend on.

Honouring a rule written before seeing the numbers is the whole point of writing it
then. Step 27 kept its revert for the same reason and the mechanism it recorded is
what made this step's design possible.

---

## 5. What shipped

`internal/kernel/keytable_bench_test.go`, and it is the durable part. Step 27 closed
with *"it needs measuring first, on a table that does not fit cache as well as one
that does"* — that benchmark did not exist, so nothing could act on the advice.

Five cases: hits and misses, at 64k keys (index around L2) and 4M keys (nowhere near
it), plus insert. Keys are 12 bytes, which is what `GroupKeyEncoder` emits for a
non-null int64 plus a little — the realistic middle. Probes multiply by a large odd
constant so successive lookups land far apart, rather than walking the table in
insertion order and measuring the prefetcher.

The numbers it produced are the finding: **a hit costs twice a miss, and the whole
cost is the dependent chain.** Anything aimed at rejections is aimed at the cheap
half. That closes off a design without spending another step on it.

---

## 6. What is still open

- **Inline keys for short values** — step 27's actual remaining suggestion, still
  untested. It removes `keyAt` and `memeqbody` (17% of the join between them) at the
  cost of a bigger entry, and the benchmark to judge it now exists. It is the one
  remaining idea with a plausible mechanism.
- **Step 39's debt is unpaid.** `n_unique`'s 1.35x stands. That was named as a
  possible outcome when the trade was taken, and it is the outcome.
- **`probeHash` and the arena layout**, untouched.
- The standing list: the parallel build side (`joinBuildSink.Merge`, implemented and
  still never called), Right/Full parallel probe, `quantile`/`median`'s per-group
  storage, `JoinWhere`, the allocation rate, re-running the suite, nested writing and
  `as_struct`, `.list` set operations, Map/Array, Pivot/Unpivot, SQL, cloud stores,
  join reordering.
