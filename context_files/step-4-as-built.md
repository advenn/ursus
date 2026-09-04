# Step 4 — as built

Joins: all seven equi-join kinds, `Validate` cardinality checking, and projection
pushdown through the join. Authoritative where it disagrees with
[`step-3-as-built.md`](./step-3-as-built.md) and the vision docs.

**All tests green** under `GOEXPERIMENT=simd` × `GODEBUG=simd={512,256,128,0}`,
with the experiment off, and under `-race`.

```go
enriched := orders.Join(customers,
    ursus.JoinOn(ursus.Col("customer_id")),
    ursus.JoinHow(ursus.JoinLeft),
    ursus.JoinValidate(ursus.ValidateManyToOne), // catches accidental fan-out
)
```

ursus can now combine two frames. No document named a "step 4"; against
`dataframe-features.md` §14 — the only version plan in the repo — exactly two v0.1
items were unbuilt, joins and in-memory parallel execution. This is the first.

---

## 1. Two live bugs found on the way, one of them shipped

**`data.NewNull` produces a column nothing can read.** It carries no payload
buffer — right for a value nobody looks at, wrong for anything a kernel then
gathers from. `kernel.Take` on one returns "no fixed-width payload" for a numeric
column and **panics** for a string one.

That was already reachable in shipped step-2 code:

```go
GroupBy(g).Agg(Col("v").Min())   // one group entirely null
// ursus: column "__agg0" (Float64) has no fixed-width payload
//   this is a bug in ursus; please report it
```

`assembleRows` placed a `data.NewNull` for a group that produced no value and then
concatenated it. Every existing Min/Max test had at least one non-null value in
the group, which is exactly why it survived. `kernel.NullColumn` is the fix and
the rule it establishes is: **`data.NewNull` is for a column that is the ANSWER;
`NullColumn` is for one that is an OPERAND.** Outer-join padding is an operand on
every unmatched row, so joins would have hit it immediately.

**`NewGroupKeyEncoder` hardcoded the op string `"group_by"`**, so a join on a List
key would have reported `cannot group by a List column` for an operation the query
does not contain. Now a parameter, with three call sites naming `group_by`,
`unique` and `join`.

## 2. The output schema is the whole difficulty

`dtype.NewSchema` rejects duplicate names, so joining two frames that both have an
`id` **cannot produce a schema at all** unless something renames. Suffixing is not
a convenience; it is what makes the join expressible.

Step 2's audit found `WithColumns.Schema()` and `resolveWithColumns()` had
implemented different semantics. A join has a worse version of that exposure,
because the physical operator needs more than the schema: it needs to know, per
output column, which side and which ordinal to gather from and which right column
to merge with. So `Join.Layout()` returns all of it — `Schema`, per-column
provenance, and the promoted key types — and `Join.Schema()`, `resolveJoin`,
`planJoin` and projection pushdown all go through that one function.

| Kind | Output |
| --- | --- |
| Semi, Anti | the **left schema, unchanged**. Filters: no right column, so no collision is possible. An equality, not an approximation. |
| Inner, Left | left ++ right-minus-merged-keys; the key appears once, from the left |
| Right | the key appears once, **from the right** — an unmatched right row has no left value |
| Full | **both key columns**, right one suffixed. A full join can leave the key null on either side, so no single side has it. |
| Cross | left ++ right, no keys; both `k` columns survive, right one suffixed |

A suffixed name that **also** collides is an error naming both. Never suffix
twice: `a_right_right` depends on iteration order and needs its own recursive
check.

### Two deliberate divergences from Polars

**Coalescing under the default requires the key names to MATCH.** Polars merges
regardless, so `LeftOn("cust_id"), RightOn("id")` silently deletes the right
frame's `id`. Coalescing exists to solve the duplicate-name problem; differently
named keys already appear exactly once, so there is nothing for a default to fix —
and dropping a column the user named explicitly breaks the standard "left join,
then test the right key for null" idiom. `JoinCoalesce(true)` still merges.

**A computed key does not appear in the output.** Polars materialises it, which
makes the output schema depend on the *shape* of the key expression and requires
inventing names for anonymous expressions.

### `CoalesceMode` is tri-state

Unset is not equal to on or off: it means "merge for Inner/Left/Right/Semi/Anti,
keep both for Full". A bool field would make a hand-built node's zero value mean
`Coalesce(false)`, which is not what the API produces for the same input — the
same reasoning that makes `Pushdown`'s zero value `Unsupported`.

## 3. The first two-child node

Every other node has exactly one child. The generic traversals needed **nothing**:
`Walk`, `TransformUp`, and both pushdown rules' default branches were already
written N-ary, and step 1 predicted this — *"`TransformUp`, `Expressions` and
projection pushdown's conservative default already handle unknown nodes correctly,
so they are correct-but-unoptimized the day they land."*

Three things would have failed silently:

- `Resolve`'s `default: return x, nil` passes a new node through **unresolved** —
  keys never expanded, never type-checked.
- `plan.Expressions`' `default: return nil` — and `plan_test.go` walks
  `Expressions` to assert every column read is projected, so without the arm that
  invariant test is blind to join keys.
