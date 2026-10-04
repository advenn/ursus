# Step 78 — as built

**The optimizer does not change the answer.** This is `audit.md` §3's open rows: O4,
O5, O8b, O8-join, O9 and P1, which step 70's on/off differential still counted, with
O10 and O11 beside them. O7 is closed as step 71's. It is the fifth step of the road
to 0.3.

Eight commits and this document.

Authoritative where it disagrees with the vision docs and [`design/`](./design/).

Each of these was asserted on its own exit code:

- `make test-all` exit 0, with **105** package-ok lines: 21 packages × 5 SIMD
  configurations.
- `make race` exit 0 (21).
- `make levels` and `go vet` clean in all three modules.
- PDS-H SF=0.1 exit 0, with **22/22** matching the duckdb reference.

No benchmarks. **No golden plan moved**, including TPC-H's.

**The suite is 2753 passing tests and subtests** (2740 at step 77).

**Step 70's generated differential — 1629 queries, each run optimized and
unoptimized — has no mismatch left.** It counted 164 at the start of this step: O4
19, O5 9, O8b 2, O8-join 1, O9 1 and P1 132.

---

## 1. What was wrong

| row | the optimized answer | the cause |
| --- | --- | --- |
| P1 | `WithColumns(x+1 as w)` on `{x, w, y}` gave `{x, y, w}`, where `CollectSchema` promises `{x, w, y}`. 132 shapes. | Projection pushdown dropped the requirement for a name the node redefines, so the input stopped supplying the column whose place the redefinition takes. |
| O4 | NaN matched NaN under `==` in a cross join's Filter, `JoinWhere`, `WhereExists` and `WhereNotExists`. | The cross-join collapse turned the equality into hash keys, and a hash join matches by grouping equality. |
| O5 | `JoinNullsEqual(true)` on a cross join made `Filter(k == k_right)` match null keys. | The collapse copied the flag (`c := *j`), under a comment saying it "stays false". The flag was accepted on a cross join and did nothing until then. |
| O8b | `Unique("s").Filter(ok).Filter(s.Cast(Int64) > 1)` failed. | The cast moved below the Distinct, ahead of the guard that stays. |
| O8-join | A strict cast pushed into an inner join's side failed on a row the join removes. | |
| O9 | `Unique("x")` over `[-0, +0]`, then a filter telling them apart, kept the wrong row. | Pushdown assumed a predicate is constant on a duplicate class, which floats are not. |
| O10 | `GroupByDynamic(Col("t").Shift(1))` was ErrInternal. | The index and keys were never checked for a window. |
| O11 | `Explain` and `CollectSchema` accepted `Sum().OverWith(OrderBy…)`. | Only the physical planner refused it. |

## 2. Evidence first

**E1 extends `optimizer_byhand_test.go`**, whose cases run optimized and unoptimized
against a hand answer.
- **Six new cases** join the two P1 and O8b cases already listed. Each was wrong only
  when optimized.
- **`TestPlannerRefusesWhatCollectRefuses`** asks `CollectSchema`, `Explain` and
  `Collect` to refuse O5, O10 and O11 alike. That is three cases.

The generated evidence is the differential itself, whose counted ratchet had to
reach zero.

## 3. The fixes

1. **P1:** a redefined input column the parent wants stays required, so the new value
   lands in place. Its old values are read and replaced.
2. **O4 and O5:**
   - When the collapse extracts an equality whose key is compared as a float, it keeps
     the equality as a residual as well. The IEEE test removes exactly the NaN pairs
     the hash match let through; −0 and +0 are equal under both.
   - Both collapse arms set `NullsEqual` false.
   - `JoinNullsEqual` on a cross join is refused at resolution.
   - The differential's `cross-nulls` transform no longer sets the flag, and keeps the
     equality over nullable keys.
3. **O8b and O8-join: `plan.fallible`.** A conjunct can fail on its data when it holds
   a strict cast that is not total, a udf, arithmetic on a temporal operand, or
   `dt.truncate` / `dt.epoch`.
   - In the projection, Distinct and join arms, a fallible conjunct moves down only if
     every conjunct before it moves to the same place.
   - A join keeps a fallible conjunct out of any side whose rows it can lose. Only the
     preserved side of a Left or Right join keeps every row.
   - Polars pushes these anyway; ursus keeps O8's contract.
4. **O9:** a conjunct reading a column that can hold a float, at any depth, stays
   above a Distinct, whole-row or subset.
5. **O10 and O11:**
   - `resolveTemporalGroup` rejects a window or an aggregate in the index or keys.
   - `expr.Window`'s resolution refuses an ordering over an aggregate. The physical
     check stays as the backstop.

## 4. Teeth

Every patch was checked to have applied, and every one ran against a green baseline.
**13 of 14 bite.**

| reintroduce | fails |
| --- | --- |
| P1's requirement deleted | the differential, the P1 case |
| no float residual, the cross-join arm / the semi-anti arm | the differential, two O4 cases / the other two |
| `NullsEqual` accepted on a cross join | the O5 refusal |
| `fallible` always false | the differential, the O8b and O8-join cases |
| no ordering rule: subset Distinct / whole-row Distinct / projection / join | the differential and O8b / and each of the three guard cases (below) |
| no join side rule | the differential, O8-join |
| no float rule at a Distinct | the differential, O9 |
| no `rejectWindow` on the index / no OrderBy refusal in resolution | O10 / O11 |

**Silent, and why:**

- **Removing `NullsEqual = false` from the collapse alone.** Resolution now refuses
  the flag on a cross join, so no resolved plan brings it to the collapse. The reset
  stays as the comment's promise made true, defence in depth behind the refusal,
  which bites.

**Silent at first, and fixed:** the ordering rule in three arms. No case had a guard
that stays while a cast behind it could move. A case was added for each, in its own
commit:
- a float guard over a whole-row Unique;
- a udf-column guard over a WithColumns;
- a right-side guard over a Left join, whose left side alone would let the cast in.

## 5. A mistake in the process

**The O9 fix's commit was pushed with the differential failing.** Its edit to empty
the differential's ratchet did not match the gofmt-aligned map. The test that would
have said so ran as `go test … | tail -1`, and a pipeline's exit status is tail's, so
the commit chain went on.

The next commit removed the stale entry, and its message says why. Every commit
since checks the test's own result before committing.

## 6. Behaviour changes

- **A redefined column keeps its place**, at the cost of reading its old values.
- **A float equality over a cross join, `JoinWhere` or `WhereExists` is IEEE**, so NaN
  never matches. The join is still a hash join.
- **`JoinNullsEqual` on a cross join is refused.**
- **A filter with a strict cast, a udf, temporal arithmetic or a truncate/epoch is not
  pushed ahead of a guard, nor into an inner join's side.** Such a query may read more
  rows than before.
- **A filter reading a float does not pass a Distinct.**
- **A window or an aggregate in `GroupByDynamic`'s index or keys, and an ordering over
  an aggregate in a window, are refused at plan time.**

## 7. Still open

- **O12**, which is acceptable as recorded, **O13**, and **O14** (`Goexit` in a udf).
- The rest of the road to 0.3: an Enum you can construct, and its casts, next.
