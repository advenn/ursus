# Step 70 — as built

**The optimizer must not change the answer.** This is item 1 of `audit.md` §11: O1,
O2 and O8, plus the instruments that would have caught them. The instruments found
one more defect, P1, and measuring the step first found a second, W1. Both are
recorded and neither is fixed.

Six commits and this document.

Authoritative where it disagrees with the vision docs and [`design/`](./design/).

`make test-all` exit 0 (**100** package-ok lines = 20 packages × 5 SIMD configurations),
`make race` exit 0 (20), `make levels` and `go vet` clean in all three modules, PDS-H
SF=0.1 exit 0 with **22/22** matching the duckdb reference — each asserted on its own
exit code. No benchmarks. **The 32 golden plans did not move**, and they are now
checked under Verify. The suite is at **2422** tests.

---

## 1. The defect the on/off comparison could never see

The first action was a probe the design review had asked for: a window over a column
defined earlier in the same `WithColumns`.

```go
WithColumns(Col("x").Mul(10).Alias("w"), Col("w").Sum().Over(Col("g")).Alias("s"))
// s = 200, 200, 100 — the input's w. The answer is 30, 30, 30.
```

It is wrong **with the optimizer on and off alike**. Windows are lifted below the
whole node, so the window reads the input's `w`. With a new name, `w2`, it is refused
with `unknown column`.

This is why the evidence commit compares both runs against a **hand-computed**
answer, not against each other. An on/off comparison agrees with itself here, and it
agrees in every defect that lives in the resolver. W1 is recorded in `audit.md` and
listed in `knownOptimizerDefects`. It is outside the scope chosen for this step.

## 2. A differential that is generated, not hand-picked

`TestPushdownSoundness` described itself as *"the ONLY mechanism that can catch a bad
predicate push"*. It compares twelve hand-picked queries, and none of them is a
chained `WithColumns`, which is exactly the shape O1 needs.

`optimizer_differential_test.go` composes the shapes instead:

- **31 transforms**, each run singly. (The commit message said 30.)
- **12 of them in every ordered pair**, and five named triples.
- **"Tops" on every shape**, derived from the shape's own unoptimized result:
  - a filter at each column's **median**;
  - one filter built to reach a finding per type — `c*c` for O6, `1/c` for O9, a
    cast for O8;
  - a Select of each column, `Head`, `Len`, and the bare shape.
- **1629 queries in about 0.23 s.**

**The median is the point.** A fixed threshold keeps every row whichever way the
predicate is evaluated, and so it hides O1. The fixture is eight rows, and each column
is placed to reach a finding: −0 next to +0, NaN, a null key, a value that overflows
Int32 when squared, and a `"x"` on the row the guard removes.

It started at **266 mismatches**, and every one was attributed to a defect:

| class | at start | now |
| --- | ---: | ---: |
| O1 | 64 | **0** |
| O2 | 29 | **0** |
| O8 | 32 | **0** |
| O8b | 4 | 2 |
| P1 | 106 | 132 |
| O4 | 19 | 19 |
| O5 | 9 | 9 |
| O6 | 1 | 1 |
| O8-join | 1 | 1 |
| O9 | 1 | 1 |
| **total** | **266** | **165** |

### The ratchet is counted, not listed

The first design listed every mismatching query by name pattern. That broke on
contact: one shape can carry two defects, so no name pattern could separate them.

The ratchet counts per defect instead. `classify` attributes each mismatch from three
things:

- the rules that reproduce it alone, found by switching on one `plan.Flags` field at
  a time;
- its symptom;
- its transforms.

The attribution does not have to be subtle to be safe. The counts are **exact**, so a
new defect landing in an old class changes that class's count, and an unattributed
mismatch fails outright. A wrong attribution can blur the per-defect numbers above;
it cannot hide a mismatch.

`AssertFrameEqual` calls `Fatalf` and relies on it not returning. So the harness runs
it under a `recorder`, a `testing.TB` that records failures and turns `Fatal` into a
recoverable panic. The recorder and the judge each have a test of their own.

