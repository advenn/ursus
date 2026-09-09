# Step 44 — as built

**`WhereExists` and `WhereNotExists` ship, and q21 finally stops counting distinct
suppliers.** The claim steps 38 and 39 made and step 43 retracted is paid: the
motivating query is **1.4x faster and uses 1.75x less memory**.

The more useful half of this document is that the first working version was **6.8x
SLOWER**, and the reason is a design mistake that every test passed.

Authoritative where it disagrees with [`step-43-as-built.md`](./step-43-as-built.md),
the vision docs and [`design/`](./design/).

`make test-all` exit 0 (75 package-ok lines across five configurations),
`make race` exit 0 (15), `make levels` and `go vet` clean in all three modules —
each asserted on its own exit code. **PDS-H SF=0.1 validates 22/22** against the
duckdb reference, with q21 rewritten.

---

## 1. What shipped

```go
func (lf *LazyFrame) WhereExists(other *LazyFrame, preds ...Expr) *LazyFrame
func (lf *LazyFrame) WhereNotExists(other *LazyFrame, preds ...Expr) *LazyFrame
```

Named for the SQL a user is transcribing rather than for the join kind, because the
output **is** the left frame — `join_layout.go` says so as an equality, not an
approximation. There is no Polars name to copy: `join_where` there is inner-only.

Under them, `plan.Join` gains one field:

```go
// Residual is evaluated per candidate PAIR, before the match verdict.
Residual []expr.Node
```

and the builder emits a **keyless semi/anti join carrying the predicates**, which
`collapse_cross_join` — step 43's rule — then splits:

```
built:      JOIN SEMI residual [(l_orderkey == l_orderkey_right), (l_suppkey != l_suppkey_right)]
optimized:  JOIN SEMI [l_orderkey] = [l_orderkey] residual [(l_suppkey != l_suppkey_right)]
```

Both plans are golden files. The rule needed one new arm, not a new rule: deciding
which conjunct is an equality straddling the two sides is the same question it
already answered for `Filter` over `Cross`, and `equiKeys`/`sideMask`/`rewriteForSide`
answer it unchanged.

**Why it could not be a filter above the join.** A semi join emits a left row once
if ANY right row matches, so filtering its output filters rows that already
survived. `EXISTS(r : k(r)==k(l) AND p(l,r))` is not
`EXISTS(r : k(r)==k(l)) AND p(l, …)` — the second has no `r` to name. That is the
whole reason step 43 could ship `JoinWhere` without touching an operator and this
step could not.

---

## 2. The mistake: a vectorised engine run at scalar granularity

The first version evaluated the residual **one probe row at a time**. It is simpler
— all of a row's candidates belong to that row, so "does any pair satisfy" is just
"is the filtered batch non-empty", with no per-row reduction — and every test
passed.

Three matched pairs on q21 at SF=0.1:

| | before (`n_unique`) | after (per-row residual) |
| --- | --: | --: |
| time | 333 / 389 / 373 ms | **2538 / 2691 / 2450 ms** |
| peak | 0.26 / 0.25 / 0.25 GB | 0.30 / 0.24 / 0.25 GB |

**6.8x slower and no memory win.** The cause is the fan-out: about four lineitem
rows per `l_orderkey`, so every `gatherOut` built a four-row batch and every
expression call ran on four values, ~3.8 million times. All of the engine's
vectorisation, paid for and then discarded at the last step.

The fix is to gather candidates from **many probe rows** into one pair batch,
evaluate once, and fold the result back per row — which is what the plan's §3 said
before I decided the per-row form made the fold unnecessary. It does make it
unnecessary; it also makes the whole thing slow.

| | before | after (batched residual) |
| --- | --: | --: |
| time | 312 / 290 / 314 ms | **198 / 237 / 199 ms** |
| peak | 0.24 / 0.25 / 0.25 GB | **0.14 GB** ×3 |

**1.4x faster, 1.75x less memory.** Same answers, same tests, 12 lines of loop
structure apart.

The lesson is not "batch things". It is that a design can be correct, pass every
test, read better than the alternative, and still be the wrong shape — and the only
thing that says so is a measurement against the query the feature was justified by.

### Two paths, on purpose

`stepResidual` batches across probe rows and never splits a row. A row whose
fan-out alone exceeds a chunk falls back to `residualMatch`, which chunks *within*
the row and keeps the pair batch bounded. Batching across rows when fan-out is
small; within a row when it is enormous. Both are exercised: the ordinary fixtures
have fan-out 2, and `TestWhereExistsHighFanOut` gives one probe row 20,000 partners.

