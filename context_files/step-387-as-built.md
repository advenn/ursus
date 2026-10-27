# Step 387 — as built

**A sink's windows computed at once, and a rolling sum's passes trimmed.** W2 of
`v0.7-scope.md`.

## 1. How

An expression's rolling, `Col("x").Sum().Rolling(t)`, already slid its state along
the rows, adding the row that enters a window and taking out the one that leaves
(steps 337 and 172); what was slow was around it.

- **The windows at once** (`physical/window.go`, `windowSink.Finish`): a sink's
  windows were computed one after another; each reads the rows and its partition's
  ids and writes a column of its own, so they run at once, as many as the query's
  threads. A rolling mean, max and std over a million rows took the sum of the three.
- **An Int64's rolling sum** (`kernel/rolling.go`, `rollingIntSum`): read where its
  values lie, as step 386's running sum is, and a column of no nulls not asked for
  each row's.
- **An index already in order** (`kernel/rollingby.go`, `inOrderBy`): where every
  row is present and each partition's index is already in order, as an expression's
  rolling by a row index is, the partitions' rows are the window's own; they were
  copied into a filtered order first. A window's closedness is read once, not at each
  step of its two edges.

## 2. Measured

§4's W2 probe (step 382), a million rows indexed in order, windows of 100, four
threads, step 386's build and this step's alternating, seven runs each at a load of
1.2 to 1.5; Polars 1.44.1 at the same load:

| | step 386 | here | Polars |
| --- | --- | --- | --- |
| `Sum().Rolling(t)` | 27.5–31.4 ms | 22.0–25.0, a median once 32.6 | 10.2–10.5 |
| a rolling mean, max and std | 107.9–161.2 | 55.8–83.3 | 23.2–26.3 |

The three windows now take about the time of the slowest of them: 2.2 to 3.2 times
Polars' where they were 5. The sum is a single pass of the kernel, a row's
add and a row's take-out at 128 bits, about 20 ns a row; Polars sums in Int64.

No suite query computes a window: none of the 37 plans of PDS-H and h2o holds one,
and they are step 382's.

## 3. Tests

**`TestASinksWindowsAnswerTogetherAsAlone`** (root): 3,000 rows; ten windows in one
`WithColumns`, a rolling sum, mean, max and std by an index in order, a sum by one
out of order with nulls and a mean by one out of order without, a cumulative sum, a
rank, a shift and a sum over two partitionings; computed together on four threads
in batches of 211, each the same as the window computed alone on one. A Duration's
running sum past its range beside them fails the query. It and step 386's test
passed under the race detector.

**Teeth: 5 of 5 bite:** a window's answer put in the next one's place; a window's
failure dropped; an index out of order taken as in order; rows of a null index kept;
an Int64's null rows summed in a rolling window. The kernels' teeth bite in the
rolling tests of earlier steps, which compare against hand-written answers and
Polars'; the new test compares a window with itself.

**Gate:** the light gate, one package at a time on four procs: the tests at v3, 28
packages ok; levels, vet ×3 and the bench engine tests clean; the 22 PDS-H answers
match DuckDB. `./internal/...` and the root package ran whole before it.
