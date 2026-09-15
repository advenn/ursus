# Step 56 — as built

**The call surface, enumerated.** `internal/expr` declares **62 call functions**
across six namespaces, and **no test file in the repository referenced a single
`CallFn` constant.** The contract matrix had binary, unary, cast and literal arms and
no call arm; `CallFn` was the one enum missing from a sweep that has been total over
seven since step 11.

Both holes are closed. The arm runs **155 combinations** and found **one broken
function** — which is a much better result than the operator surface gave, and is
worth saying plainly rather than burying. But that one is broken three ways.

Three commits.

Authoritative where it disagrees with [`step-55-as-built.md`](./step-55-as-built.md),
the vision docs and [`design/`](./design/).

`make test-all` exit 0 (85 package-ok lines = 17 packages × 5 SIMD configurations),
`make race` exit 0 (17), `make levels` and `go vet` clean in all three modules —
each asserted on its own exit code. **PDS-H SF=0.1 validates 22/22.**

---

## 1. A doc sentence, and the defect living inside it

`ResolveCall` takes the whole `Call` node, and said why:

> *"`.struct.field` is the first call whose output type depends on an ARGUMENT …
> **Every other family answers from the receiver's type alone.**"*

The second half was false. `dt.truncate` is the second, and the divergence is exactly
the sentence:

```
dtCallOut(FnDtTruncate, Time)   ->  promises `in`
truncateCalendar(…, Time)       ->  refuses: "a Time has no date"
```

The kernel routes on `iv.IsCalendar()`, so `Truncate(Every("1mo"))` on a Time column
has no answer while `Truncate(time.Hour)` on the same column does. A resolver that saw
only the receiver promised both.

**Two more of the same shape, and these were the surprise.** The kernel also refuses a
zero or negative interval — and both are reachable, because `dtype.FromDuration` is
`Interval{nanos: int64(d)}` and validates nothing, so `Truncate(time.Duration(0))` and
`Truncate(-time.Hour)` carry no error out of `DtExpr.Truncate`'s `iv.Err()` check.
They planned, rendered in `Explain`, and failed at `Collect`.

All three move to plan time. The interval was always visible there: it is a
**constant of the expression**, not data — which is what distinguishes it from the
strict-cast value refusals the cast arm deliberately exempts.

---

## 2. One fixed argument per call asks a smaller question than it looks like

Every finding above is invisible to a matrix that drives each call with one argument
set. `truncate` needs at least two to show anything, and the five flag-bearing string
calls need two for the same reason one layer down: the trailing `literal` flag decides
whether `CompilePattern` compiles a regex at all, so a `literal=true` table leaves
every regex path in the family unexercised.

**The tooth for that decision is the one worth reading.** Deleting the calendar
argument set makes the divergence vanish while the test still passes — it bites only
through the *stale-entry* sweep, and only while the ratchet holds entries. Once the
ratchet is empty, a removed argument set is invisible again.

So the argument table is not self-guarding, and the thing that keeps it honest is the
two-directional public test in §5. Same lesson as step 54's *"a golden that cannot
move is not evidence"*, one layer further in: **a fixture that can shrink silently is
not evidence either.**

---

## 3. What the arm found, and what it did not

| | |
| --- | --: |
| functions covered | 62 |
| labels generated | 1054+ |
| combinations that actually ran | **155** |
| broken functions | **1** |
| gaps recorded | 9 |

The other 61 agree. Every string call is correct on a String and on a Null receiver,
every temporal component is correct on all four instants, `is_in` and `round` agree
everywhere they are defined.

That is a genuinely different result from the operator surface — `TestEvaluatorContract`
found 42 on its first run, the cast cross-product found 27 where a ratchet recorded 6,
and step 55's Null column found 22 — and the reason is worth naming: the call families
have narrow, explicitly-written receiver guards (`HasStringStorage`, `isInstant`,
`IsHashable`), while the operator surface resolves through promotion, where the rules
compose and the compositions are where things hide.

Two things the arm needed that the existing ones did not:

**The fn list is derived from the six exported classifiers**, not from `String()`.
`CallFn.String()` falls back to `"call(N)"` rather than `"?"`, so step 55's `!= "?"`
trick does not port. The classifiers are range tests over contiguous blocks, which
makes the walk total by construction.

**The values are not at round offsets.** `iota` counts ConstSpecs across the whole
block, so `FnDtYear` is 127, `FnIsIn` 247, `FnMathRound` 349, `FnListLen` 451 and
`FnStructField` 500. A driver walking `0..120` would find twenty-six functions and
report nothing wrong.

---

## 4. `CallFn`'s fallback is better than the one the sweep expected

`names_test.go` asserts `"?"` for an unknown constant, and the obvious move was to
change `CallFn` to match. That would have been wrong, and the reasoning is the
file's own:

`"?"` makes **every** unnamed constant render identically — which is precisely the
collision the test exists to catch. It is safe in the other seven only because their
name tables are **arrays sized by a sentinel**, so `"?"` is unreachable for a declared
constant. `CallFn`'s names live in a **map**, where a missing entry is perfectly
reachable, and `"call(247)"` and `"call(248)"` are distinct.

So the test generalised — a range list instead of a count, a per-enum fallback
predicate instead of a hard-coded `"?"` — and the enum did not move to match the test.

A range list can be short in a way a count cannot, so
`TestTheEnumSweepCoversEveryEnumItClaimsTo` counts what each table sweeps. Dropping
one of `CallFn`'s six ranges would leave a family unswept and nothing else would say
so.

