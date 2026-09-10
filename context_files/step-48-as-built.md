# Step 48 — as built

**Two arms the reshaping nodes never had.** A resolve arm, so a typo is a typo
rather than an ursus bug; and projection-pushdown arms, so `Explode`, `Unnest` and
`Unpivot` stop reading every column of their input.

The approved plan named two nodes for the first debt and prescribed four hand-written
arms. Both were wrong: **the defect is four nodes**, and the right fix is **one line**
in `Resolve`'s default arm. And **four checked-in tests were passing against the
broken message**, which is why it survived from step 30.

Authoritative where it disagrees with [`step-47-as-built.md`](./step-47-as-built.md),
the vision docs and [`design/`](./design/).

`make test-all` exit 0 (75 package-ok lines across five configurations),
`make race` exit 0 (15), `make levels` and `go vet` clean in all three modules —
each asserted on its own exit code. **PDS-H SF=0.1 validates 22/22.**

---

## 1. One line, not four arms

Several nodes put their entire validation in `Schema()`. With no resolve arm, nothing
calls `Schema()` during `Resolve`, so the **first** caller is a rule inside
`Optimizer.Run` — which wraps every rule failure as `KindInternal`, whose own doc
reads *"a bug in ursus. Users should never legitimately see one."*

```
ursus: optimize: rule "projection_pushdown" failed
  caused by: ursus: unnest: unknown column "nope"
```

`resolveJoin` solved this once and says so — *"rather than at the first Schema() call
inside Optimizer.Run … the exact maximally-confusing message defect P2 produced for a
perfectly valid query"* — and step 47 copied it into a four-line `resolveUnpivot`.

Copying it four more times was the plan. The better fix is to make `Resolve` enforce
the postcondition **its own doc already claims** — *"The result is a plan on which
Schema() is total and cheap"*:

```go
default:
    if _, err := x.Schema(); err != nil {
        return nil, err
    }
    return x, nil
```

It covers `Explode`, `Unnest`, `RowIndex` and `HStack`; it covers every node added
later; and it **retired `resolveUnpivot` and `resolveMergeSorted`**, which were
nothing but that call. Thirteen arms became eleven and the coverage went up.

The cost, stated: `Schema()` recurses into its input, so calling it at every node of a
bottom-up walk is quadratic in depth. Every existing arm already does exactly that, so
this is a constant factor on a shape that was already there, and plans are tens of
nodes deep.

**`Window` is a false positive** and is named here so nobody adds an arm for it: it is
synthesised inside `resolveProject`/`resolveWithColumns`, which have already resolved
the same expressions against the same schema.

---

## 2. Why it survived, and why the suite said it was fine

**The defect was intermittent.** Every arm-bearing node returns its input's
`Schema()` error *unwrapped*, so anything with an arm above the broken node masked
it. `Scan.Unnest("nope").Select(...)` was always clean. `Scan.Unnest("nope")` and
`Scan.Unnest("nope").Head(5)` were not — `Limit` has no arm either.

**And four checked-in tests were false negatives.** They asserted
`errors.Is(err, uerr.ErrSchema)` plus a substring, and both survived the wrapping:
`(*uerr.Error).Is` compares only its own kind, `errors.Is` then walks the cause chain
and finds the good kind underneath, and the substring was on the `caused by:` line.

So the only thing that separates the two forms is the **top-level kind**. The four
sites — `explode_test.go`, `unnest_test.go`, `frameops_test.go`, `concat_test.go` —
now assert `!errors.Is(err, uerr.ErrInternal)`, and `assertUserError` adds the other
half: **`Explain` must fail too**, which is the discipline `Expr.DropNulls` states —
*"A refusal that only Collect notices … lets Explain print a plan that cannot run."*

---

## 3. Projection pushdown, and where the join's shape is wrong

All three nodes fell to the rule's fail-safe default — *"An unrecognised node type
requires ALL of its input's columns … correct-but-unoptimized the day they are
added"* — so **any `Select` above an `Explode` or `Unnest` was annihilated**, which is
the normal shape for both: one list column of a wide frame, then a few columns out.

The evidence is a checked-in golden, `4/4` → `3/4`:

```
 UNPIVOT jan, feb index id into month/sales
   MEMORY SCAN 1 rows
-    projection: [id, region, jan, feb] (4/4 cols)
+    projection: [id, jan, feb] (3/4 cols)
```

**`Explode`** passes the parent's set through and adds its own columns
unconditionally — they set the row count.

**`Unnest`** is the one with a translation: the parent asks for **field** names, which
exist in no input schema, so `required` is filtered against the input and the structs
added back. Passing the field names down asks the scan for columns it never had.

**`Unpivot` is where `pushdownJoin`'s shape would be a defect.** That arm iterates the
layout and keeps what the parent asked for. Doing that here with an explicit `Index`
prunes an index column nobody selected — and `IndexColumns` returns the named list
verbatim, so `Schema()` then fails with `unknown index column`. Resolution succeeds
and then the schema does not: **defect P2's signature, one node over.** An explicit
`Index` is required in full, because the node *names* those columns and therefore
reads them.

The implicit case is safe, and step 47 (mine) overstated it as a rename hazard. It is
not a rename; it is a removal driven by the very set that says what to keep, and every
one of `Schema()`'s five failure modes is **monotone under pruning** — the unknown-
index check becomes vacuous, `On` is untouched, `seen` only shrinks, and a duplicate
name can only disappear. That last line is the argument the `RowIndex` arm already
makes. The proof is in the arm's doc comment.

---

