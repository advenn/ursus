# Step 55 — as built

**The Null operand.** `ResolveUnary` admitted `dtype.Null` for eleven operations and
`kernel.Unary` implemented none of them. `kernel.Take` had no arm for it either, so
**`Select(ursus.Null(ursus.NullT))` — the spelling `cond.go` recommends — did not
work.**

The instrument that catches all of this was one fixture column away from working,
and the column was missing.

Three commits.

Authoritative where it disagrees with [`step-54-as-built.md`](./step-54-as-built.md),
the vision docs and [`design/`](./design/).

`make test-all` exit 0 (85 package-ok lines = 17 packages × 5 SIMD configurations),
`make race` exit 0 (17), `make levels` and `go vet` clean in all three modules —
each asserted on its own exit code. **PDS-H SF=0.1 validates 22/22.**

---

## 1. The assertion was written and could not fire

`evalcontract_test.go` has asserted this since step 52:

```go
if c.Len() != b.Rows() && c.Len() != 1 {
    t.Errorf("%s: produced %d rows for a %d-row batch", label, c.Len(), b.Rows())
}
```

`contractBatch` had sixteen columns and none of them was Null. **Adding one produced
twenty-two disagreements**, which are checked in — in their own commit, before the
fix — in `knownContractGaps`, kept empty since step 52 *"because the mechanism is the
useful part"*.

Seventh recorded instance of *the fixture, not the test, is the unit of coverage*,
and the third in four steps where the blind instrument was one I had built.

The ratchet could not express the headline gap as it stood: it suppressed errors from
the Field/Eval switch but not the shape assertions, and `not(nu)` **succeeded** —
right type, nil error, wrong row count. `checkContract` now routes every assertion
through one recorder, so the ratchet covers what the test actually checks.

### Two more things the fixture was hiding

**The op lists were hand-written, and three declared ops were not in them.** `OpPow`,
`OpLog10` and `OpLog1p` had never been through this matrix. Both `String()` methods
return `"?"` for an undeclared value and are sized by their enum's own sentinel, so
the lists are now derived by walking the range — the same total-by-construction trick
step 51 used for `AggOp`, available only because step 51 fixed the empty-string
fallback that would have made `"?"` unreachable. `log10` and `log1p` are two of the
twenty-two.

**The literal operand was a node the public API cannot build.**
`&expr.Lit{Value: int64(2)}` leaves `DT` at its zero value, which is `dtype.Null`.
`litNode` always sets it. `Lit.Field` reports `DT` while `litColumn` switches on
`Value`, so that node **declares Null and evaluates to Int64** — and the matrix
survived it only because `Promote(T, Null)` is `T` for every column it had. Against a
Null column there is no `T` to adopt, and it surfaced as four failures belonging to
the fixture. Both literals are now what the public surface builds, including
`ursus.Null(ursus.NullT)`.

---

## 2. One question, answered three ways in one switch

| ops | admitted Null? | promised |
| --- | --- | --- |
| `is_null`, `is_not_null` | yes | `Bool` — **and correct**, total over validity |
| `not` | yes | `Bool` |
| `is_nan`, `is_not_nan`, `is_finite`, `is_infinite` | yes | `Bool` |
| `neg`, `abs` | **no** | — |
| `sign`, `floor`, `ceil` | yes | `Null` |
| `sqrt` … `log1p` | yes | `Float64` |

`neg` refuses and `sign` accepts, three lines apart in the same family. Ten of the
eleven failed at execution, six as `Internalf` — the user told that an expression the
planner had just accepted is a bug in ursus.

**The two answers already written down were implemented.** `op.go` says *"Kleene
NOT: NOT null is null"*; three files say *"null.is_nan() is NULL, not false, because
a missing value is not a float that could be tested"*. Neither was true of the code.
`kernel.Unary` gains one guard that returns n nulls of the promised type — the shape
`Cast`'s `from.IsNull()` arm already takes, through `NullColumn` rather than
`data.NewNull` because `take.go`'s rule is that this is an **operand** somebody will
gather from.

**The drift nothing argued for was narrowed.** `sign`, `floor`, `ceil`, the six maths
ops and `round` — their Call-surface twin — now refuse Null at plan time exactly as
`neg` and `abs` always have.

