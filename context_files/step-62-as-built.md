# Step 62 — as built

**Duration aggregates accumulated in float64.** Step 61 named this as next in the
family it had just closed. It turned out to be three defects and one structural
asymmetry underneath them.

Six commits and this document.

Authoritative where it disagrees with the vision docs and [`design/`](./design/).

`make test-all` exit 0 (**100** package-ok lines = 20 packages × 5 SIMD configurations),
`make race` exit 0 (20), `make levels` and `go vet` clean in all three modules, PDS-H
SF=0.1 exit 0 with **22/22** — each asserted on its own exit code. PDS-H and h2o sum
only Int64 and Float64 columns and no golden plan mentions a Duration, so neither can
be affected by this step; that was checked rather than assumed.

---

## 1. Measured first, through the public API

| query | got | want |
| --- | --- | --- |
| one row of 2^53+1 ns: `sum` | **2501h59m59.254740992s** | …993s |
| the same row: `min`, `max`, `first` | …993s | …993s |
| one row of `MaxInt64`: `sum`, `mean` | **−2562047h47m16.854775808s** | +2562047h47m16.854775807s |
| `cum_sum` of one hour | **1331860h53m23.894837248s** | 1h |
| `[MaxInt64, MaxInt64]` summed | **−2562047h47m16.8s** | refuse |
| `[Max, Max, −Max, −Max]` summed | 0 | 0 — and it must stay 0 |
| `mean` of three `MaxInt64` | **negative** | `MaxInt64` |

The first row is the whole defect in one line: on a **one-row group**, `sum`
disagreed with `min` beside it in the same `Agg()` call. The widen to float64 happens
in `widenFloat` *before any addition*, so nothing was ever summed — the value was
simply rounded on the way in. 2^53 ns is 104 days, which any "total time per user"
query reaches.

`MaxInt64` is worse than imprecise: `float64(MaxInt64)` is exactly 2^63, and
`int64(2^63)` is out of range, so the result is **implementation-defined** — the
integer-indefinite value on amd64, saturation on arm64. The same query, the same data,
opposite signs on two machines. That is the construct `dispatch.go` documents as the
one it removed from FloorDiv.

And `sum` wrapped what **arithmetic already refused**: `Col("d").Add(Col("d"))` has
raised an error on this data since step 61.

## 2. `cum_sum` was not imprecision — it was type confusion

`winCumFloat` finishes by handing its `[]float64` to `data.NewFixed` under the
**output** type. A Duration output landed there, so float64 bit patterns were
published as a tick count and one hour read back as ≈152 years.

The structural reason it survived is the finding worth keeping: **`data.Values` checks
the exact physical type on the way OUT, and `data.NewFixed` checked nothing on the way
IN.** `Values`'s own doc explains why a width check is not enough — *"Int64 and
Float64 are both 64 bits"* — and that is exactly the hole: float64 and int64 being the
same width meant neither the declared type nor the row count could disagree. Had a
Duration been four bytes like a Date, `checkShape` would have caught it on the row
count.

`Expr.CumSum`'s doc promises *"the last row of `cum_sum(x)` equals `sum(x)`"* — the
property it violated.

## 3. The fix

- **`Acc: Int128, Out: Duration`** for both `sum` and `mean`. The gap between them is
  deliberate: the result must stay a Duration, so `Out` cannot widen; the total must
  stay exact, so `Acc` cannot narrow; and the narrowing that remains is where the
  refusal lives.
- **`sum` refuses**, naming the group, rather than wrapping — so the aggregate and the
  arithmetic answer the same way about the same values. Its op string is `"sum"`, which
  is load-bearing: it is how a caller tells this from an arithmetic refusal without
  matching on message text.
- **`mean` never refuses**, and that is a theorem: for a non-empty group
  `min ≤ mean ≤ max`, and truncating a value inside that interval leaves it inside, so
  the mean of int64 values is an int64. This is why `mean` carries the exact sum
  instead of borrowing `sum`'s refusal — its *sum* may leave int64 while the mean
  cannot.
- **`i128.DivUint64`** is the minimal addition that allows it: 128÷64, two `bits.Div64`
  calls, not the Knuth division the package doc refuses. The no-panic property is
  structural — the first call passes a literal `0` as its high word, the second a
  remainder — rather than an argument about the data.
- **Rounding truncates toward zero**, matching `Duration // Int64` and `.dt.Total*`.
  Flooring is right for an **instant**, because an epoch is arbitrary and flooring keeps
  the map monotone across it; a **span** has a true origin at zero, so the symmetry that
  matters is `mean(−x) == −mean(x)`. It is also what shipped, so this step changed
  precision rather than semantics — but it is now a test instead of a side effect of a
  Go conversion rule.
- **`cum_sum` narrows per row**, which is not a choice: a cumulative publishes every
  prefix, so a running total that transiently leaves int64 has no value to emit even if
  it comes back. That makes `cum_sum` deliberately **stricter than `sum`** on the same
  column, and a hint says so.

