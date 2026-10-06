# Step 89 — as built

**PDS-H q7's date filter is pushed below its joins again.** This was found by
benchmarking v0.3.0. Against step 40's report, q7 went from 2.4 s and 0.60 GB to
3.9 s and 1.68 GB, and it became ursus's peak-memory query at SF=1.

Two commits: the evidence, then the fix. The as-builts for steps 90 and 91 carry
the gate this one shares with them.

## 1. What was found

The runner's `date(...)` is `Lit(time.Time).Cast(ursus.Date)`: a nanosecond Datetime
literal narrowed to a Date. Three facts made it block pushdown:

1. **It is never folded.** `litFromColumn` folds a constant only into a number, a
   Bool or a String. It declines every temporal type, because `litColumn` builds
   a `time.Time` literal with 64-bit ticks, and a Date's payload is 32 bits wide.
2. **Step 78 counts it as fallible.** Since step 78, a filter conjunct that can
   fail is not pushed into a join side. A strict cast counts as fallible unless
   `totalCast` calls it total, and `totalCast` knew no temporal cast at all.
3. **So the filter stayed above the joins.** In q7 it sits on the Concat of two
   five-join chains, and stayed there, so every lineitem row went through the
   order, supplier and nation joins first.

The cause was confirmed by explaining q7 in the trees of step 77 and step 78. In
step 77's, the filter sits on the lineitem scan; in step 78's, it sits above the
joins.

## 2. The fix

`totalCast` learns two temporal rules:

- **Datetime → Time cannot fail.** It folds into the day, as of step 86.
- **A rescale is total when every source tick fits the target after the change of
  unit.** This covers one instant to another, Duration to Duration, and Time to
  Time. It is computed exactly in `big.Int` (`totalRescale`), not tabulated.
  - A nanosecond or microsecond Datetime narrowed to a Date is total.
  - A Date widened to a nanosecond Datetime is not, since it overflows outside
    1677–2262.
  - A Datetime in seconds narrowed to a Date is not either, since it overflows
    the 32-bit day count.

**Evidence:** `castpush_byhand_test.go`, 9 cases, 4 of them wrong. The controls keep
every cast that can fail where it was written. One hand case was itself wrong: it
compared against an Int64 literal cast to Time, which step 86 made fallible on
purpose.

**Teeth: 3 of 3 bite.** No rescale total; every rescale total; the Time rule removed.

## 3. Measured

ursus only, PDS-H SF=1, under the benchmark's 8 GB cgroup:

- **q7:** 1,723 ms and 0.63 GB, against 3,914 ms and 1.68 GB in the v0.3.0 report.
- **Peak memory across the suite:** 1.56 GB, against 1.68.