---

## 3. `not` returned the right type over zero rows

```go
case expr.OpNot:
    return data.NewBool(name, bitmap.Not(c.Bools()), c.Validity()), nil
```

`data.NewNull` carries validity and **no payload at all**, so `c.Bools()` is the zero
`View`, `bitmap.Not` is length-driven and returns length 0, and `data.NewBool` takes
its length from the payload while **silently discarding** the n-bit validity it was
handed. A `Bool` column of length 0 carrying 3 validity bits, returned with a nil
error, from a kernel that had satisfied its type contract.

The user saw:

```
ursus: physical: expression lit(null).not().alias("v") produced 0 rows for a 3-row batch
  this is a bug in ursus; please report it
```

`NewBool` now panics and names both lengths. Two integer compares against the cost of
building the bitmap, so there is no case for hiding it behind `CheckNonNullable`'s
debug flag; a panic rather than a clamp on the argument `NewBatchRows` already gives
for the same shape of violation. **Nothing in the repository produces one**, and the
gate passing is the evidence.

---

## 4. The one that was worst was also the simplest

`kernel.Take` had no arm for a column whose type says there are no values. A literal
evaluates to a **length-1** column, so putting one beside a batch's other columns is a
broadcast and a broadcast is a Take — which means **every** use of an untyped null
went through the one path that refused it:

```
Select(ursus.Null(ursus.NullT))  ->  ursus: take is not implemented for Null
```

`ursus.Null(ursus.Int64)` worked throughout, which is why nothing noticed: the typed
form takes the ordinary fixed-width path. `Explain` printed the plan cleanly first.

This was not predicted by anything. It was found by probing the Call surface for a
different reason and reading the error rather than the expression that produced it.

---

## 5. The comparison family, where the two halves answer differently

Two Null operands promote to Null; equality needs no ordering, so the binding is
`CastL = CastR = Null` with `Out: Bool` and **nothing casts**. `dispatchCompare`
refused the whole family after `Field` had promised `Bool`.

The fix has to give two different answers, and `op.go` states both:

```
==  and !=    three-valued      ->  null == null is NULL
<=> and <!>   nulls are data    ->  null <=> null is TRUE, and never null
```

`missingCompare` already had `case !lok && !rok: res = true` and could not reach it,
because it computes the ordinary comparison first. So one arm in `dispatchCompare`
fixes both families, and it appends zeros — which is not a semantic choice, because
neither caller reads those bits for a row where both sides are null. A fix that gave
all four the same answer would satisfy every type assertion and be wrong about half.

---

## 6. The Call surface: measured, and only one was wrong

The contract matrix has **no Call arm**, so these six were checked by hand.

Five were already correct — every string call produces its promised type from a Null
receiver. `is_in` was not, and it failed in a way none of the others could: its probe
set is encoded against the **receiver's** type, so `compileCall` tried to cast `Int64`
probes to `Null`. It now answers the way the NaN predicates do — whether a value that
is not there belongs to a set is unknown, not false.

Only `is_in` is special-cased. A general Null arm for every call family would also
work, and it would replace five paths that were measured correct; the matrix arm that
would justify it is separate work.

---

## 7. A false doc claim, retired with its duplicate

`evalCond` hand-rolled `kernel.NullColumn(pred, Bool, n)` under:

> *"Cast cannot do this — a data.NewNull has no payload to reinterpret."*

`kernel.Cast` does exactly this, and has since it gained its `from.IsNull()` arm for
the same stated reason. The guard now calls `Cast`, so there is one mechanism — a
false comment about a neighbouring function is how the next person writes a second
workaround for a hole that was already filled. Second false doc claim found in three
steps; step 53's was `uerr`'s *"prefix containment"*.

---

## 8. Teeth

| tooth | result |
| --- | --- |
| remove the `nu` column while the ratchet is populated | **bites twice** — the new column floor, and 22 stale entries |
| revert the `kernel.Unary` Null guard | **bites** — `not(nu): produced 0 rows for a 3-row batch` |
| revert `NewBool`'s guard as well | **bites** — proves the constructor catches it independently, naming both lengths |
| remove `dispatchCompare`'s Null arm | **bites** — exactly 8 resurface |
| remove `Take`'s Null arm | **bites** — 7 cases, including `Select(Null(NullT))` |
| remove the `is_in` arm | **bites** — *"cast from Int64 to Null is not implemented yet"* |
| leave a `knownContractGaps` entry undeleted | **bites** — *"now agrees — delete the entry"*, observed on all 14 unary entries at once |