`TestCallFnFamiliesDoNotOverlap` guards the six range comparisons themselves, as
`TestMathOpsAreClassified` guards `IsMath`. Every dispatcher that touches a call —
`ResolveCall`, `evalCall`, `compileCall` — is a chain of `case fn.IsString():` arms,
so a constant claimed by two families takes whichever comes first and one claimed by
none falls to an `Internalf`.

---

## 5. Two rules for one question, pinned rather than trusted

Moving truncate's refusals to plan time creates a second copy: `kernel.DtCall` is
exported and reachable without `ResolveCall`, so its checks stay — and are now
**unreachable from the expression path**. They could drift for a whole release with
nothing noticing, because nothing reaches them.

*"Two copies would drift, which is the Binding lesson"* is `ResolveCall`'s own comment
about output types. A second copy of a **refusal** has the same problem.

`TestTruncateRefusalsAgree` walks receiver × interval and asserts the **same kind**,
not merely that both refuse — `errors.Is` walks the cause chain and would call a
`KindValue` and a `KindType` the same thing. It has anti-vacuity floors on **both**
the refusals and the accepts, because all-refuse and all-accept each satisfy an
agreement test and each is a different disaster.

The public test asserts **both directions**, and that is not symmetry for its own
sake: refusing `truncate` on a Time outright would pass a one-sided test and delete a
working feature. Flooring a wall clock to the hour is what a Time is for.

---

## 6. `list.sort` indexed its arguments bare

```go
desc, _ := args[0].(bool)
```

The only member of its family without a guard — `listGet`, `listEnds` and `listSlice`
all return an `Internalf` on a short slice — and `listCallOut` does not inspect
arguments, so `Field` succeeds for a node with none and the process goes down instead
of the query.

Unreachable from `ListExpr.Sort`, which always passes the flag. **Exactly as reachable
from the IR as the contract matrix's call arm is**, which is the distance that matters
now that a matrix drives these functions from a table.

The whole argument-taking family is swept rather than the one that was broken:
*the sibling that forgot the guard* is a class, not an incident — three of four had it.

---

## 7. Teeth

| tooth | result |
| --- | --- |
| remove the `call` subtest while the ratchet is populated | **bites** — all 9 entries go stale |
| drop `IsTemporal` from the derivation | **bites** — *"only 43 call functions derived; the enum declares 62"* |
| restore `dtCallOut`'s type-only view | **bites in three instruments** — the matrix, the agreement test, and `Explain printed a plan that cannot run` |
| **drop the calendar argument set** | **bites, but only through the stale-entry sweep** — §2, and the most informative of the seven |
| delete `FnDtTruncate` from `callNames` | **bites twice** — the name sweep and the family check, both naming `call(141)` |
| restore `listSort`'s bare index | **bites** — `index out of range`, caught as a panic rather than a failure |
| leave a `knownContractGaps` entry undeleted | **bites** — *"now agrees — delete the entry"* |

And an instrument caught **my own** mistake for the third step running:
`TestEveryCitedTestExists` refused a comment citing `TestTruncateRefusalsAgree` before
that test was written. Step 52's was a renamed test, step 55's was a wrong expected
value; this one was a forward reference to work I had planned and not done.

---

## 8. What is still open

- **`.list` and `.struct` are covered by the arm but not reached by it** — 15 of 62
  functions. Every combination is an agreed refusal because `contractBatch` has no
  List and no Struct column, and that fixture is shared with the binary, unary and
  cast arms, so widening it will surface gaps in all three. Its own step, with its own
  ratchet commit. The argument table already holds correct arguments for all fifteen,
  so the day a column is added they are driven properly rather than with a short slice.
- **The string family on a `Binary` receiver**, `round` on `Decimal` and `is_in` on
  `Enum` are unreachable for the same reason. `strCallOut`'s claim that the family
  *"has already normalised Enum and Binary receivers down to String"* stays untested.
- **`is_in` is driven with an empty probe set**, deliberately. A fixed probe is
  strict-cast to the receiver, so one `int64` against `bo`, `dt` or `tm` fails on the
  **value** — not a contract violation. Real probes stay `isin_test.go`'s business; a
  receiver-keyed probe table would reach further.
- **`callCache` now holds roughly a thousand more entries** for the life of a test
  binary, one per `(node, receiver type)` the matrix builds. Harmless here and a
  sharper version of the standing complaint: it is a process-global `sync.Map` with no
  eviction whose keys pin every `*expr.Call` ever evaluated.
- **The byte-flip spill sweep**, deferred a fourth time. The reader has **zero**
  bounds checks: `ncols` panics through `Schema.Field`'s bare index, `ncols == 0`
  yields a valid-looking batch with zero columns, every length varint reaches `make`
  *before* the read that would bound it, a zero-length validity buffer silently
  becomes *all-set*, and a corrupt `TimeUnit` byte has no unknown-value arm. It keeps
  losing to defects reachable from ordinary user code, which has been the right call
  each time and is now a pattern worth naming.
- `Optimizer.Verify` off in the golden inventory and in `Explain`; UDF name uniqueness
  never enforced; `Time + Duration` overflowing 24h with no wrap or saturate; ~37 weak
  `errors.Is` accept-assertions against two `ErrInternal` rejections and one
  `errors.As` in the whole corpus; `Writer.Write`'s unwrapped error and `Open`'s
  discarded read error; `List().Join()` and `Str().Join()`; the UDF serial opt-out;
  `.list` set operations; `Expr`-level selection; `Pivot`; and a benchmark suite
  twenty-nine commits stale on a machine that cannot run it.
