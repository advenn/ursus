# Step 51 — as built

**Why "tested" has not meant "correct", measured rather than asserted — and the
instruments that make the answer stick.**

This step began as a challenge rather than a feature request: *"you wrote a lot of
code, you claim they are tested, but still you are finding problems in them."* That
is accurate, and the measurement turned out worse than the anecdote.

Four commits: `2cfec82`, `e435bf3`, `a196d5d`, `ae1db5a`.

Authoritative where it disagrees with [`step-50-as-built.md`](./step-50-as-built.md),
the vision docs and [`design/`](./design/).

`make test-all` exit 0 (**80** package-ok lines = 16 packages × 5 SIMD
configurations), `make race` exit 0 (16), `make levels` and `go vet` clean in all
three modules — each asserted on its own exit code. **PDS-H SF=0.1 validates 22/22.**

---

## 1. Coverage is 82.6%, and it was irrelevant to almost every defect

Measured two ways, because the first way is a trap:

```
go test -cover ./...                 40.1%   <- credits each package only with ITS OWN tests
go test -coverpkg=./... ./...        82.6%   <- the real figure
```

The naive number badly understates, because 69% of the tests live in the root
package and exercise the internals without being credited. I nearly reported it.

The real figure is good, and it did not help:

| function | held | coverage |
| --- | --- | --- |
| `internal/expr/nodes.go` `Binary.Field` | the nullability lie, ~40 steps | **100.0%** |
| `internal/plan/rule_projection.go` `pushdown` | the missing arms | 79.3% |
| `internal/plan/rule_predicate.go` `substitutable` | the UDF duplication | 74.3% |
| `internal/kernel/unary.go` `rescaleTemporal` | the ignored `strict` | 66.7% |

**`Binary.Field` was at 100% coverage while it was wrong.** The line
`nullable := lf.Nullable || rf.Nullable` executed thousands of times, in hundreds of
tests, for forty steps. Nothing ever *asserted* the value it produced.

So the defects split into two modes needing different remedies. **Unreachable code**
— `reverseSink.Merge` at 0%, `joinBuildSink.Merge`, `bitmap.IsAllSet` — which
coverage *can* find and nobody looked for. And **covered but unasserted**, which
coverage can never find; only an assertion on the property that was wrong — the
schema, the plan shape, the error's kind — can.

---

## 2. The structural cause

**69% of the tests (453 of 654) live in the root package**, at the public API,
asserting the final answer. The layer beneath is nearly bare:

| package | tests | src lines | one test per |
| --- | --: | --: | --: |
| **root** (public API) | 453 | 5,868 | **12 lines** |
| `internal/kernel` | 65 | 8,687 | 133 lines |
| `internal/plan` | 19 | 6,658 | 350 lines |
| **`internal/physical`** | **16** | **9,313** | **582 lines** |

`internal/physical` is the largest package in the repository — the whole execution
engine — and it has sixteen test functions. **Tests are 48× denser at the public API
than in the engine**, and the engine is where `reverseSink.Merge` sat wrong for
thirty-four steps.

A thick end-to-end layer asserts *answers*. Anything that does not change the answer
passes straight through: code nothing calls, a plan that is worse but not wrong, a
schema that lies, an error with the right text and the wrong kind.

---

## 3. The taxonomy, and the category I did not predict

68 findings across steps 40–50, classified by what would have caught them:

| category | count |
| --- | --: |
| **a prose claim nothing checks** | **21** |
| assertion too weak, or a test that cannot reach what it names | 13 |
| performance-invisible | 11 |
| code nothing calls | 10 |
| right error, wrong kind | 6 |
| schema lie | 5 |
| right answer, wrong plan | 3 |
| phantom instrument | 2 |
| **silently wrong value** | **1** |

**The largest category is prose** — a comment declaring a property true, so nobody
wrote the test. Step 46's is emblematic: a `hashAggSink` comment said a window
inside `Agg()` was refused at plan time; it was not; the fix was one line and the
as-built's own summary is *"makes the comment true."* The code was changed to match
the comment, and nothing prevents the next one.

**Exactly one wrong answer in eleven steps.** Everything else was a plan, a schema,
an error kind, a comment or a clock. The engine computes correct results; what has
been unreliable is everything *around* the result.

