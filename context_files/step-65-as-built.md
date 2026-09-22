# Step 65 — as built

**Two udfs sharing a name were one computation.** The only defect on the carried
list where the engine answered wrongly and said nothing. `v0.3-scope.md` item 5.

Four commits and this document.

Authoritative where it disagrees with the vision docs and [`design/`](./design/).

`make test-all` exit 0 (**100** package-ok lines = 20 packages × 5 SIMD configurations),
`make race` exit 0 (20), `make levels` and `go vet` clean in all three modules, PDS-H
SF=0.1 exit 0 with **22/22** matching the duckdb reference — each asserted on its own
exit code. No benchmarks. `testdata/plans/udf_raw.txt` and `udf_optimized.txt` are
**byte-identical**, asserted rather than assumed, because the identity this step adds
must not render.

---

## 1. Measured first, at four maps

`UDF.String()` renders the *name*, because a Go closure has no printable identity.
Four maps deduplicate expressions by their rendering, so two different closures under
one name render alike, merge, and both results come from whichever was seen first.
No error, right row count, right type, right nullability.

| query | got | want |
| --- | --- | --- |
| `max(double(id))`, `max(triple(id))` in one `Agg` | 8 and **8** | 8 and 12 |
| the same, per `GroupByDynamic` window | 2 and **2** | 2 and 3 |
| `max(…).Over(g)` twice in one `Select` | 6 and **6** | 6 and 9 |
| `sum().Over(bucket)` and `max().Over(bucket)` | **3** | 20 |

The last is the worst and is not a shared temporary but a shared **partitioning**.
With `t = 0..5` and `v = [1,10,3,20,3,null]`, the second window asks to be bucketed
by `t%3` — row 0 would join `t=3`, giving `max(1,20) = 20` — and is bucketed by `t%2`
instead, giving 3. Every row gets another function's group.

`udf.go` stated the consequence and did not enforce it. The one existing test proved
only the *positive* direction; changing its two names to one string returns 20 and 20.

## 2. It is four maps, not three

Three separate comments said three, and listed the same three files — `udf.go`,
`internal/expr/udf.go`, `internal/expr/names_test.go`. `extractAggs` is instantiated
**twice**, once per `plan.Aggregate` and once per `plan.TemporalGroup`, and the
temporal one appeared in no list.

That is the failure mode `names_test.go` exists to prevent, happening to
`names_test.go`: a hand-written list of a thing that grew. It is the second time in
two steps — step 64's whole subject was a hand-written axis in a file arguing against
hand-written lists.

## 3. The remedy recorded in four documents does not work

`step-57`, `step-58`, `step-64` and `v0.3-scope.md` all said identity *"has to go
through `reflect.ValueOf(impl).Pointer()`"*.

It detects nothing. Every `MapElements` udf closes over the **same func literal** in
`udf.go`, and every `MapBatches` over another, so `Pointer()` — which returns the
*code* pointer, and whose own doc says it is "not necessarily enough to identify a
single function uniquely" — answers the same value for every udf of a given kind. A
check written as "same name is fine if the impl pointers match" matches **always**,
accepts every collision, and ships as a guard that is not there.

**I wrote that remedy into `v0.3-scope.md` four days ago**, carrying it forward from
step 57 without checking it. It was carried through three as-builts before that. The
tooth against it is now a test: `TestEveryUDFGetsADistinctIdentity` passes one impl
value twice on purpose, which is the case every closure-derived scheme fails.

The neighbouring claim in those documents *is* right: `==` on two `expr.UDFImpl`
values compiles, because the field is an interface, and **panics** at runtime —
`comparing uncomparable type kernel.ColumnUDF`.

## 4. The fix

An unexported monotonic `id` on `expr.UDF`, minted by `expr.NewUDF`. `&expr.UDF{…}`
was constructed in **exactly one place in the module**, so the constructor is
enforceable by inspection, and a bare literal carries 0 — which the check reports as
an ursus bug rather than treating two unidentified udfs as one.

Three properties, each a test:

- **Distinct per construction**, even from one closure — §3.
- **Preserved across copying.** `Rebuild` and `extractAggs` both do `c := *t` on
  every rewrite, so node-*pointer* identity would be useless and a lost field would
  make two copies of one udf look like two udfs.
- **Never rendered**, so the goldens do not move. The node's doc rejected a monotonic
  counter for making goldens depend on allocation order; that objection is about a
  counter that *renders*, and the comment now draws the distinction.

**The rule is "one name, one implementation"** — not "one name, one node", which
multi-column expansion breaks by design, and not the narrower "one *rendering*, one
implementation", which is sufficient for today's four maps but is not a rule a caller
can check without knowing where the planner puts things.

The false positive is irreducible and was a decision, not an oversight: a helper
called twice mints two closures that are semantically identical, and Go offers no way
to tell that from the real bug. It is refused, and the refusal names the fix — hold
the `Expr` in a variable and use it twice, which makes it one node, one id, one
computation, and is what the caller meant.

## 5. Where it runs, which is the whole step

`plan.Resolve`, **at the top, before `TransformUp`**.