**One sequencing constraint dominated the step:** the binding and the accumulator had
to ship together. Flipping `Acc` alone makes `Finish` hand a 16-byte-per-element slice
to a column declaring 8 — which nothing checked — and changing the accumulator alone is
inert for `mean`, which ignored `Acc` entirely.

Fixed for free by the shared accumulator: hash group-by, the parallel aggregate,
spilling replay, `.Over()` windows, `GroupByDynamic`/`Rolling`, and `list.sum` over a
`List(Duration)`. The spill format carries input rows rather than accumulator state, so
there was no format change and no version bump.

## 4. `data.NewFixed` now checks its values against its type

The asymmetry in §2, closed: a panic in the `NewBool` shape, whose justification
transfers verbatim — the signature has no error to return, and values of the wrong type
are a bug in the caller rather than a condition. The intended puns still pass, because
`Physical()` is what it compares: Date/int32, the temporal types/int64, Enum/uint32,
Decimal/i128.

It fired on exactly **one** site in the repository, and that site was a test fixture
building an `Int32` column out of `[]int64` as a convenient way to make a "wrong type"
column for `NewBatchRows` to reject. The column it built was itself structurally
invalid — the very class this check exists to prevent — so the fixture was corrected
and the case still tests what it always tested.

## 5. The instruments

**The merge harness had three float-ish families.** `aggFamilies` held Float64, String
and Bool, and `columnsEqual` allows 1e-12 relative for floats, so **`Merge` had never
been checked against an exact reference for any accumulator**. Seven exact families
joined it, with values around 2^53. That alone caught the parallel divergence: merging
2, 3 and 5 partitions gave 9007199254740998, …41000 and …41002 where a single pass gave
…40992. Since `aggWorkers` parallelises `sum` by default, the thread count was changing
the answer.

**The new sweep asks the smallest question an aggregate can be asked.** A group of one
row, over every `AggOp` × every operand type, reusing step 61's `allOperandTypes` so
`TestOperandTypesCoverTheEnum` keeps it exhaustive. Three properties per case:

1. an exact aggregate returns its input;
2. `min`, `max`, `first`, `last` and `sum` **agree with each other** — five engine
   answers through four accumulators, so a fixture I misunderstood cannot make it pass;
3. the all-null group is null.

Every declared `AggOp` must land in exactly one behaviour class or the sweep fails.
111 identity arms, 189 null-group assertions, 24 cross-checks.

Two ratchets held the evidence — eight inexact aggregate arms, four cumulative type
confusions — populated red in the first commit and emptied by the fixes.

**One assertion of mine was wrong and the sweep said so:** `implode` over an all-null
group returns a *list containing null*, not a null, which is correct — the same
distinction `NewList`'s doc draws between an empty list and a null list. The class
exists in the classifier for that reason.

## 6. Teeth

| reintroduce | result |
| --- | --- |
| revert the `sum` binding | **bites** — 20 arms of the sweep, the merge families, and the public tests |
| drop the `Int64()` narrowing check | **bites** — the refusal tests, and `sum` stops agreeing with `+` |
| put `mean` back on the float path | **bites** — all three instruments |
| **floor the mean instead of truncating** | **bites** — the rounding is asserted, not incidental |
| remove the negative case from that fixture | the tooth goes silent, so the test asserts its own mean is negative |
| one `bits.Div64` instead of two | **panics** |
| read the magnitude signed / drop the sign restoration | **bite** — `−1/1 = 1`, and every negative quotient flips |
| revert the `cum_sum` routing gate | **bites** — four arms, one hour as 152 years |
| narrow `cum_sum` only at the last row | **bites** |
| drop `fn == WinCumSum` from the gate | **did not bite** — nothing exact binds Product today, so it is a latent trap closed rather than a live defect |

## 7. Still open

- **`asof`'s tolerance** does `left − right`, `−d` and `d*npt` unchecked on one line, so
  a candidate three centuries away can test as within a one-hour tolerance. The last
  unchecked temporal arithmetic I know of.
- `mean(Datetime)` is refused; the mean of instants is meaningful and Polars supports
  it. A gap, not a defect.
- `list.mean` hard-codes Float64 while `listReduce` derives the type, which
  `checkShape` catches loudly. When it is fixed by delegating as `list.sum` does,
  `list.mean` over a `List(Duration)` becomes exact for free.
- A streaming consumer can receive rows and *then* a refusal from a spilled partition
  replayed lazily. True of every error raised in `Next`, not new here.
- Carried: the contract fixture's missing List/Struct columns (32 predicted
  divergences, sized); the byte-flip spill sweep; `Optimizer.Verify` off in `Explain`;
  six planners leaking an opened operator; `spill.Writer.Write`'s bare error; UDF name
  uniqueness; `callCache` eviction; `Pivot`; the stale benchmark suite.