## 3. It found P1

**Projection pushdown stops reading a column that a `WithColumns` redefines without
reading it, and the redefinition then lands at the end instead of in place.**

```go
Frame(x, w, y).WithColumns(Col("x").Add(1).Alias("w"))
// Collect: {x, y, w}.  CollectSchema: {x, w, y}.  The doc: "REPLACED IN PLACE".
```

It is silent unless `WithVerify` is on, and it is exactly the kind of redefinition
people write. Measured: `WithColumns(Lit(0).Alias("n"))` on `{x, n, y}` returns
`{x, y, n}`.

**My first by-hand case for it answered correctly.** It redefined the *last* column,
where moving the column to the end changes nothing. The case redefines the middle one
now.

## 4. The fixes

### O1 — compose each definition through the ones before it

`pushThroughProjection` recorded each `WithColumns` definition as written. A
`WithColumns` is sequential, so a definition that read an earlier-defined column was
substituted as if it read the input's column. The fix composes each definition as it
is recorded, and three details decide whether that is right:

- **The composition happens in the walk, not in a second pass.** In
  `y+1 as z, x*2 as y`, `z` was defined before `y` and reads the input's `y`. The
  second-pass tooth fails `TestPushdownSoundness` as well as both new tests.
- **A definition that cannot be composed makes its name opaque.** That is one holding
  a UDF, which must never be duplicated into a predicate. **The opaque check comes
  before the fallback to the input's column.** In `udf(x) as x, x+1 as y` the input
  also has an `x`, and reading it would be the same bug. The ordering tooth fails
  exactly the two UDF cases.
- **A Project is left alone.** It is parallel. Composing it too fails the "select is
  parallel" control, and only that.

This also fixed a UDF form of O1 that was being pushed down: in
`udf(x) as w, w+1 as v`, the `w+1` holds no UDF, so it was substituted and read the
input's `w`.

**Fixing O1 uncovered 26 more P1 shapes.** O1's wrong substitution made the pushed
filter read the input's `n`, which kept projection pushdown reading it, which hid P1.
With the filter now reading the composed definition, the input's `n` is no longer
read, and P1 shows. P1 rose from 106 to 132 without anything new being broken.

### O2 — every Concat child is narrowed to the same columns

A child keeps what it needs as well as what it is asked for. A Filter keeps its
predicate's column, and `resolveUnion`'s adaptation Project keeps every expression it
has. So the children came back at different widths:

- the strict union refused the user's own matching frames;
- the diagonal union reconciled the mismatch, verified, and then raised `ErrInternal`
  at the physical union.

`pushdownUnion` fixes the target from the union's own schema. It wraps a child in a
Project **only when the children disagree**, which is why the goldens did not move.

The empty-target guard covers a parent that reads no column, such as `Len`. **Its
tooth was silent**, and it was right to be. A zero-column batch keeps its row count,
so the rows come out right without the guard. What the guard prevents is a scan
reading every column and both children being wrapped in `PROJECT []`. The rows cannot
see that; the plan can, so `TestConcatUnderLenReadsOneColumn` asserts the plan.

Every O2 repro had the narrowest child first. A fix that took its target from the
first child would have passed all of them, so a case now puts the wide child first.

The comment at `resolve_union.go:23-24` claimed pushdown *"narrows each child
independently through an ordinary Project, with no Union-specific rule needed"*. Both
halves were false.

### O8 — stacked filters keep their order

This is one line: the inner filter's predicates go first. The physical filter
evaluates each conjunct only on the rows the ones before it kept.

That fixed all 32 O8 shapes, and 2 of the 4 O8b ones. Those two had the guard
**below** the `Unique`, where merging the filters was the whole problem. The remaining
two, **O8b**, have the guard above it: the cast is pushed beneath the Distinct while
the guard stays. That needs a rule about which conjuncts can fail, not an ordering.