---

## 3. What the teeth found

| tooth | result |
| --- | --- |
| `newSub` loses `pairLayout` | **bites** — but only once the fixture actually spilled; see below |
| projection pushdown drops the residual's columns | **bites** — `unknown column "rv"` |
| never evaluate the residual | **bites** |
| `needBuildRows` stays false for a residual join | **bites** |
| evaluate the residual after the verdict, not before | **bites** — 2 rows where 5 are right |
| a routed row is judged here as well as by its sub-join | **bites** — semi 4000 + anti 4000 = 8000 of 4000 rows |
| a null predicate counts as a match | **bites** |
| anti early-exits like semi does | **does not bite** — §4 |
| let a probe row's candidates split across chunks | **does not bite** — §4 |

**A spill test that did not spill.** The first `newSub` tooth passed, which should
have been impossible — dropping the residual's namespace on a replayed bucket
cannot be harmless. It was: the fixture fit in the limit and never reached the
replay path at all. The test now asserts `MemoryStats.Spills > 0` before comparing
anything, and with a fixture that actually spills the tooth bites. A test that
cannot reach the code it names is worth less than no test, because it reads as
coverage.

---

## 4. Two claims the teeth disproved

**Anti cannot early-exit.** The code had `if p.spec.emitMatched { break }` and a
comment explaining that proving no partner satisfies the predicate means testing
every one. Removing the guard changed no answer, and the reason is obvious in
hindsight: **one satisfying partner decides both verdicts** — the row is kept by
`WhereExists` and excluded by `WhereNotExists`. The guard bought nothing and cost
anti a full scan of every row it excludes. It is gone.

The real asymmetry is in the other case and is a property of the data, not the loop:
a row with *no* satisfying partner is only known to have none after every candidate
has been tested. That is `WhereNotExists`'s common case and `WhereExists`'s rare
one.

**The chunk-size guard enforces row completeness.** It does not. The inner loop
drains a row unconditionally, so removing the guard changes how large a chunk gets
and nothing else. It is a memory bound; the invariant is structural.

---

## 5. The cost, stated plainly

A semi or anti join with a residual **retains its build rows**. Without one their
memory is O(distinct keys) — `extjoin.go` argues at length that this is what makes
them the two kinds that never need the bucket rule — and a predicate that reads a
right-hand value needs the rows to read. `needBuildRows` is therefore no longer a
function of the kind alone, and both docs that said otherwise now say this.

On q21 that trade is still strongly positive, because what it replaces is two
`n_unique` accumulators over 1.5M and 1.38M groups. On a query with a large build
side and a cheap predicate it might not be, and there is no cardinality estimate
anywhere in the plan layer to decide it automatically.

---

## 6. Verification

- **The differential is rule-on against rule-off.** With `CollapseCrossJoin`
  disabled the join stays keyless and tests every pair of one key group — a
  different code path, same answer required — across batch sizes {1,2,3,7,64,8192}
  and thread counts {1,2,3,4,8}.
- **An independent oracle**: `WhereExists` compared against the row-index → inner
  join → distinct → semi-join formulation this step rejected on cost grounds. It
  shares no code with the probe's verdict, and the fixture is asserted to be
  discriminating before the comparison is trusted.
- The partition property (`exists` + `not exists` = every row, disjoint), one left
  row against three satisfying partners returning one row, the high-fan-out
  boundary, a residual column in no key surviving projection pushdown, a pure
  inequality that must not collapse, a spilled build side, and both error paths.
- **q21 validates against duckdb** in its new form, and all 22 answers still match.

---

## 7. What is still open

- **Right and Full residuals.** `matched` is indexed by key id, and its doc calls
  that "identical in meaning" — true without a residual, false with one, since two
  build rows can share a key and disagree. `resolveJoin` refuses the combination, so
  the two never meet; the comment now says why rather than leaving a trap.
- **When a residual is worth it** — no cardinality estimate exists to choose
  between the residual join and a group-by rewrite, so the user chooses by writing
  one or the other.
- **The published q21 figure is still step 41's**, measured at SF=1 on the old
  form. This step measured at SF=0.1 only; a suite re-run is its own step.
- The standing list: the parallel join's memory trade (28–47% faster, 30–45%
  heavier, still not a decision), CSV scanning, inline keys for `KeyTable`, the heap
  sampler being unconditional, `quantile`/`median` per-group storage, nested writing
  and `as_struct`, `.list` set operations, Map/Array, Pivot/Unpivot, SQL, cloud
  stores, join reordering.