### The tooth that did not bite, and what it showed

**Restoring `!in.IsNull()` for `sign`/`floor`/`ceil` changed nothing.** The contract
matrix stayed green, because `kernel.Unary`'s Null guard is general: it answers
`sign(null)` with a Null column of n rows, which is the type `ResolveUnary` promised
and the right shape.

So the narrowing is **not what makes the contract hold** — it is a semantic choice,
and the honest tooth for it is a different one. `TestNumericUnaryRefusesNullAtPlanTime`
asserts the refusal directly, and with both admissions restored it fails on all nine.
Re-aimed, not quietly dropped.

---

## 9. Verification

- Every implementation asserted on **values and row count**. The defect this step
  opens with produced the correct *type*.
- Every narrowing asserted with `assertUserError` — which also fails if `Explain`
  succeeds, the half that matters, since the old behaviour was a plan that printed
  cleanly and could not run. `errors.Is` cannot see this: it walks the cause chain.
- `is_null`/`is_not_null` asserted to stay **total** over a Null column — n rows,
  zero nulls, all-true and all-false. Without it, "make everything null" would pass.
- The Call surface asserted as `CollectSchema`'s promise against `Collect`'s answer,
  which is the contract the matrix asserts for everything that is not a Call.

---

## 10. What is still open

- **The contract matrix has no `Call` subtest.** Six calls were measured by hand and
  one was broken. `round`, `is_in` and the whole string and temporal namespaces admit
  operand types at resolution that no subtest reaches.
- **Byte-level spill corruption**, now fully mapped: the reader has **zero** bounds
  checks. `ncols` panics through `Schema.Field`'s bare index; `ncols == 0` yields a
  valid-looking batch with N schema fields and zero columns; every length varint is
  passed to `make` **before** the read that would bound it, so six bytes request a
  terabyte; a zero-length validity buffer silently becomes *all-set*, turning every
  null in a column into a value; a corrupt `TimeUnit` byte has no unknown-value arm
  and becomes a silent 10⁹× scale error. No checksum, no cross-check, no cap, and no
  `recover()` anywhere in the module. `truncate_test.go`'s `readAll` is the driver,
  but it only drains — a corruption sweep must also *read* every column, since most
  of these panic in a kernel rather than at the reader.
- **A payload-free `Null` column in a frame is still mostly untested.** `Take` now
  handles it; `Concat`, `Sort`, a join key and a spill round trip were not exercised
  here, and `data.IsPayloadFree` exists precisely because the rule used to be
  enforced by memory.
- **`Optimizer.Verify` is off in the golden inventory** (`inventory_test.go`, a bare
  `plan.NewOptimizer()`) **and in `Explain`** (`lazy.go`, via `optimizer()`, which
  never sets it — only `compile` does).
- **UDF name uniqueness** — `checkUDF` rejects only an empty name while its own hint
  states the consequence it does not prevent; `extractAggs`' `byKey`, plus the same
  keying in `resolve_window.go` and `window.go`.
- **`Time + Duration` overflows 24h** with no wrap or saturate, and `FormatTemporal`
  rolls it over silently, so `23:00 + 2h` sorts and compares differently from how it
  prints. Needs a semantics decision, not a guard.
- **~37 weak `errors.Is` accept-assertions across 24 files** (up from 32), against
  exactly **two** `ErrInternal` reject-assertions and a single `errors.As` in the
  whole test corpus. Wants one `ursustest` helper.
- The standing list: `callCache`'s missing eviction and its pinned `*expr.Call`
  keys, `Writer.Write`'s unwrapped error and `Open`'s discarded read error,
  `List().Join()` and `Str().Join()`, the UDF serial opt-out, `.list` set
  operations, `Expr`-level selection, `Pivot`, and a benchmark suite twenty-six
  commits stale on a machine that cannot run it.
