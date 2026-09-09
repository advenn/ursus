# Step 43 — as built

**`JoinWhere` ships, and it is an optimizer rule rather than a join.** The
longest-carried item in the project — open since step 14, named in seventeen step
documents — turned out to need no new plan node, no new operator and no physical
change at all.

Two things the teeth disproved are the more useful half of this document: a doc
comment claiming a correctness guard that guards nothing, and a plan claiming a rule
ordering was load-bearing when it is not.

Authoritative where it disagrees with [`step-42-as-built.md`](./step-42-as-built.md),
the vision docs and [`design/`](./design/).

`make test-all` exit 0 (75 package-ok lines across five configurations),
`make race` exit 0 (15), `make levels` and `go vet` clean in all three modules —
each asserted on its own exit code. **PDS-H SF=0.1 validates 22/22** against the
duckdb reference — the leg that matters here, because three of those queries already
write a cross join followed by a filter, so they meet the new rule whether or not
anyone types `JoinWhere`. Which of them it fires on is §7.

---

## 1. What shipped

```go
func (lf *LazyFrame) JoinWhere(other *LazyFrame, preds ...Expr) *LazyFrame {
    return lf.Join(other, JoinHow(JoinCross)).Filter(preds...)
}
```

That is the whole API. The content of the step is `collapse_cross_join`, a new
optimizer rule:

```
raw                                      optimized
FILTER [(k == k_right) AND (lv < rv)]    FILTER [(lv < rv)]
  JOIN CROSS                               JOIN INNER [k] = [k] no coalesce
```

Both plans are pinned as golden files, and the diff between them **is** the rule.

`ScanSpec`-style plumbing, a `JoinWhere` node, an `Expressions()` arm, a projection
pushdown arm, a `Layout()`, a physical operator: none of it. The rule's output is
`Filter` over `Join`, both of which existed, so the parallel probe from step 36, the
spilling join, `gatherOut` and batch-size invariance all apply untouched.

The API doc had already fixed the signature (`ursus-api.md:728`) and
`dataframe-features.md` §7.4 had already specified the design — *"`on` equality is
optimizable into a hash join, the rest into a loop join"* — and §11 had already
listed the pass by name: *"collapse joins (fold a filter above a cross join into a
real join condition)"*. This step is those two sentences.

**It improves hand-written cross-plus-filter too**, which is a shape already in the
test suite (`TestInnerJoinIsAFilteredCross`) and in the benchmark queries.

---

## 2. The API doc's example could not have worked

`ursus-api.md:979` sketched frame-qualified names:

```go
sessions.JoinWhere(events, ursus.Col("events.ts").Ge(ursus.Col("sessions.start")))
```

Nothing in `internal/expr` can resolve a dotted name — `Col.Field` is a single
`in.ByName(c.Name)` — so `"events.ts"` would be looked up as a column literally
called that and fail. Qualified names would be new binding machinery in the one
place the project has none.

Predicates name the join's **output** columns instead: left names verbatim, a
colliding right column suffixed `_right`. That is what `JoinLayout` already
produces, what every two-sided predicate in the existing suite already uses, and it
costs nothing. The doc is corrected rather than left as an aspiration.

---

## 3. Two claims the teeth disproved

### The side check is not a correctness guard

`equiKeys` requires each side of an `==` to read a column from its own input. The
comment justifying it said a literal *"belongs to any namespace"*, so `Col("k") ==
Lit(5)` would otherwise become a join on a constant, *"which is neither the filter's
meaning nor anything a user wrote"*.

The second half is false. Joining `l.k` against a constant right key is **exactly
equivalent** to filtering the left side on `k == 5` and keeping every right row —
the rows agree, and so do the nulls, since a null left key matches nothing under
either reading. Removing the side check changes no answer in the suite.

What it does buy, and the comment now says so:

- **A plan nobody wants.** A constant key puts every right row in one hash bucket:
  a cross join with a hash table in front of it.
- **The mirror.** `k_right == k` is the same condition written the other way round,
  and only a check that knows which side is which can swap the operands rather than
  refuse. That is what the tooth actually caught.

Correctness against the genuinely bad cases — both operands left-only, both
right-only — belongs to `rewriteForSide` and holds without any of this: a left
column cannot be re-expressed in the right child's namespace, so the conjunct is
refused and stays in the residual filter.

### The rule ordering is not load-bearing either

The approved plan said *"That ordering is load-bearing"* about running after
predicate pushdown. Moving the rule to the front of the rule list **changes no
answer in the suite** — which follows directly from the paragraph above, since the
one-sided conjuncts it would then see turn into equivalent constant keys.

So the ordering is a plan-quality choice, stated as one now: after predicate
pushdown so the one-sided conjuncts have already left, before projection pushdown
because this rule changes which node reads the key columns.

---

## 4. Teeth

| tooth | result |
| --- | --- |
| inherit `Auto` coalesce instead of forcing it off | **bites**, three ways at once — §5 |
| any `OpEq` is a join key: skip the side check | **bites**, but only on the mirror case, and it refuted the comment — §3 |
| build the join with `NullsEqual(true)` | **bites** — 5 rows where 4 are right |
| fire on an inner join, not only a cross join | **did not bite until a test existed for the shape** — §6 |
| run the rule before predicate pushdown | **does not bite** — §3 |

