# Step 54 — as built

**The last three projection-pushdown arms.** `AsOfJoin`, `MergeSorted` and `HStack`
read every column of their input for six steps. All three prune now, and every plan
node type declared in `internal/plan` has an arm — the rule's conservative default is
reachable only by a node type that does not yet exist.

Two commits, plus one that shipped first because a survey for this step found
something that outranked it.

Authoritative where it disagrees with [`step-53-as-built.md`](./step-53-as-built.md),
the vision docs and [`design/`](./design/).

`make test-all` exit 0 (85 package-ok lines = 17 packages × 5 SIMD configurations),
`make race` exit 0 (17), `make levels` and `go vet` clean in all three modules —
each asserted on its own exit code. **PDS-H SF=0.1 validates 22/22.**

---

## 0. A silent wrong answer, found while surveying

Float `FloorDiv` did `dst[i] = T(int64(dst[i]))`. A float-to-integer conversion in Go
is **implementation-defined** when the value does not fit, so four results were wrong
and none of them is a corner:

```
-7.0 // 2.0    ->  -3        (want -4 — truncation, in an operation named FloorDiv)
 1.0 // 0.0    ->  -9.22e18  (want +Inf)
 0.0 // 0.0    ->  -9.22e18  (want NaN)
1e300 // 1.0   ->  -9.22e18  (want 1e300)
```

It also broke the totality the `OpDiv` arm immediately above deliberately
establishes: *"IEEE division never traps, so x/0 is ±Inf and stays a value rather
than becoming a null."* Flooring a total operation must stay total, and `math.Floor`
is — `Floor(±Inf)` is `±Inf`, `Floor(NaN)` is `NaN`, and a magnitude past int64's
range is already an integer.

**`TestFloatModAndFloorDiv` named this behaviour and asserted nothing about it.**
`"fdiv"` appeared exactly once in the file — on the line that computed it, including
the row where the divisor is zero. Shipped as `e52d104` before the planned work.

---

## 1. The instrument for this step was blind, and that is the first commit

Step 51 added goldens for all three node types. Every fixture selected everything, so
`projectionPushdown` seeded `required` with the root's whole schema, every scan read
`(N/N cols)`, and **landing all three arms would have changed zero bytes**.

That is the failure mode step 51 was written to attack, in an instrument step 51
built: the fixture, not the test, is the unit of coverage — the sixth recorded
instance.

So the fixtures were widened **first, in their own commit**, each gaining an
unselected column and a `Select` above the node. The arms then moved all six
projections:

```
asof_join     [k, lv, lx] (3/3)  ->  [k, lv] (2/3)
              [k, rv, rx] (3/3)  ->  [k, rv] (2/3)
merge_sorted  [k, v, x]   (3/3)  ->  [k, v]  (2/3)   both sides
hstack        [k, ka, kb] (3/3)  ->  [k, ka] (2/3)
              [hv, ha, hb](3/3)  ->  [hv, ha](2/3)
```

A golden that cannot move is not evidence. Committing the "before" state separately
is what makes the diff the evidence rather than the claim.

---

## 2. The refactor is mandatory, not tidiness

`pushdownJoin` ends in `j.WithChildren(...)`, which returns a **`*Join`**. Reusing it
for `AsOfJoin` through `asJoin()` — which is the obvious move, since
`AsOfJoin.Layout()` already delegates there — would have silently replaced the
as-of join with an equi-join: nearest-match semantics gone, **schema identical**, and
not one existing test would have noticed.

So the analysis is split from the rebuild. `joinNeeds` computes the two sides;
`pushIntoPair` descends and rebuilds through the node's **own** `WithChildren`. Each
arm keeps its own type.

`TestAsOfJoinStaysAnAsOfJoin` is the test that would have caught it, and it has to
assert **values**, not the schema: key 10 has no exact match, so backward as-of takes
key 8's value while an equi-join would leave it null. Same shape, same column count,
different answer.

---

## 3. `MergeSorted` validates itself, because the backstop is not where I thought

Its `Schema()` requires both inputs `Equal` **exactly**. So this is the one arm where
pruning can break a query that worked: push the same set into both children and one
may prune while the other cannot, because its subtree holds a node whose own arm must
keep everything. The result is `[k,v]` against `[k,v,x]` with resolution already
succeeded — defect P2's signature.

The arm therefore re-derives both children's schemas and **falls back to the
conservative descent** unless they still agree — falling back rather than returning
the node untouched, so pruning a `Project` deeper in either subtree is still earned.

**And the backstop I had planned to lean on is not there.** `Optimizer.Verify` does
catch it by rule name, but it is **off** where it would be seen first:
`inventory_test.go` uses a bare `plan.NewOptimizer()`, and `Explain` only resolves
schemas when `opts.Schema` is set, which it is not by default. **A broken MergeSorted
plan renders a clean golden file, and `go test -update` would check it in.**

