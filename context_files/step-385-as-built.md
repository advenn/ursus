# Step 385 — as built

**A large top-k sorts every row.** T1 of `v0.7-scope.md`.

## 1. How

A `Head(k)` after a `Sort` becomes a top-k (limit pushdown), and the sort sink found
its rows with `ArgTopK`, a heap of k, whatever k was. The heap reads each row once,
and each row that beats its root, about k ln(n/k) of them in no order, costs log₂ k
comparator calls to place: at a k of a million of ten million rows, 4.4 s where
Polars takes 0.4.

- **`sortBeatsHeap`** (`physical/sort.go`): where k log₂ k ln(n/k) is past a third
  of n, the sink sorts every row, the radix sort `ArgSortColumnsN` on its threads,
  and keeps the first k. `ArgTopK` is held to equal `ArgSort[:k]`, so the two give
  the same rows in the same order, ties in the input's.
- The crossing was measured on ten million floats and on a million, on four
  threads: there the heap took 196 ms for 10,000 rows and 282–303 for 30,000, the
  sort 330 to 420 whatever k, crossing near 38,000; at a million rows they cross
  near 10,000, where the rule turns at 5,000, a few milliseconds early.

## 2. Measured

§4's T1 probe (step 382), ten million floats, `Sort(v).Head(k)`, four threads, this
step's build and one whose rule never sorts, as step 384 did, seven runs each at a
load of 2.3 to 4.2; Polars' from step 382, at 1.5 to 4.4:

| k | the heap alone | here | Polars |
| --- | --- | --- | --- |
| 10,000 | 136.1–151.5 ms | 135.7–138.4, the heap | 215.1–248.1 |
| 30,000 | 210.7–228.1 | 207.8–209.8, the heap | |
| 100,000 | 479.6–491.6 | 267.2–281.0 | |
| 300,000 | 1,277.4–1,304.6 | 292.2–301.9 | |
| 1,000,000 | 3,993.6–4,201.6 | 326.8–357.0 | 382.1–399.3 |

Of a million rows, at a load of 4.1 to 5.8, the heap took 14.1 ms for 1,000, 21.7
for 5,000, 33.5 for 10,000, 79.8 for 30,000 and 197 for 100,000; the sort 33 to 38.

No suite query sorts to more than a few thousand rows.

## 3. Tests

**`TestALargeTopKIsTheSortsFirstRows`** (root): 60,000 rows of a float with ties,
NaN, −0 and nulls, an integer, text, and a unique id; four orders, a float ascending,
one descending with nulls last, an integer and text descending, text and a float
nulls last; k of 1, 30 and 300, a heap's, and of 1,000 to past n, a sort's; on one
thread and four in batches of 997, and k of 30,000 spilled under 256 KiB: each
answer's ids are the full sort's first k, in its order, and one answer's rows whole.

**Teeth: 2 of 2 bite:** a large k's last rows kept; a large k one row short. Six
top-k tests of earlier steps bite beside the new one.

**Gate:** the light gate, one package at a time on four procs: the tests at v3, 28
packages ok; levels, vet ×3 and the bench engine tests clean; the 22 PDS-H answers
match DuckDB. `./internal/...` and the root package ran whole before it.