---

## 5. The coalesce trap, and what caught it

`coalescesKey` is false for Cross and true for Inner when the two key names match.
So a rewrite that inherits `Auto` **merges the key columns**, and the rewritten plan
returns one column fewer than the plan it replaced. `Rule`'s contract is same rows,
same order, same schema; dropping a column is the worst of the three, because
nothing downstream can even name what went missing.

`c.Coalesce = CoalesceOff` is the fix, and it is visible in `Explain` as
`no coalesce`, which makes the decision auditable rather than implicit.

Three independent things caught the tooth, and the first is the one worth noting:

```
ursus: rule "collapse_cross_join" changed the output schema
```

That is `Optimizer.Verify`, built in step 2 and running in tests ever since. A rule
written 41 steps later walked into exactly the failure it was built for, and it said
so by name. The golden plan file and the explicit column assertion caught it too.

The fixture matters as much as the check: `joinLeft` and `joinRight` both call the
key `k`. Rename either side and the trap is invisible.

---

## 6. A guard nothing exercised

The rule fires only on `JoinCross`. Deleting that guard broke **nothing** — the
suite had no test with a two-sided equality above an existing inner join, which is
the only shape the guard governs.

It is a real guard: the rewrite *replaces* `LeftOn`/`RightOn`, so firing on a join
that already has keys drops them and joins on the predicate instead.
`TestFilterOverInnerJoinIsNotCollapsed` builds the fixture where those two answers
differ — the obvious fixture does not, because no `lv` ever equals an `rv`, so the
right plan and the wrong one both return nothing — and with it the tooth bites.

This is the step-13 and step-17 pattern for the third time: a branch that is correct,
necessary, and untested until someone goes looking.

---

## 7. Verification

- **The differential is rule-on against rule-off.** `CollapseCrossJoin` is its own
  flag, so the same query runs as a hash join and as a cross-join-plus-filter —
  code that shares nothing — over batch sizes {1, 2, 3, 7, 64, 8192} × thread counts
  {1, 2, 3, 4, 8}, compared with `AssertFrameEqual`, which checks row order.
- **"Did the rule fire" is asserted from the plan**, not inferred. The first version
  of that helper matched `"cross"` against a plan that prints `JOIN CROSS`, so every
  such assertion passed vacuously; one test failing for the opposite reason is what
  exposed it. A check that cannot fail is worse than no check.
- Null keys on both sides, the interval join from the API doc, the equality written
  in either operand order and in either conjunct position, a one-sided predicate
  with predicate pushdown both on and off, and a pure inequality that must **not**
  collapse.
- `JoinWhere` with no predicates is an error naming `JoinCross`, for the reason
  `resolveJoin` gives about a keyless equi-join: the cardinality difference is
  |L| vs |L|×|R|.

### It already fires in production, and where it does not is the better half

`pdsh.go` writes cross-join-plus-filter three times, and the rule sorts them exactly
as designed without anyone changing those queries:

| | the filter | fires? |
| --- | --- | --- |
| q15 | `total_revenue == max_revenue` | **yes** — an equality straddling the sides |
| q11 | `value > threshold` | no — an inequality has no key to extract |
| q22 | `c_acctbal > avg_acctbal` | no — likewise |

So the "leave it alone" branch is exercised by two real queries rather than only by
a fixture, and all 22 answers still match. The gain in q15 is small — the right side
is a one-row aggregate either way — but it is the shape working end to end on code
written before the rule existed.

---

## 8. What this does NOT do

**q21 is unchanged, and steps 38 and 39 overpromised.** Both said `JoinWhere` would
remove q21's `n_unique` — *"which makes it a memory fix as well as a feature"*. That
is true only of a **semi/anti** variant. q21 needs
`EXISTS(l2.l_orderkey = l1.l_orderkey AND l2.l_suppkey <> l1.l_suppkey)`, and an
inner join plus a filter is not a semi join without a deduplication that costs more
than the workaround it would replace. Polars' `join_where` is inner-only and the
signature in `ursus-api.md` takes no `how`, so this step ships the inner form and
the memory claim stays unpaid.

**A predicate with no equality is still O(|L|×|R|).** The rule leaves it alone
deliberately: the keyless probe already streams its pairs, so there is nothing a
rewrite would recover. It also inherits the existing refusal to spill — a cross join
has no key space to partition.

---

## 9. What is still open

- **Semi/anti `JoinWhere`**, which is what q21 actually needs, and with it the
  memory claim steps 38 and 39 made.
- **A loop join worth the name.** Step 13 declined a block-nested-loop and the
  argument stands, but "no equality means every pair" is now a user-reachable path
  rather than an internal one.
- The standing list: the parallel join's memory trade (28–47% faster, 30–45%
  heavier, still not a decision), CSV scanning (byte-range blocks or a SIMD
  delimiter scan, with a cheap `bytes.IndexByte` control to measure first), inline
  keys for `KeyTable`, the heap sampler being unconditional, `quantile`/`median`
  per-group storage, nested writing and `as_struct`, `.list` set operations,
  Map/Array, Pivot/Unpivot, SQL, cloud stores, join reordering.