### The systematic reason

**Every instrument here is a list, and a list goes quiet rather than failing.**
Pushdown arms covered 19 of 22 node types; golden plans 15 of 22;
`TestPushdownSoundness` 7 of 22; `CheckNonNullable` 9 of 15 packages; teeth cover
the current diff and nothing else. None *fails* when a node is added — they go
silent, and silence reads exactly like green.

Two fixes in eleven steps broke that pattern and are the template: `resolve.go`'s
**default arm**, which enforces the function's own documented postcondition and so
covers *"every node added later"* (coverage up, arm count 13 → 11); and
`TestPushdownSoundness`'s **anti-vacuity counter**, which fails when no assertion
ran.

---

## 4. What the instruments found, immediately

**`TestEvaluatorContract`** — the op × dtype matrix `eval.go` has claimed exists all
along; repo-wide the only occurrence of that name was the sentence promising it.
On its first run it found **42 real `Field`/`Eval` disagreements**: queries where
`CollectSchema` and `Explain` succeed and `Collect` fails. Three fixed:

- `Int64 * Uint64` promotes to Int128, where `arithI128` implements only add and
  sub. **Step 49's own comment in `op.go` asserted "resolveArithmetic refuses the
  rest before that." It did not.**
- `Duration / number` bound to a Duration output and reached a kernel with no
  integer `OpDiv`. Now refused, naming `FloorDiv` — this file's own rule for
  truncating division.
- `Duration / Duration` bound its operands as Duration against a Float64 output.
  **My first fix cast both to Float64 and made `1s / 1e9ns` return `1e-09`** — a
  wrong answer replacing a safe error. Reverted; mixed units are refused, same units
  exact.

The remaining ten are a **ratchet**, not an allow-list: each is asserted to still be
a gap, so fixing one fails the test until its entry goes.

**`TestEveryCitedTestExists`** reads every comment in the module. It found **seven**
phantoms where a manual grep found one — `TestEvaluatorContract` plus five doc
headers whose names had drifted from the functions beneath them, and one dead
cross-reference. Line-wrapped identifiers are handled precisely rather than by
weakening the check, because an instrument that cries wolf gets switched off.

**`TestEveryAggregateMergeIsCovered`** replaces a hand list whose own comment is
*verbatim* the anti-pattern: *"A new op MUST be added here — nothing enumerates them
automatically."* That list omitted `AggImplode`, `AggAny` and `AggAllTrue`, and
running over Float64 alone could not reach `extremumStr`, `extremumI128` or
`extremumBool` at all. `AggOp` is a `uint8` whose `String()` returns `"?"` for
anything undeclared, so the whole space walks with no list and no unexported access:
**42 op × family pairs, all fourteen accumulators.** All are correct — they were
unverified, not wrong.

**`TestEveryPlanNodeIsCovered`** parses `internal/plan` for `planNode()` receivers
and requires each to be reached by a checked-in query, matching by `reflect.Type`.
Label matching was the obvious design and is wrong three ways, each verified:
`MERGE SORTED [` contains `SORT`; `Distinct` emits two labels; `Scan`'s depends on
its source. Declaring a 23rd node fails the test the same day.

**`TestEverySinkMergeIsExercised`** — and **its own tooth found a flaw in it**. The
first version scanned every `_test.go` including the inventory's own, which names
every sink type and calls `Merge`, so it certified types by merely listing them;
deleting `reverseSink`'s tests came back green. The file is now excluded.

---

## 5. The blind tests

Eight assertions passed regardless of the behaviour they named. Two are worth
recording in full.

**`TestParallelAggregationIsDeterministic` was blind in two independent ways, and
fixing one was not enough.** It compared `got.String()` against `first.String()`,
and `String()` renders ten rows of a **97-group** fixture. Worse, the fixture could
not produce the condition at all: eight sinks over 4000 rows each see all 97 keys,
so `Merge` never inserts one and the visit order it is careful about cannot matter —
shuffling that loop changed no output. It takes **more keys than one sink can
hold**; at 2000 keys a shuffled order first differs at **row 352**, past the ten
`String()` renders. That is how the two defects hid behind each other.

