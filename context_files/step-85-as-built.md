# Step 85 — as built

**The open silent wrong answers a 0.3 user can reach.** Step 84 made the docs ready,
and its known-limitations list still carried six audit rows that answer wrongly or
crash. Three of them were cheap, measured and reachable through ordinary use, and
shipping a release with them known was not "ready". This step fixes those three:

- **A16:** `Diff` over an unsigned column;
- **O14:** a UDF that calls `runtime.Goexit`;
- **S22:** a pad with a huge width.

It also applies **S24's** limit to the pads. It rewrites `CHANGELOG.md` with the
release-notes material gathered across steps 22–83.

Five commits and this document.

Authoritative where it disagrees with the vision docs and [`design/`](./design/).

Each of these was asserted on its own exit code:

- `make test-all` exit 0, with **105** package-ok lines: 21 packages × 5 SIMD
  configurations.
- `make race` exit 0 (21).
- `make levels` and `go vet` clean in all three modules.
- PDS-H SF=0.1 exit 0, with **22/22** matching the duckdb reference.

No benchmarks.

**The suite is 2913 passing tests and subtests** (2890 at step 84).

---

## 1. What was measured

| row | before |
| --- | --- |
| A16 | `Diff` over UInt8 `[3, 1, 255, 0]` was UInt8 `[∅, 254, 254, 1]`, and likewise at every unsigned width. Polars widens: UInt8→Int16, UInt16→Int32, UInt32→Int64; UInt64→Int64 with nulls where it overflows. |
| O14 | A UDF calling `runtime.Goexit` on the third of five rows, at 4 threads and a batch size of 1, answered **2 rows and no error**, in a `Select`, a `GroupBy().Agg` and a join probe alike. |
| S22 | Not run: before the fix, a 1<<40 width asks for a terabyte per row, and a fatal out-of-memory is no test to run on a laptop. Read from the code: `strings.Repeat(fill, width)` per row, with nothing bounding the width. |

## 2. Evidence first

**E1 is `openrows_byhand_test.go`: 9 cases, 7 wrong.**

- **A16:** the four unsigned widths, plus controls for Int8, which stays Int8 and
  wraps as Polars does, and for Datetime, whose Diff is a Duration.
- **O14:** the three parallel shapes. Each query runs in a goroutine of its own
  under a 10 s deadline, so a Goexit that reached the caller's goroutine, or a
  hang, is reported rather than ending the test.

The S22 cases landed with their fix (§3), for the reason in §1.

## 3. The fixes

1. **A16.** `Diff` takes an unsigned column one signed width up before subtracting,
   through a new internal call typed at plan time, as `PctChange`'s float is. The
   widths:
   - UInt8 → Int16, UInt16 → Int32, UInt32 → Int64;
   - **UInt64 → Int128**, which holds every difference exactly, where Polars gives
     Int64 and nulls. It is the same choice ursus makes for integer sums.

   Signed integers keep their width and wrap, as all of ursus's integer arithmetic
   does, and as Polars' `diff` does. The call-family count test went from 63 to 64,
   as it is meant to.
2. **O14.** `goexitGuard(report, body)` runs a goroutine's body and checks, in a
   defer, whether the body returned.
   - A Goexit and a normal end both reach that defer with `recover()` nil; only
     the flag tells them apart.
   - When the body did not return, report gets a KindValue error before the
     goroutine's own defers close its lane, so the error takes the place of the
     dropped rows.
   - It wraps all five goroutines that run a caller's code: the parallel pool's
     dispatcher and workers, the join probe's dispatcher and workers, and the
     parallel sink's workers. For the sink, report is `fail`, which also cancels a
     dispatcher a dead worker would otherwise leave blocked.
   - The hint names `testing`'s `FailNow`, `Fatal` and `SkipNow`, which call
     Goexit.
3. **S22, and S24 for the pads.** Before allocating, the kernel counts the bytes the
   output needs: each row's own, plus its padding at the fill's width. Past what a
   String column's 32-bit offsets hold, it refuses with KindValue. The count is
   exact, so a pad that fits is never refused, and it is guarded against int64
   overflow. The limit is a package variable, so the kernel's own test can lower it
   to 4 KiB.

**The first draft of the guard was wrong, and it was caught before it ran.** It was
a function to be deferred, deciding in its defer whether the goroutine had exited.
It cannot: `recover()` is nil both after a Goexit and after an ordinary return.
Only running the body and setting a flag after it can tell the two apart.

## 4. Teeth

Every patch was checked to have applied, and every one ran against a green baseline.
**All 11 bite**, after one re-aim and three cases added for silent teeth.

| reintroduce | fails |
| --- | --- |
| Diff not widened / UInt64 widened to Int64 | the four widths / the UInt64 case |
| the guard believing every body returned | all five Goexit cases (re-aimed after a first patch left a variable unused) |
| the pool worker's / join worker's / sink worker's report dropped | its Goexit case |
| the pool dispatcher's / join dispatcher's report dropped | **silent at first:** no case ran a UDF in a dispatcher. A sort key does, evaluated as the sort drains in the goroutine pulling from it. Two cases were added, one above a parallel stage and one above a join probe. |
| the pad bound removed / the fill counted as one byte | the kernel test's refusals / its wide-fill case |
| the int64-overflow guard dropped | **silent at first:** over three rows, the wrapped totals wrapped back past the limit and were refused by accident. A one-row case cannot. |

**The pad teeth run only the kernel test**, whose limit is 4 KiB. With the bound
gone, the public 1<<40 cases would ask for a terabyte.

## 5. Behaviour changes

- **`Diff` over an unsigned column widens:** UInt8→Int16, UInt16→Int32, UInt32→Int64
  and UInt64→Int128.
- **A UDF that ends its goroutine with `runtime.Goexit`** in parallel execution is a
  KindValue error, not a shorter answer.
- **`PadStart`, `PadEnd` and `ZFill`** refuse, as KindValue, a width whose output one
  String column cannot hold.

## 6. Still open

- **S24 beyond the pads:** every other string builder past 2 GiB in one column.
  Testing it needs 2 GiB.
- **O13 and I23**, both unmeasured; I23 needs a third-party writer.
- **The lower-severity silent rows** that `CHANGELOG.md` lists.
- **The tag:** step 84's §4 has the commands.