`extractWindows` runs *inside* that walk, and on a merge it returns a `Col` reference
and **never appends the losing subtree** — the second udf is deleted from the tree. A
check after the walk sees one `"f"` and passes on exactly the query it exists to
refuse.

That is measured, not argued. Moving the hook after `TransformUp` leaves
`TestUDFCollisionInAWindowTemporary` failing while the other three still pass,
because nothing deletes those. **It would have shipped green**, with three tests
agreeing it was fine.

The design brief I started from said the opposite — that a check must run after the
walk "or it double-counts". Both halves were wrong: the subtree is *moved*, not
copied, so nothing double-counts either way.

All three entry points — `CollectSchema`, `Explain`, `compile` — inherit the one
hook, so `Explain` cannot render a plan `Collect` will refuse. That asymmetry is
already on the defect list for `Optimizer.Verify` and did not need a second instance.

## 6. The instruments, and the vacuity they had to avoid

Four end-to-end tests, one per map, each pairing the refusal with a **control** in
the same shape with distinct names whose answers must differ: 8 and 12, 2 and 3,
6 and 9, and 20 rather than 3. Without the controls, "the planner refused" would be
indistinguishable from the query being broken some other way.

**Those four prove nothing on their own.** Once the collision is refused at plan
time, no end-to-end query can reach any of the four maps with one, so all four
degrade to "the planner refused something" and would keep passing if a dedup map were
deleted outright. So each merge is pinned where it happens, by calling the merging
function directly — `extractWindows` collapsing two windows to one temporary,
`extractAggs` collapsing two aggregates to one spec — each with a differently-named
control proving the name reaches the rendering.

The partition-key map needs a planned operator to reach, so what is pinned there is
its precondition: two windows can render *differently* while their
`StringAll(PartitionBy)` renders the *same*. That is what makes the site reachable at
all, and it is what would break first if the rendering changed.

## 7. Teeth

| reintroduce | result |
| --- | --- |
| the hook moved after `TransformUp` | **bites** — one test of four, and the right one |
| the id made a constant | **bites** — the distinct-identity test |
| the id rendered in `String()` | **bites** — the golden plans |
| the zero-id arm dropped | **bites** |
| the same-id arm dropped | **bites, after a test was written for it** |
| the check removed entirely | **bites** — all four |

**Two teeth read as silent and neither was.**

The hook-placement tooth — the most important one in the step — reported nothing on
its first run. The patch had not applied: I built it out of three chained `python3
-c` edits with nested quoting, and the result compiled without moving anything.
A tooth that fails to *apply* looks exactly like a tooth that fails to bite, and the
only way to tell is to check the patched file. It bites cleanly when applied
properly, and that is now the first row of the table.

The same-id tooth was genuinely silent, for a reason worth keeping: **the check runs
before expansion**, so `Col("a","b").MapElements(…)` has not yet become two nodes
when it runs, and nothing else in the suite put one udf in two places. The arm was
unreachable *by the tests*, not unnecessary — reusing one `Expr` twice reaches it,
which is also the workaround the refusal recommends. The test for that was missing;
writing it made the tooth bite and closed a real coverage gap.

## 8. Still open

- **`dtype.Interval.Every` accumulates unchecked** — the other half of scope item 5,
  and the natural step 66. Six sites, each a multiply *and* an add.
  `Every("357913942y")` is **8 months**, `Every("613566757w")` is **3 days**,
  `Every("5124096h")` is **25m26s** — all with `Err() == nil` and `Negative() ==
  false`, passing every downstream gate. `Every("536870912y")` reaches **MinInt32**
  months in twelve characters, after which `String()` emits `--178956970y-8mo`,
  which `Every` cannot re-parse. `tempgroup.go`'s 4096 and `1<<24` guards both
  exhaust into `return out, nil` with no error, silently dropping rows. The
  `err`/`Err()` field already exists and every consumer gates on it, so the fix needs
  no signature changes. **Zero fuzz targets in the repo**, and `Every` is the obvious
  first one.
- **Mixed-sign `Interval.String()` is broken independently of overflow**:
  `Negative()` is an OR over three fields while `String()` negates all three, so
  `IntervalOf(1, -1, 0)` renders `--1mo1d` with no arithmetic error anywhere.
- **A udf inside a future lambda body would be invisible** to `expr.Walk`, hence to
  this check *and* to `expr.HasUDF`, which guards predicate pushdown today.
  `Children()` deliberately excludes a nested scope body and nothing implements one
  yet; `List().Eval()` would be the first. Recorded in `CheckUDFNames`' doc.
- `MonthsInterval` and `DaysInterval` are exported, unvalidated and called nowhere.
- Carried from `v0.3-scope.md`: nested Parquet write; cloud object stores; decimal
  aggregation's precision rule; `unique`/`over` not spilling; the byte-flip spill
  sweep; `Optimizer.Verify` off in `Explain`; the seventeen-site planner leak;
  `spill.Writer.Write`'s bare error; `callCache` eviction; `Pivot`; the stale
  benchmark suite.
