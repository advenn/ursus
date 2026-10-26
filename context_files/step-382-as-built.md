# Step 382 — as built

**A filter of keys below a group-by or a window.** O1 of `v0.7-scope.md`, and §4's
items measured first, as the scope asks, to order them.

## 1. §4 measured

One program of ursus at step 381's code and Polars 1.44.1's script over the same
inputs, held in memory, on four threads; each seven runs, min and median, ursus and
Polars an item at a time, at a load of 1.5 to 4.4 from other work:

| | input | ursus | Polars | |
| --- | --- | --- | --- | --- |
| **O1** a group-by filtered on its key, `k < 10` | 10M rows, 1,000 keys | 41.8–43.4 ms | 7.5–7.7 | 5.6 |
| **O1** a window filtered on its partition key | the same | 149.6–167.3 | 7.8–9.6 | 17 |
| **U1** `Upsample` by a second, 100 groups | 1M rows, 1 or 2 s apart | 305.7–311.6 | 21.0–24.5 | 12.7 |
| **T1** `Sort(v).Head(1,000,000)` | 10M floats | 4,270–4,370 | 382–399 | 11 |
| **T1** `Head(10)`, `Head(10,000)` | the same | 97.3–98.8, 134.9–140.9 | 214.7–247.3, 215.1–248.1 | 0.40, 0.57 |
| **W1** `Agg(Col("x").CumSum())` by 1,000 groups | 1M Int64s | 62.0–67.2 | 10.6–12.0 | 5.6 |
| **W1** `CumSum().Over(g)` | the same | 35.8–38.5 | 15.1–17.2 | 2.2 |
| **W2** `Sum().Rolling(t)` of 100 rows | 1M rows | 28.2–30.8 | 9.9–10.2 | 3.0 |
| **O2** a mean and a window, each written twice | 1M rows | 40.6–51.1 | 19.1–23.3 | 2.2 |
| **C2** a CSV read | 1M rows of an integer, a float, text and an instant | 116.4–148.8 | 63.9–68.6 | 2.2 |
| **C1** a CSV write | the same | 413.1–435.9 | 208.9–236.9 | 1.8 |
| **Q1** PDS-H q11, q15, q22 at SF=1 | | 25.5, 129.4, 76.2 | 32.3, 60.6, 110.0 | 0.79, 2.13, 0.69 |

So the order is O1, U1, T1, W1, W2, then O2, C2 and C1. Q1 is not a gap: q11 and q22
are faster than Polars', and q15's time is its read of lineitem, zstd 42% of its CPU
and the decode 21%, its scalar a cached subplan's one row. T1 is a gap only past a k
of some thousands: a heap of ten thousand is faster than Polars' top-k.

## 2. How

`predicatePushdown` held every conjunct above a group-by and a window, a barrier
whose comment named the exception it did not take: a predicate of a group's keys,
or of a window's partition keys, keeps or drops each group whole.

- **`pushThroughAggregate`** (`plan/rule_predicate.go`): a conjunct that reads the
  keys alone moves below, rewritten through each key's definition, as through a
  Project's: `GroupBy(Col("k").Mod(7).Alias("m")).Filter(m < 3)` filters `k % 7 < 3`
  beneath. A name no key defines is an aggregate's, HAVING, even one named as an
  input column, `Agg(Col("v").Sum())` filtered on `v`, and stays. So does a conjunct
  of a key holding a float, whose group merges −0 with +0 and keeps its first row's,
  so a predicate telling them apart is not constant on it, as `Distinct`'s O9 has it;
  one of no column, or of a group-by of no keys; one holding an aggregate, a window
  or a UDF; and a fallible one behind a conjunct that stays, as `holdFallible` keeps
  it everywhere.
- **`pushThroughWindow`:** a Window appends its windows to its input's rows, so a
  conjunct of input columns is itself below it. It moves where each column it reads
  is a plain partition key of every window in the node, of no float, each window
  mapping its partitions to their rows: then two rows of a partition agree on it.
  A column a partition is computed from, `Over(k % 7)` filtered on `k`, a key of one
  window of two, and any key beside the frame's window, `Over()`, keep it above.

## 3. Measured

The O1 probe, step 381's build and this step's alternating, two rounds of seven at a
load of 7.6:

| | step 381 | here | Polars |
| --- | --- | --- | --- |
| the group-by filtered on its key | 49.5–54.6 ms | 11.1–12.5 | 7.5–7.7 |
| the window filtered on its partition key | 171.9–380.6 | 13.7–15.2 | 7.8–9.6 |

Polars' times are §1's. Both now read about one and a half times Polars', the filter
over ten million rows before a group-by of a hundred thousand.

**No suite query changes:** the 22 PDS-H plans and the 15 h2o plans are the same
before and after, by `Explain`, none filtering a group-by's keys after it. PDS-H at
SF=0.1, all 22 alternating, read 1.02 in all at a load of 3.9 to 4.4, q1 1.38, and
q1 alone over five rounds 1.02.

## 4. Tests

**`TestAFilterOfKeysMovesBelowAGroupByOrAWindow`** (root): 22 queries over 3,000
rows of an integer key with nulls, a text key with nulls and some rows that are no
number, a float key of −0, +0 and NaN, a second integer key and values. A group-by
filtered on a key, its null, keys defined by expressions, in first-appearance order;
on an aggregate, one named as an input column, a key and an aggregate as two
conjuncts and as one, a float key telling −0 from +0, no key, no column, and a cast
of text behind a HAVING that removes the rows it cannot parse; a window filtered on
its partition key, one of two, ordered within its partition, a key of every window,
of one window of two, a column its partition is computed from, beside the frame's
window, the frame's window alone, a column no window partitions by, a float key.
Each plan's filter is below the node or above it as it should be, and each answer
is the one with predicate pushdown off, on one thread and four.

**Teeth: 8 of 8 bite:** a float key's conjunct moved below; an aggregate's output
read as the input's column; a key's name moved below unsubstituted; a cast moved
ahead of the HAVING before it; a partition computed from a column taken as that
column; a float partition key's conjunct moved below; the keys of any window, not of
every one; the frame's window passed over.

**Gate:** the light gate, one package at a time on four procs: the tests at v3, 28
packages ok; levels, vet ×3 and the bench engine tests clean; the 22 PDS-H answers
match DuckDB.
