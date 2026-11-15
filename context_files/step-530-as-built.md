# Step 530 — as built

**A float aggregate that is NaN answers one canonical NaN.** Over NaNs of several
payloads, Sum, Mean, Var, Std, Product and the rest answered NaN at every thread count,
but which NaN came out followed the order of their additions (step 523, "listed, not
fixed"). The maintainer decided on 2026-11-15 that such an answer is one NaN, whatever
the payloads of its inputs and the order of its additions, and approved the change it
makes to a serial run's NaN bits. Every float aggregate by arithmetic now answers
`math.NaN()`'s bits, 0x7FF8000000000001, and a Float32 one `float32(math.NaN())`'s,
0x7FC00000. On the way, element-wise arithmetic with a NaN literal was found to answer
other bits in a batch of one row than in a batch of many, and is fixed.

## 1. The finding

x86 hands on the payload of the first NaN operand of an addition or a product, quieted.
So a sum over NaNs of several payloads answers the payload of whichever NaN its
additions met first, and a parallel group-by, which folds its workers' partial states
in worker order, meets them in another order than one thread does. A probe on master
(73f9863), a frame of NaNs of five payloads, both signs, quiet and signalling, the same
query in each configuration:

| aggregate | threads 1 (any batch) | threads 4, batch 1 | threads 4, batch 7 | threads 8, batch 3 |
| --- | --- | --- | --- | --- |
| keyed Sum, group 0 | fff8000000000000 | 7ffc000000000003 | fffc000000000005 | 7ffc000000000003 |
| keyed Var, group 0 | 7ffc000000000003 | fff8000000000000 | 7ffc000000000003 | 7ffc000000000003 |
| keyless Var | 7ff8deadbeef0001 | 7ffc000000000003 | 7ffc000000000003 | 7ffc000000000003 |
| keyless Corr | 7ff8deadbeef0001 | 7ff8deadbeef0001 | fff8000000000000 | fff8000000000000 |

A NaN made by the arithmetic itself, +Inf plus −Inf or 0/0, is x86's default NaN,
0xFFF8000000000000, its sign set; a NaN ursus makes itself is `math.NaN()`.

## 2. The canonical NaN

What there is to choose between:

- **Go and ursus.** `math.NaN()` is 0x7FF8000000000001. ursus makes it wherever it
  makes a NaN: a rolling sum, mean, var, std, skew or kurtosis whose window holds a NaN
  (`rollingState.nonFinite`, `momentAnswer`), a correlation of fewer than two pairs
  (`ranker.spearman`, `pairspill.go`), a frame's `Corr` of a column holding a null,
  and "NaN" read from text, by `strconv` and `dtype/numtext.go`. `float32(math.NaN())`
  is 0x7FC00000: the narrowing keeps the top 23 bits of the payload.
- **Polars 2.1.0** (`polars-latest`, bits by `struct.pack`): it has no one NaN for
  arithmetic. `pl.Series([float('nan')]).sum()` is 0x7FF8000000000000, Python's own NaN
  handed on. Over five payloads its `sum`, `mean` and `product` hand on the first NaN
  operand's, quieted, so they follow the order of the values: 7ff8000000000001 in one
  order, fff8000000000002, 7ff8dead00000000 and fffc000000000000 in three others; its
  `std` and `var` answer fffc000000000000, its `cum_sum` each prefix's first NaN, its
  `ewm_mean` the first. Where it makes a NaN itself, `max` and `min` of NaNs and a
  `rolling_sum` over one, it is Rust's `f64::NAN`, 0x7FF8000000000000. A Float32 `sum`
  hands on the first operand's, 0x7FC00001 of ours, and of `[nan]` is 0x7FC00000.

**The choice is `math.NaN()`,** 0x7FF8000000000001, and **0x7FC00000** in a Float32:

1. It is the NaN ursus makes everywhere already, so every NaN an aggregate answers,
   made or computed, is the same bits; the rolling kernels needed no change for a NaN
   input, and a column of NaNs read from a CSV file sums to the same bits it did.
2. A Go caller compares with `math.NaN()`.
3. Polars offers nothing stable to agree with: its arithmetic answers follow the
   order of the values, as ursus's did, and its own NaN differs from Go's by the last
   payload bit. ursus is not a Polars clone (feature-gaps §9 now says so of this).
4. 0x7FC00000 is `float32(math.NaN())`, the quiet NaN of IEEE 754, Rust's `f32::NAN`,
   and the NaN a Float32 column parsed from "NaN" holds. A Float32 answer's NaN is the
   canonical one of its own type: widened, it is 0x7FF8000000000000, not `math.NaN()`.

## 3. Where it is applied

`internal/kernel/aggnan.go` holds `nan64` and `nan32` and three helpers: `oneNaN`
makes each NaN of a []float64 the canonical one in place, `narrowOneNaN` narrows to
Float32 with each NaN `nan32`, and `aggFloatsAs` is `floatsAs` for an answer by
arithmetic. Each is applied where the answer leaves its kernel, on the output and never
on the state, which a NaN keeps NaN anyway:

| answer | where | reached by |
| --- | --- | --- |
| Sum, Mean | `sumAcc.Finish`, `meanAcc.Finish` (aggsum.go) | group-by, keyless, `Over`, the list namespace's `Sum` and `Mean` (`listReduce`), `SumHorizontal`, `MeanHorizontal` (`horizontalReduce`), `Dot` and `Arr().Dot()`, `GroupByDynamic` and `Rolling` groups, a spilled group-by, `Describe` |
| Var, Std | `varAcc.Finish` (aggstat.go) | as above, and the list's `Var` and `Std` |
| Product | `productAcc.Finish` (aggstat.go) | as above |
| Skew, Kurtosis | `momentAcc.Finish` (aggmoment.go) | as above |
| Corr, Cov | `coMomentAcc.Finish` (aggpair.go) | as above |
| CorrSpearman, Entropy | `spearmanAcc.Finish` (both arms, the spilled too), `entropyAcc.Finish` (aggrank.go) | as above |
| cum_sum, cum_prod | `winCumFloat` (window.go), each running total as it is reached, not a pass over the answer (§8) | in memory and streamed (`carried` calls `WinCall`) |
| rolling sum, mean, var, std | `rollingFloat` (rolling.go) | in memory, centred, and by time in memory (`rollingBy` runs `rollingOver`) |
| rolling skew, kurtosis, cov, corr, and a streamed rolling window | `floatsOut` (rollingmore.go) | `rollingMoment`, `rollingCovCorr`, `RollingNext`; `rollingRank`'s ranks, which are never NaN |
| rolling sum, mean, var, std by time, streamed | `RollingByNext`'s `rbFloat` arm (rollingbynext.go) | the sorted pass; its quantile arm is a pick and keeps `floatsAs` |
| ewm mean, std, var, sum, and by time | `winEwm`, `EwmNext` (ewm.go), `winEwmBy` (ewmby.go) | in memory and streamed |
| a frame's `Corr` | `DataFrame.Corr` (frameconv.go) | eager |

Left as they are, by the brief: the picks, Min, Max, NanMin, NanMax, First, Last,
Median, Quantile (its interpolation of two picks too, deterministic since step 523),
TopK and the like, and `RollingMin`, `RollingMax`, `RollingMedian`, `RollingQuantile`,
`CumMin` and `CumMax`. `Interpolate` is a fill, its own rows' bits kept.
`CumSumHorizontal` is a fold of `Add`, element-wise.

## 4. Element-wise arithmetic

A NaN's payload flows through a row's arithmetic: of two NaNs the answer is one
operand's, which IEEE leaves to the machine. A probe over batch sizes {1, 3, 7, 1024}
and the optimizer on and off found one place where that was order-dependent:

```
DIFF opt true batch 3 col L+y row 1: fff8000000000000 vs ref 7ff8deadbeef0001
DIFF opt true batch 3 col L*y row 1: fff8000000000000 vs ref 7ff8deadbeef0001
DIFF opt true batch 3 col L32+f row 0: ffc00000 vs ref 7fc0beef
```

`Lit(NaN).Add(Col(y))` and `.Mul` over a row whose y is a NaN of another payload
answered the literal's NaN in a batch of one row and y's in a batch of many: `byConst`
(step 400) adds and multiplies a constant on the left as the right operand, `a[i] + s`,
and a batch of one row has no constant side (`constSide` declines n == 1), so it takes
the two-column kernel, left first. **`constSide` now declines a NaN constant**, which
is broadcast as in a batch of one: the same kernel in every batch, at the cost of a
copy of the constant only when it is a NaN. A constant that is not a NaN cannot tell
the two orders apart: of one NaN operand the answer is that NaN, quieted.

Checked and the same in every batch and with the optimizer off: two columns added,
subtracted, multiplied and divided (the left operand's payload, in the SIMD and the
scalar kernels alike); a literal on the right; a subtraction or division by a literal
on either side, which keep their order; `Lit + Lit`, folded through the kernel. These
hand the payload on per row and stay as they are.

**Under Go's emulation of vectors, `GODEBUG=simd=0`, they do not.** CI's `test (0)`
failed the literal test on the first push: Go's emulated two-lane `Float64s.Add`
hands its first lane the right operand's NaN and its second the left's (a probe of
`addF64SIMD` over rows of NaN A plus NaN B: `fffc…05 7ff8…01 fffc…05 …` emulated,
`7ff8…01` in every row natively), so a row's NaN follows its place in its batch, two
columns' as much as a literal's. That is the toolchain's emulation, a test setting,
not ursus's arithmetic: the native widths (128, 256, 512) and the scalar build hand on
the left operand's in every row. The test compares a NaN as a NaN where the vectors
are emulated (`!kernel.Accelerated() && kernel.VectorBits() > 0`), and to the bit
everywhere else.

## 5. The tests and their oracles

`canonicalnan_test.go`, over frames generated from a seed (`genNaNs`): a key `g` whose
class, g mod 4, decides what its rows draw: numbers only; numbers and a NaN of the
group's own payload; numbers and NaNs of six payloads, both signs, quiet and
signalling; numbers and both infinities. `a`, `b` (the next class) and `f` (Float32)
have nulls; `c` is numbers only; `h` is numbers two of which overflow a sum, so a
rolling window of numbers alone makes a NaN; `z` is zeros, which do not vary, so a
correlation with it is 0/0; `i` is a row index. The numbers are halves, so a sum of
them is exact in any order. Nothing expected is typed:

- **Go oracle.** `theOneNaN`: every NaN cell of every answer is `math.NaN()`'s bits,
  or `float32(math.NaN())`'s.
- **A recorded run of the old code.** `testdata/canonicalnan/{aggregates,windows,corr}.json`
  were written on master (73f9863) by `URSUS_CANONICAL_NAN=write`, a NaN of any payload
  recorded as N: every answer must hold the same bits, and a NaN where one was.
  Recorded again on this branch, the three files are byte-identical to master's: no
  answer that was not a NaN moved, at any configuration. Master's recording is the same
  at `GOMAXPROCS` 1, 4 and 8, `GODEBUG=simd=0` and `simd=128`, and with the SIMD
  experiment off, so CI's legs read it as written. At `GOAMD64=v1`, where no a*b+c
  fuses, a skew's, a kurtosis's, a correlation's and two ewm columns' last bits differ
  (60 aggregate cases, 2 window columns), so the bits are held under the build tag
  `amd64.v3`, which the Makefile and CI set (`recordedBits`), and elsewhere each
  case's NaNs and nulls only.
- **Differentials.** The windows in every configuration against one thread in batches
  of 8192, and streamed past a memory limit against in memory.

| test | what | configurations |
| --- | --- | --- |
| `TestAFloatAggregateThatIsNaNIsTheOneNaN` | Sum, Mean, Var, Std, Skew, Kurtosis, Product, Entropy and the list's Sum, Mean and Std of `a`, `f` and `c`; Sum, Mean and Var of `h`; Corr, Cov, CorrSpearman and Dot of pairs, with `z` among them: 43 aggregates, keyed (8 groups) and keyless | threads {1, 4, 8} × batches {1, 3, 7, 1024}; the recorded run in each |
| `TestAWindowOfFloatsThatIsNaNIsTheOneNaN` | cum_sum (both ways), cum_prod, rolling sum, mean, var, std (centred), skew, kurtosis, cov and corr, rolling sum and var by the index, ewm mean, std, var, sum and mean by the index, in a partition and over the frame; `Sum` and `Var` over a key; `SumHorizontal`, `MeanHorizontal` | the same, against the reference and the recorded run |
| `TestAStreamedWindowOfFloatsThatIsNaNIsTheOneNaN` | the streamable ones over 6,000 rows: cumulative (carried), rolling of rows (`RollingNext`), by the index (`RollingByNext`), ewm (`EwmNext`) | batches of 64 under 16 KiB, spill asserted, against in memory |
| `TestAFloatCorrelationMatrixThatIsNaNIsTheOneNaN` | a frame's `Corr`: NaN columns, a steady one, two that vary | the recorded run |
| `TestANaNLiteralInArithmeticIsTheSameInEveryBatch` | a NaN literal of each of six payloads added, subtracted, multiplied and divided on either side of a column of NaNs, Float64 and Float32 | threads × batches, the optimizer on and off, against the reference |
| `TestAFloatSumOfZerosIsTheSameAtEveryThreadCount` (step 523's) | its NaN column now compared by bits, where it forgave a NaN's payload | 30 configurations |
| `TestAConstantIsReadOnce` (step 400's, kernel) | its column holds NaNs of three more payloads and a constant of a fourth; a NaN constant is not held to the allocation count | the constant against its broadcast |

On master (73f9863), with these tests and its own recording, six root tests fail, 895
lines, and the kernel test fails, `NaN + NaN, left true: row 9 is NaN, broadcast NaN`.
Here every one passes.

## 6. The teeth

Each from the saved copies: master's file over the fixed one, the tests run, the fixed
copy back; `cmp` against the fixed copies after each round.

| tooth | what it restores | what fails |
| --- | --- | --- |
| T1 | master's aggsum.go | 237 lines: sum, mean, the list's sum and mean, dot, the horizontal sum and mean, step 523's sums |
| T2 | master's aggstat.go | 240 lines: var, std, product, the list's std, var over a key, step 523's |
| T3 | master's aggmoment.go | 96 lines: skew, kurtosis |
| T4 | master's aggpair.go | 96 lines: corr and cov of a pair with NaNs, corr of a column that does not vary (0/0) |
| T5 | master's aggrank.go | 60 lines: entropy, CorrSpearman of a column that does not vary (0/0) |
| T6 | master's window.go | 15 lines: cum_sum and cum_prod in memory, over a key, reversed, and streamed |
| T7 | master's rolling.go | 10 lines: the rolling sum, mean, var and sum by the index of `h`, in memory and centred |
| T8 | master's rollingmore.go | 12 lines: rolling cov, corr and skew, and the streamed rolling sum and mean |
| T9 | master's rollingbynext.go | 2 lines: the streamed rolling sum by the index |
| T10 | master's ewm.go | 19 lines: ewm mean, std, var and sum, in memory and streamed |
| T11 | master's ewmby.go | 1 line: ewm mean by the index |
| T12 | master's frameconv.go | 5 lines: a frame's `Corr` |
| T13 | master's dispatch.go | 109 lines: every NaN literal on the left added or multiplied, and the kernel's test of a constant against its broadcast |
| T14 | `nan64` made Rust's 0x7FF8000000000000 | 643 lines: the Go oracle is `math.NaN()` itself |

## 7. Found on the way

**A streamed Float32 `CumSum` differs from the answer in memory in its last bits.**
Past a memory limit a cumulative window streams and carries its last answer to the next
batch, a Float32, where in memory the running total is a float64 narrowed a row at a
time: 5,017 of 6,000 rows of `sin(i)·1000.3` as a Float32, in batches of 64 under
16 KiB. A Float64 total, and a NaN now, are the same bits either way. Not this step's
theme, and not small: the fix is to carry the float64 total, as `RollingNext` carries a
rolling window's state. Recorded in feature-gaps §1, open.

## 8. The cost

The work is one look at each answer: a pass over a group-by's groups at Finish, over a
window's rows once it has answered, and nothing on a state. A micro benchmark of
`oneNaN` over a million float64s took 1.4 to 1.9 ms on this laptop, under a load of 7
to 14 from other work; a read-only sum of the same values took 5.2 ms. That pass is
about a tenth of a cum_sum's time, the cheapest kernel it follows, so `winCumFloat`
makes each running total the one NaN as it is reached instead, in a register.

Queries over 1,048,576 rows, three test binaries built from master, from this step
with the pass after cum_sum, and from this step, alternated under the heavy lock,
median of 11 runs after one. The two rounds at a load of 7 to 8:

| query | master | with the pass | this step |
| --- | --- | --- | --- |
| keyed Sum, Mean, Var, 100,000 groups, 8 threads | 89.6, 94.4 ms | 96.8, 93.8 | 96.3, 93.7 |
| keyless Sum and Mean, 1 thread | 2.85, 2.81 | 2.97, 2.81 | 3.06, 2.83 |
| cum_sum, 1 thread | 11.3, 11.1 | 12.7, 14.2 | 13.4, 11.1 |
| cum_sum of a column a tenth NaN | 10.6, 12.5 | 14.0, 14.1 | 15.2, 12.7 |
| rolling_mean(10), 1 thread | 52.0, 55.3 | 77.9, 77.4 | 54.7, 60.1 |
| ewm_mean, 1 thread | 18.7, 18.7 | 19.3, 19.7 | 19.6, 18.8 |

No cost stands out of the noise; the rolling mean's 77 ms is code that did not change
between the last two binaries. Two later rounds, at a load past 10, slowed every
binary alike, up to three times, and are not shown. A NaN literal in arithmetic costs
a broadcast copy of it, only where it is a NaN.

## 9. Checks

After the last edit, on master 73f9863 with this step on top, every `go test` with
`GOFLAGS=-p=1 GOMAXPROCS=4 GOEXPERIMENT=simd GOAMD64=v3`, under a load of 7 to 16
from other work:

| check | outcome |
| --- | --- |
| `go test ./internal/...` | ok, 290 s |
| `go test .`, in full, under the heavy lock | ok, 1,130 s |
| `cd qcheck && go vet ./... && go test ./...` | ok, vet clean, 242 s (`self` 125 s) |
| `GOEXPERIMENT=simd go vet ./...` | clean |
| `go run ./internal/gen/levels` | ok |
| `GOEXPERIMENT=simd go run ./internal/gen/api -check` | 2,481 features, 0 gone since v0.7.2 |
| `go run ./internal/gen/testinventory -check` | 1,223 hand-typed lines of 1,223 allowed; the new tests add none |
| `gofmt -l .` (the GOROOT's) | nothing |
| the new tests at `GOAMD64=v1` | ok: NaNs and nulls held, not the recorded bits |