- `Explain` rendered both children at the same indent with **no marker**, so
  `Join(A,B)` and `Join(B,A)` were indistinguishable — and for Left, Right, Semi
  and Anti those are different queries. Worse, `predicatePushdown` detects change
  by string-comparing Explain output, so a rewrite that swapped the sides would
  have reported "changed = false". Fixed with an optional `childLabeler`
  interface, so the seven single-child nodes render byte-identically.

## 4. Execution

**`breaker` holds one child and `Sink.Finish` returns an `Operator`** — a hash
table is neither. `ProbeBuilder` is `Sink` plus one method, and `joinBreaker` is
breaker's two-input sibling. `Finish` stays total rather than
`panic("use Probe")`, because it has a real meaning:

```
Finish(ctx)  ==  Probe(ctx, an operator that yields no rows)
```

which is zero rows for Inner/Left/Semi/Anti/Cross and every build row null-padded
for Right/Full.

**The right side always builds.** Not cost-based — nothing in the plan layer
carries a cardinality estimate, and a heuristic would be a fabricated number. More
importantly, `Right` is **not** `Left` with the inputs swapped: a swap would put
the *left* input on the build side, so `bigFacts.Join(smallDim, Right)` would
buffer the big table. A silent order-of-magnitude memory regression triggered by
changing one enum value. Instead the kinds reduce to flags: **Right is Inner plus
a build-unmatched flush; Full is Left plus the same flush.**

**CSR, not `map[string][]int32`.** A map-to-slice costs a slice header plus a
backing array per distinct key — at 5M keys, ~120 MB of headers and 5M pointers
for the GC to trace. Two flat pointer-free slices scattered once at freeze
instead, so a probe is one map lookup and then a contiguous slice already in
ascending build order.

**Semi and Anti keep no build rows at all.** They emit no right column, so their
build memory is O(distinct keys) rather than O(build rows), and they take a
`FilterBatch` mask path rather than a gather.

**Output is a sequence; `BatchSize` only chooses the cuts.** One probe row can
match thousands of build rows, so `(row, hit, entered)` is a resume cursor: a
probe row with 20 000 matches yields batches across many `Next` calls and never
allocates a 20 000-element selection. The join is stricter about `BatchSize` than
`sortSink` and `hashAggSink`, which return the whole result as one batch.

**The build-unmatched flush walks build rows in INPUT order**, not key-id order.
Two independent reasons: it gives right-input order, and Go map iteration is
randomised — so after a `Merge` a flush that walked ids would be nondeterministic
run to run.

**`Merge` is implemented for real**, unlike `hashAggSink`'s recorded gap. It can
be: the ids map is invertible and a join's per-key state is a row *count* rather
than accumulator state, so remapping is exact.

## 5. Null keys — the opposite rule from GroupBy

The group-key encoder makes `null == null` **true**, which is what GROUP BY wants
and the opposite of the join default. One `bitmap.AndMulti` fold per batch and one
boolean, not a second encoder.

| | `JoinNullsEqual(false)` — default | `(true)` |
| --- | --- | --- |
| build row with a null key | not inserted; marked `noKey` | inserted |
| probe row with a null key | `m = 0` **without a lookup** | ordinary lookup |

The trap it avoids needs no special case: forcing `m = 0` routes the row into the
existing "no match" branch, which is already *emit with nulls* for Left/Full,
*skip* for Inner/Semi/Cross, and *emit* for Anti. The naive implementation —
skip null-keyed probe rows entirely — is **correct for Inner and Semi** and
silently drops rows for Left, Full and Anti. Verified: reintroducing it gives 6
rows instead of 7 for a left join while every inner-join test still passes.

Worth stating together, because two hashing operations with different null rules
looks like a bug three months later: **group keys treat null as a group; join keys
do not match nulls.** Grouping asks "are these the same value?", joining asks "is
this the same entity?", and a missing identifier is not evidence of sameness.

## 6. What is optimized, and what deliberately is not

**Projection pushdown through the join: shipped.** It cannot change rows, and when
it is wrong the plan above stops resolving — loud. The win is concentrated: a semi
join against a wide dimension table reads only the key columns, visible in
`testdata/plans/join_semi.txt` as `projection: [k] (1/3 cols)`.

It needed one non-obvious clause. A right column is suffixed **only if the left
has that name**, so narrowing the left can *rename* an output column:

```
left {a,b} ⋈ right {b,c}  ->  a, b, b_right, c
Select(a, b_right)  =>  left drops "b"  =>  right's "b" is no longer suffixed
                    =>  the column is now "b" and the Select above fails
```

That is defect P2's exact signature. A left column that *causes* a suffix is kept
alive even when nothing reads it. Verified: removing the clause produces
`rule "projection_pushdown" produced a plan whose schema does not resolve`.

**Predicate pushdown through the join: deliberately NOT shipped.** The existing
fail-closed default already makes `Join` a barrier, so this is
correct-but-unoptimized. The rule is wrong in exactly the way that returns
different rows with no error:

| `p` reads | Inner | Left | Right | Full | Semi | Anti | Cross |
| --- | :-: | :-: | :-: | :-: | :-: | :-: | :-: |
| left only | → left | → left | **BARRIER** | **BARRIER** | → left | → left | → left |
| right only | → right | **BARRIER** | → right | **BARRIER** | n/a | n/a | → right |
| both sides | **BARRIER** | **BARRIER** | **BARRIER** | **BARRIER** | n/a | n/a | **BARRIER** |
| `Validate ≠ None` | **BARRIER** | **BARRIER** | **BARRIER** | **BARRIER** | **BARRIER** | **BARRIER** | n/a |

The Left/right-only cell is the trap: an unmatched left row carries all-null right
columns, `p(null)` is null, so the filter above drops it — but pushing `p` into the
right input deletes right rows, which turns previously-matched left rows into
unmatched ones that **reappear** null-padded, with the filter that would have
dropped them now gone.

The last row is not about rows at all: a pushed predicate can delete the duplicate
keys a `ValidateManyToOne` check exists to catch, turning a query that *must* error
into one that returns a plausible answer. No schema check can see that.

Classification would also have to go through the layout rather than the child
schemas — a suffixed name exists in neither child, and a coalesced key is both
sides at once — so the rule depends on brand-new layout code. Step 2 made the same
call for `Aggregate` and wrote down why. This is step 5's first item.

## 7. Honest gaps

- **No predicate pushdown through joins** (§6). The legality table above is the
  specification; the non-negotiables are layout-based classification, the
  `Validate` barrier, and a fixture where the join binds.
- **No as-of or inequality joins.** Both v0.2, both genuinely different operators.
- **`JoinCoalesce(true)` on a full join is refused** with `KindUnsupported`: the
  merged value is genuinely `coalesce(l, r)` and there is no coalesce or fill-null
  expression in ursus. The default keeps both keys, which is Polars' default too.
- **No join reordering and no cost model.** The seam is a `plan.Source.Rows()` plus
  a swap decision in `planJoin`; nothing below it would change.
- **`Validate` names the key COLUMNS and the row, not the offending value.** The
  only value renderer in the tree is in `ursustest` at level 80, and
  `internal/physical` is level 50. A `data.FormatValue` at level 20 would upgrade
  the message.
- **`MaintainJoinOrder` is not implemented.** The serial implementation emits in
  probe-side order for Inner/Left/Semi/Anti and appends the flush last for
  Right/Full; the option would document a guarantee rather than change behaviour,
  and step 3's rule was not to add a field nothing reads.
- **Peak memory at freeze is 2× the build side** for the duration of
  `kernel.Concat` — the same shape as `sortSink`'s ~3×. The seam is a
  `kernel.TakeMulti` that gathers across un-concatenated parts.
- **`Sink.Consume`'s "must not retain" doc is stricter than the code needs.**
  `sortSink` and `joinBuildSink` both retain, which is sound because
  `data.Column` is documented immutable and shareable. If a batch-reusing source
  ever lands, both break together.
- Still deferred from earlier steps: `exec.Collect` peaks at 2× the result;
  `hashAggSink.Merge` does not remap group ids; no SIMD accumulators; temporal
  types round-trip through neither IO format.

## 8. Verification

```bash
make test-all   # 4 SIMD widths + experiment-off
make race
make levels
go test ./... -update   # golden plans
```

The tests worth knowing about, because each pins something otherwise invisible:

| Test | What it would otherwise miss |
| --- | --- |
| `TestJoinRowCountsPerKind` | the fixture gives all seven kinds DIFFERENT counts (4/7/6/9/2/3/20), so every mis-wired flag changes one |
| `TestJoinNullKeyRowStillEmittedInLeftJoin` | rows silently dropped by an implementation that passes every inner-join test |
| `TestInnerJoinIsAFilteredCross` | the differential test: a cross join shares no code with the hash path, and `Eq`/`EqMissing` model the two null settings exactly |
| `TestJoinBatchSizeInvariance` | a resume cursor that cuts inside one probe row's match list |
| `TestSemiJoinDoesNotDuplicate` | a semi join returning one row per MATCH instead of per left row |
| `TestSemiAndAntiPartitionTheLeftFrame` | the null branch, from a second direction — the property holds even where `Filter(p)`/`Filter(Not(p))` does not |
| `TestJoinKeyTypesPromote` | an Int32/Int64 key pair encoding to different widths and matching **zero** rows |
| `TestJoinProjectionKeepsCollidingLeftColumns` | the P2 repeat: narrowing the left un-suffixes a right column |
| `TestJoinSchemaMatchesCollectedSchema` | any disagreement between what the plan predicts and what execution produces, across the whole option matrix |
| `TestJoinValidateCatchesFanOut` | a check that never fires — it asserts the error, the *absence* of an error on clean data, and the fan-out it exists to catch |
| `TestJoinKeysResolveAgainstTheirOwnSide` | the most likely two-child bug: an error listing the union of both frames' columns |
| `TestMinOverAnAllNullGroup` | the shipped step-2 bug in §1 |