**`ursustest` had 410 lines and no tests**, and it decides whether every other test
can fail. Its first tests found a live hole: under `WithTolerance` a Float32 column
was compared by **neither** path — `renderRows` drops every float column when a
tolerance is set, and `assertFloatsClose` read only float64. Two frames with wildly
different Float32 values compared equal. Latent, because no caller passed one — the
same shape as this option's other documented gap.

**Three of seven `String()` comparisons were fixed, not all seven.** `String()`
prints the true `shape: (N, M)` and a *"… N more rows"* line, so a row-count change
is always caught; only a *value* difference past row 10 is invisible, and four of the
seven have heights structurally pinned below it. Churn in a step about test quality
is the wrong signal.

**Five operators had no batch-size or thread sweep at all** — `JoinAsOf`,
`MergeSorted`, `GroupByDynamic`/`Rolling`, `TopK`, and `Distinct` (thread-swept,
never batch-swept). All five are invariant. `assertInvariant` carries an
anti-vacuity floor, because an operator returning nothing agrees with itself at
every batch size.

---

## 6. What my own reporting got wrong

- **"75 package-ok lines across five configurations"**, in nine consecutive commits,
  is **15 packages × 5 SIMD widths** — the same tests five times, catching one bug
  class (hardcoded vector lanes). Reported as breadth; it is depth on one axis. The
  honest phrasing is "N packages, five SIMD configurations", and it is 16 packages
  now that `ursustest` has tests.
- **Step 49 extended one of `NewBatch`'s five checks into `NewBatchRows`** and left
  four behind, using an argument — *"blind to a third of the engine and blind in
  exactly the newest operators"* — that applies verbatim to all five. All five run
  there now.
- **Coverage had never been measured** before this step.
- The first draft of my own boolean-aggregate test had **four of five subtests
  vacuous**, and the anti-vacuity guard caught them the moment it existed.

---

## 7. Teeth

| tooth | result |
| --- | --- |
| revert the Int128 guard | **bites** — exactly 24 disagreements resurface |
| leave a fixed gap in the ratchet | **bites** — "listed but now agrees" |
| delete a test a comment cites | **bites** |
| break `implodeAcc.Merge` | **bites** — the op the hand list omitted |
| declare a 23rd plan node | **bites** — required automatically |
| delete `reverseSink`'s Merge tests | **bites**, after the inventory stopped certifying its own file |
| rebuild `distinctOp`'s seen-set per batch | **bites** |
| shuffle `hashAggSink.Merge`'s key order | **bites** at row 352, with the 2000-key fixture |
| skip Float32 under `WithTolerance` again | **bites** |

---

## 8. Small corrections

`sortSink.Merge` did no memory accounting — the third instance of a hole fixed for
`joinBuildSink` at step 13 and `reverseSink` earlier in this step.
`JoinKind.String()`'s `default: return "INNER"` made an out-of-range kind render as
a plausible label; `ConcatMode.String()` documents that hazard for eight other enums
and this was the unfixed instance. `Join.CoalesceSide` is **deleted** — not merely
uncalled, but a second copy of logic `physical/join.go` implements inline, which can
drift. Two comments claimed `arrowx.IsAligned` is what a kernel asks; no kernel
asks.

---

## 9. What is still open

- **The ratchet's ten entries** — four Date arithmetic, six `CanCast`/kernel.
- **28 weak `errors.Is` accept-assertions** and 7 refusal tests asserting only
  `err != nil`. Mechanical; they route through `assertUserError`, which step 48
  wrote and never generalised.
- **Performance regressions remain unguardable** by assertion here — the ~30% noise
  floor means the only instrument is a re-run, and the suite is seventeen commits
  stale on a machine that cannot run it.
- **The 21 prose claims** — the largest category, with no mechanical instrument.
  `TestEveryCitedTestExists` closes only the narrow, checkable sub-case.
- `internal/spill`'s `Reader.wrap` at 0.0% with five call sites: every IO-error and
  truncated-file path in the spill reader.
- The standing list: `Str().Join()`, `.list` set operations, `AsOfJoin`/
  `MergeSorted`/`HStack` pushdown arms, the UDF follow-ons and a serial opt-out,
  the nullability fast path, the parallel join build, spilling ⊥ parallelism,
  CSV range-splitting, `Expr`-level selection, `Pivot`, SQL, cloud stores.
