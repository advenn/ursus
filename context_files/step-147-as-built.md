# Step 147 — as built

**The second half of item 8 of `v0.5-scope.md`: the everyday expressions composed
from what exists.** `Cut`, `QCut`, `QCutN`, `ValueCounts` and `Describe`.

## 1. Evidence first

The scope planned `Cut` as a `When` chain and `QCut` as `Cut` over quantiles.
Reading the pieces showed three reasons the chain would be the wrong shape.

**A `When` chain is named after its first `Then`.** That would be a label literal,
so every `Cut` would come out named `"literal"`; Polars names it after its column.

**A chain re-reads the column at every break,** and casts it once per break: k+1
passes for k breaks.

**`QCut`'s breaks are quantiles of the column, known only when it runs.**
`Quantile(q).Over()` gives them as columns. A `Call`'s parameters must be literals,
so no ordinary call could take them, and the labels Polars builds from them would
have to be built at run time too.

**Step 146's horizontal family answers all three.** Its operands may be columns, it
is named after its first operand, and it reads each operand once. One kernel serves
both: `Cut`'s breaks are literal operands, and `QCut`'s are window operands.

## 2. What changed

### `cut` and `cut_left_closed` (`internal/expr`, `internal/kernel/horizontal.go`)

**Two horizontal calls,** one per closed side, so the side is the function rather
than a flag operand.

**Their operands:** the value, then its breaks (numbers), then none or one label more
than the breaks (strings). `CutBreaks` finds the boundary by type, which is all the
typing rule and the kernel need.

**The value must be a number,** refused otherwise while the query is planned. It is
compared as a Float64, as Polars compares it.

**The kernel:**

- **Each row's bin** is a binary search over that row's breaks. Breaks are read per
  row because `QCut`'s are columns.
- **Repeated breaks are allowed.** They must only not decrease, so two equal
  quantiles leave an empty bin and never misplace a value.
- **A null or NaN value, or a row whose breaks are not all there, is null.** A row
  lacks breaks when they are the quantiles of a column of nulls.
- **The interval labels** are `(lo, hi]`, or `[lo, hi)` closed on the left, from
  `-inf` to `inf`. Each bound is formatted as a cast of a Float64 to String formats
  it. They are built again only when a row's breaks differ from the last, which
  for a literal or a quantile is once.
- **Every operand goes through `readable`,** the kernel package's rule for an
  operand: a payload-free column of nulls is given one to read. `bitCount` from
  step 146 now does the same, in place of its own shortcut.

### The public API (`cut.go`)

- **`Cut(breaks, opts...)`:** the breaks must be finite and increase.
- **`QCut(quantiles, opts...)`:** each break is `Quantile(q, InterpLinear).Over()`,
  so it is a window and belongs in `Select` or `WithColumns`. The quantiles must
  increase.
- **`QCutN(n, opts...)`** splits into n bins of equal count.
- **The options:** `CutLabels(...)`, one label more than there are breaks, and
  `CutLeftClosed()`.
- **The answer is a String.** Polars' is a Categorical, but an Enum cannot yet be
  built from the public API.

### `ValueCounts(by...)` (`describe.go`)

It is `GroupBy(by...).MaintainOrder()`, then `Len` as `"count"`, then a stable `Sort`
by count, descending:

- ties keep the order in which their values first appear;
- a null is a value, as in a group-by;
- an eager mirror is on `DataFrame`.

### `Describe(ctx, percentiles...)`

**Eager.** It reads the schema, runs one `GroupBy().Agg` of every statistic of every
column, then reshapes the one row in Go.

**The statistics:** count, null_count, mean, std (ddof 1), min, the percentiles
(default 25%, 50% and 75%, each the nearest value, as Polars' describe takes them),
and max.

**The column types:**

- **A number** (Decimal included) is a Float64 column.
- **A Bool** is a Float64 column too, true as 1:
  - its mean is the share of trues;
  - its min and max say whether a false and a true occur;
  - it has no std or percentiles.
- **Anything else** is a String column: its counts, and for a String or a temporal
  column its min and max, formatted as their cast to String formats them.

**Refused:** a frame with a column named `statistic`, and a percentile outside
[0, 1]. The percentiles are sorted, with repeats removed.

## 3. Tests

**`everyday_test.go`:**

- **`TestCut`:**
  - both closed sides, with labels and without;
  - NaN and the infinities;
  - no breaks at all;
  - expansion over `Col("age", "x")`;
  - four refusals.
- **`TestQCut`:**
  - the median and the quartiles of 1 to 8, worked by hand (2.75, 4.5 and 6.25);
  - labels;
  - equal quantiles leaving an empty bin;
  - a column of nulls, a typed null literal, and an aggregate over a group of nulls;
  - 10,000 shuffled values over batches of 512, each half holding exactly 5,000,
    which is what proves the quantiles are the whole frame's;
  - three refusals.
- **`TestValueCounts`:**
  - the order;
  - ties in first appearance, among 2,000 keys over batches of 64, which a
    group-by folded in parallel does not keep by itself;
  - two keys, and the eager mirror;
  - refusals: no keys, and a key named `count`.
- **`TestDescribe`:** an Int64, a String, a Bool and a Decimal against hand values,
  other percentiles, and both refusals.

**Elsewhere:**

- **The evaluator contract** drives `cut` with no breaks, with breaks, and labelled,
  and `cut_left_closed`. It now types a float literal.
- **`TestCallFnFamiliesDoNotOverlap`:** 85 classified functions.

## 4. Teeth

| tooth | result |
| --- | --- |
| a right-closed bin opens on the right | **bites:** `TestCut`, `TestQCut` |
| a left-closed bin opens on the left | **bites:** `TestCut` |
| a NaN value is binned | **bites:** `TestCut` |
| labels ignored | **bites:** `TestCut`, `TestQCut` |
| `QCut` interpolates to the nearest | **bites:** `TestQCut`, the batches case among them |
| `ValueCounts` does not keep first appearance | **bites:** the 2,000-key case |
| `ValueCounts` least frequent first | **bites:** `TestValueCounts` |
| `Describe`'s std is the population's | **bites:** `TestDescribe` |
| `Describe`'s percentiles interpolate | **bites:** `TestDescribe` |
| `Describe` treats a Bool as text | **bites:** `TestDescribe` |
| `Describe` keeps the percentiles' order as given | **bites:** `TestDescribe` |
| `cut` reads a payload-free operand | silent |
| a bit count reads a payload-free operand | silent |

**The first `MaintainOrder` tooth was silent** on seven rows: there, one partition
already emits groups in first-appearance order. The 2,000-key case is what sees it.

**The last two are defensive, and stay.** No public query found here hands these
kernels a payload-free operand:

- an aggregate over a group of nulls is assembled with a payload;
- so is a typed null literal.

`readable` is still the kernel package's rule for every operand
(`take.go`'s `NullColumn`), and the cost is one check per operand.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. PDS-H uses none of these.