### Verify — on in Explain, and checking what the root schema cannot show

`Explain` ran without Verify, and the golden plans are produced by `Explain`. So all
32 goldens were produced and checked with the check off. `Explain` and the golden
inventory verify now. All 32 still pass, so none was hiding a broken plan.

Verify had compared the root schema and nothing else, and two of this step's defects
leave the root schema intact:

- **Filter predicates.** `Filter.Schema()` never looks at a predicate. So O1's refusal
  form verified cleanly and failed at `Collect` with `unknown column`. Every Filter's
  predicates must now resolve, to a Boolean, against the Filter's own input.
- **Union children.** A diagonal union takes the union of its children's columns, so
  a child narrowed below its siblings still reconciled (O2's diagonal form). Every
  child must now produce the union's columns, by name and type, in position.

Verify's error paths had no test. They have six now, including two sound controls.

**The `Explain`-without-Verify tooth is silent, and says so.** Once O2 is fixed, no
public query produces an inconsistent plan for `Explain` to render. It is the backstop
for the next broken rule, and there is no honest way to test a backstop against a
defect that does not exist yet.

## 5. Teeth

| reintroduce | fails |
| --- | --- |
| O1: the definition stored verbatim | both differentials |
| O1: opaque checked after the input fallback | O1 udf chain, O1 udf in place |
| O1: the uncomposed definition kept on refusal | both differentials |
| O1: composing in the Project branch too | O1 control: select is parallel |
| O1: composing in a second pass | both, and `TestPushdownSoundness` |
| O2: no wrapper | all five O2 cases, the differential |
| O2: the target taken from the first child | O2 wide child first |
| O2: no empty-target guard | `TestConcatUnderLenReadsOneColumn` — **silent** until it existed |
| O8: the outer filter first | O8 guard, the differential |
| Verify: the filter check, the Boolean check, the union check, the call itself | `TestVerifyCatchesWhatTheRootSchemaCannotShow` |
| Verify off in `Explain` | **silent**, as expected (§4) |

Every patch was checked to have applied, and every result was read against a green
baseline.

**One run was not.** The first O2 teeth ran while the ratchet edit had failed to
apply: gofmt had realigned the map, so my exact-text replacement matched nothing. The
baseline was therefore already red, and three "bites" meant nothing. It was caught
because the suite was red before the teeth; the edit was redone by pattern and the
teeth rerun. The lesson is the one steps 65 and 66 recorded, with a new twist: a tooth
needs a green baseline as well as a patch that lands.

## 6. Mistakes, all mine

- **My first O2 widening and diagonal repros did not reproduce**, so they were not
  listed as defects. Widening needs the Int64 child first, and diagonal needs both
  frames at full width. Both were found by probing shapes, not by argument.
- **The first P1 case redefined the last column** (§3).
- **The O2 teeth were run on a red baseline once** (§5).
- **The generated test's commit message says 30 transforms. There are 31.**

## 7. Still open

The differential still counts these, and step 71 should empty them:

- **P1**, 132 shapes: the column-order defect. Silent without Verify.
- **O4** (19) and **O5** (9): the cross-join collapse's NaN and null-key cases. O4
  needs a decision about which equality a rewrite may use.
- **O6** (1): a widened join key filtered at the narrow width.
- **O8b** (2) and **O8-join** (1): a fallible conjunct overtaking a guard.
- **O9** (1): −0 under `Unique`.

Outside the differential's reach, because each is wrong in both modes or is not a
row difference:

- **W1**, the window in `WithColumns`.
- O3, literal identity.
- O10, O11 and O12.

The rest of `audit.md` §11 is unchanged: multi-file scans and Parquet pruning (I1–I7)
next, then literal identity, recovering panics in worker goroutines, and the float64
go-between. Carried as before: README debt; nested write; object stores;
`unique`/`over` spill; the planner leak; `spill.Writer.Write`'s bare error.