## 4. A prerequisite that would have made this a defect

`Expressions()` had **no arm for `Explode` or `Unnest`** — their `Columns` are string
slices, exactly the case `Distinct.Subset`, `MergeSorted` and `Unpivot` each have an
arm for. `node.go` states the rule: *"the liveness check silently stops seeing under
that node. That is not hypothetical: `*Window` went four steps without one."*

It cost nothing from steps 30 and 35 until now **only because neither node had a
pushdown arm either**, so the fail-safe default kept every column alive. Teaching one
to prune without the other is what would have turned a latent gap into a wrong answer.

`TestPushdownSoundness` walks a **fixed list of plans** with none of the three in it,
so it could not have caught this. It has one of each now — and its own anti-vacuity
counter is what catches the missing arm: *"no column assertions ran — the case has
gone vacuous."*

---

## 5. Teeth

| tooth | result |
| --- | --- |
| `pushdownUnnest` passes field names straight down | **bites** |
| `pushdownUnpivot` prunes an explicit `Index` column | **bites** |
| remove the default-arm schema check | **bites** — back to `rule "…" failed` |
| drop the `Expressions` arm for `Explode`/`Unnest` | **bites**, via the safety test's vacuity counter |
| `pushdownExplode` drops its own columns | **bites**, but only after the fixture was fixed twice — §6 |

---

## 6. The fixture that had to be wrong twice

The explode tooth needed two independent properties before it could fire, and the
first fixture had neither.

**The exploded column must not be selected.** If the parent names it, it is already in
`required` and dropping it from the arm's set changes nothing.

**And it must come straight from the scan.** With a `WithColumns` in between — the
natural way to build a list in memory now that `Split` exists — the column is
recomputed regardless: that arm prunes pass-through columns but never its own
expressions.

With both, the tooth bites. The case it protects is worth stating plainly: **explode
drives the row count, so pruning the exploded column changes how many rows come back**
— a fixture whose parent happens to select it cannot see that at all.

---

## 7. The nullability question, answered

Step 47 recorded that nothing validates a declared nullability and left it looking
like a lurking defect. I went to build the checker and found the answer instead.

**`Field.Nullable` is a permission, not a fact**, and says so: *"a static property of
the schema, not a count of nulls actually present: a non-nullable field is one where
the engine may skip validity handling entirely."*

**Nothing skips anything.** No kernel reads it; nor does the evaluator. Kernels branch
on the data (`c.NullCount() == 0`), never on the schema. The Parquet writer ignores it
deliberately — *"Every column is written OPTIONAL … ursus's nullability is a property
of the batch in hand rather than a guarantee about the data."* Every other consumer —
join layout, union reconciliation, `Cond`/`Binary` type resolution, the spill format —
is schema bookkeeping.

So a false `Nullable: false` is a **schema lie, not a wrong-answer lie**: visible in
`CollectSchema` and `Explain`, and nowhere else.

**Two real lies exist and are worth their own step**, both found by reading rather
than by running:

- **`Binary.Field` has no notion of the kernel's Total/Partial split.** It declares
  `nullable = lf.Nullable || rf.Nullable`, but integer `FloorDiv` and `Mod` are
  *Partial* kernels that manufacture a null on a zero divisor. `TestFloatModAndFloorDiv`
  already asserts a null in a column the schema calls `Int64!`, and passes today under
  `WithVerify()`.
- **`rescaleTemporal` produces nulls without consulting `strict`**, so a strict
  widening cast past ~year 2262 declares non-nullable and holds nulls.

Both are contained and neither corrupts data today. The reason to fix them is the
sentence the flag's own doc ends with: it exists so the engine *may* skip validity
handling. The lies become load-bearing the moment anyone writes the first fast path.

`bitmap.View.IsAllSet()` — O(1), and **currently dead code with zero callers** — is
the primitive such a checker would start from. Its doc already describes the shape.

---

## 8. Verification

The evidence is the **plan and the error text**, not the clock, which is what makes
this a step this machine can do honestly: load average 6.5, 3 GB free against 11 GB of
swap.

- Nine refusal cases, each asserting the substring, that `Explain` fails, that the
  kind is not Internal, and that the message contains no `rule "…"`.
- Both masking shapes: clean under a `Select`, broken under a bare node and a `Limit`.
- Pruning proved by the scan's own `projection:` line, plus a rule-on/rule-off
  differential on every one — *a pushdown that errors is loud; one that changes rows
  is not*.
- `Unnest` selecting one field of a struct; `Unpivot` with an explicit `Index` nobody
  selects; `Unpivot` as the root, where the implicit case is protected only by
  `Apply` seeding *"everything it currently produces"*.
- `TestPushdownSoundness` extended with one plan per new arm.

---

## 9. What is still open

- **The two nullability lies** — §7. Contained, named, and their own step.
- **`Pivot`** — step 47's verdict stands.
- **`AsOfJoin`, `MergeSorted` and `HStack` have no pushdown arm** either, so they read
  everything. Same shape as the three fixed here, and none of them is a reshaping
  node, which is why they were out of scope.
- The standing list: the parallel join build (`joinBuildSink.Merge`, implemented and
  never called since step 10), spilling and parallelism being mutually exclusive in
  `aggWorkers`, CSV scanning, a suite re-run (ten commits unpublished, and the machine
  has been unfit for six of them), the `.list` set operations, `Str().Join()`,
  `Expr`-level selection, inline keys for `KeyTable`, the heap sampler,
  `quantile`/`median` storage, nested writing, Map/Array, SQL, cloud stores, join
  reordering.