Worse, the user-visible failure is not an internal error. It surfaces as a
**`KindSchema`** *"the two frames have different schemas"* with the hint *"use Concat
for frames that only need to be compatible"* — the optimizer desynchronising the
user's matching frames and then blaming the user. That is the shape `assertUserError`
exists to detect, inverted.

The key is added to the required set explicitly rather than left to
`Expressions(*MergeSorted)`, so the arm does not depend on a liveness arm to keep the
thing its own `Schema()` reads.

---

## 4. `HStack` reads a child for its height

Children own disjoint names — `HStack.Schema()` refuses a collision — so the split is
by membership in each child's schema. Soundness is the `RowIndex` argument verbatim:
pruning only removes names, so a duplicate-name refusal can only become less likely.

The subtlety is the height. HStack pairs rows **positionally**, and `hstackOp` derives
the output height from its children and errors when they disagree. So a child must be
read even when no column of it survives — the arm requires its **first** column,
deterministically, rather than none.

**Without that it is not a crash but a lie.** An empty need set makes `pushdownScan`
produce a `nil` projection, and `nil` means *"every column"* — so the prune silently
becomes a no-op that still reports `changed = true`. The rows are right either way,
which is exactly why that test asserts the **plan** and not only the differential.
The tooth shows it: `projection: * (3 cols)`.

Teaching the sources and `trimOp` to honour an empty projection and carry the height
through `NewBatchRows` is the principled alternative. It touches every source and is
its own step.

---

## 5. A landmine in the test that guards this rule

`TestPushdownSoundness` collects its `needed` set **globally across the whole tree**
and then asserts it against *every* scan whose schema contains the name. That is
sound only while no plan in its list has two children — which was true until this
step.

Two sides built from the same fixture would make it demand that the **right** scan
project left-only columns, so a **correct arm would fail**. It needed a
name-disjoint second source, and a third sharing no name at all for `HStack`, where
even the key would collide.

---

## 6. Teeth

| tooth | result |
| --- | --- |
| `MergeSorted`: the naive arm, no re-check | **bites** |
| `MergeSorted`: drop the key from the required set | **bites** — `unknown column "id"` |
| `HStack`: drop the keep-first-column rule | **bites** — `projection: * (3 cols)`, the no-op reported as a change |
| revert a fixture to select everything | **bites** — `(3/3 cols)` returns, which is this step's opening finding |
| `AsOfJoin`: remove the key retention | **bites**, once aimed correctly — §7 |
| float `FloorDiv`: restore the int64 round trip | **bites** — and exposed a fourth defect I had not listed |

---

## 7. Two teeth that had to be re-aimed, and what that showed

**The as-of key tooth was silent at first.** Removing only the `LeftOn` loop changed
nothing, because the **naming-stability clause** compensated: it re-adds to the left
any right-side name the left schema also has, and the key is on both sides. Removing
both loops bites decisively, and trips `Optimizer.Verify` as well. A correct
mechanism masking the one under test is the same shape as a fixture that cannot reach
its target.

**The `FloorDiv` tooth found a defect I had not listed.** I had three cases; reverting
the fix surfaced `-7 // 2 = -3` — truncation toward zero in an operation named for
flooring, and the most ordinary input of the four.

---

## 8. What is still open

- **Byte-level spill corruption, now verified rather than predicted.** A one-byte
  flip of `ncols` panics through `Schema.Field`'s bare index; `ncols == 0` returns a
  valid-looking batch with N schema fields and **zero columns**, detonating later
  inside `kernel.Concat`; a corrupt `rows` reaches `NewFixedBuffer`,
  `NewStringBuffers` and `bitmap.NewView` with no cross-check. The validity flag is
  the only tag-like field with no unknown-value arm.
- **`ResolveUnary` admits `Null` for `Not` and the four NaN predicates, and no kernel
  handles it.** `Not` on a null column yields a **zero-row** column from an N-row
  input, reported to the user as an ursus bug. `contractBatch` has no `Null` column,
  which is the only reason `TestEvaluatorContract` misses it — its row-count
  assertion is already written.
- **UDF name uniqueness is documented as load-bearing and never enforced.** `checkUDF`
  rejects only an empty name; two closures sharing one still collapse in
  `extractAggs`' `byKey`.
- **`Optimizer.Verify` is off in the golden inventory and in `Explain`** — §3. Any
  rule that breaks a schema renders a clean golden.
- The conservative default in `rule_projection.go` is now unreachable for every
  declared node type. Its comment should say that its purpose is nodes added later,
  or a reader will mistake it for dead code.
- The standing list: 32 weak `errors.Is` accept-assertions, `List().Join()` and
  `Str().Join()`, the UDF serial opt-out, `.list` set operations, `Time + Duration`
  overflowing 24h, `callCache`'s missing eviction, `Writer.Write`'s unwrapped error,
  `Expr`-level selection, `Pivot`, and a benchmark suite twenty-two commits stale on
  a machine that cannot run it.
