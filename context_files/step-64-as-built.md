# Step 64 — as built

**The contract fixture's type axis was a hand-written list.** `TestEvaluatorContract`
derives its *operator* axes from their enums on purpose, and says why. Its *type*
axes were written down. Deriving them produced **106** disagreements where five
steps of carried estimate had predicted about thirty-two.

Five commits and this document.

Authoritative where it disagrees with the vision docs and [`design/`](./design/).

`make test-all` exit 0 (**100** package-ok lines = 20 packages × 5 SIMD configurations),
`make race` exit 0 (20), `make levels` and `go vet` clean in all three modules, PDS-H
SF=0.1 exit 0 with **22/22** matching the duckdb reference — each asserted on its own
exit code. No benchmarks. `list.mean`'s declared output type changed, so the golden
plans and `ursus-api.md` were checked: no golden mentions a list aggregate and the
API doc records signatures rather than resolved types, so neither moved. Checked,
not assumed.

---

## 1. The file's own argument, running against the file

`internal/physical/evalcontract_test.go` asserts what makes `CollectSchema` honest:

```
Field succeeds  =>  Eval succeeds, Eval(...).DType() == Field(schema).Type, rows == height or 1
Field fails     =>  Eval must fail too  (an AGREED refusal)
```

On the operator axes it says:

> The op lists are DERIVED, not written down. They used to be hand-written, and a
> hand-written list of an enum goes quiet exactly the way an allow-list does.

and three declared ops had in fact been missing. But `contractCols` was seventeen
names in a slice, and `contractCastTargets` twelve types in another — and the binary
arm's own anti-vacuity comment admitted it in passing: *"the column list is **written
down**, so it can only shrink by hand."*

So **List and Struct were absent for twelve steps**, and with them all fourteen
`.list` call functions and `struct.field`, whose argument sets sit in the file
already written, under a comment saying they are there "so that the day a List column
is added the arm drives them correctly". `Uint16`, `Decimal`, `Binary` and `Int128`
were absent with no reason at all.

Both axes are derived now. The columns are the source of truth; the schema is built
**from** them, the name axis comes from the batch, and the cast targets come from the
columns. `TestContractColumnsCoverTheEnum` walks `TypeIDCount` and fails both ways —
**22 built + 4 excused = 26 = `TypeIDCount`**, exactly.

The schema-from-columns direction is not tidiness. `data.NewList` and `data.NewStruct`
*derive* their dtype from the child and the fields, and both say why: a declared type
that disagreed with the columns holding the values would be a lie no caller could
detect. Writing the fields out separately would put that lie back.

## 2. Measured: 106, in seven classes

| n | class |
| --- | --- |
| 48 | equality over a `List` resolves and the kernel has no nested arm |
| **32** | **`Binary` comparison — all eight operators** |
| 13 | `kernel.NullColumn` cannot build a null `Struct` |
| 6 | `CanCast` promises `List → List` and the kernel refuses |
| 4 | equality over a `Struct` |
| 2 | `list.mean` promises `Float64` without reading the element type |
| 1 | `CanCast` promises `String → Int128` and the kernel cannot narrow |

Step 58's *"~32 predicted disagreements"* was carried forward five times without once
being measured. It was not wrong so much as **scoped to what its author could see**:
one List and one Struct with a numeric child gives exactly 32, and the arithmetic
landing on the nose is good evidence the reasoning was sound — it simply could not
include the classes that need a *different* element type, a Binary column, or a
derived cast-target axis to become visible.

**The Binary class is the one to read.** `Binary` is an ordinary scalar that Parquet
and Arrow both produce. `Col("blob").Eq(Col("blob"))` type-checked and then failed in
the kernel, for all eight comparison operators. The fixture simply had no Binary
column.

## 3. The fixes

- **`list.mean` derives its element type.** `listCallOut` answered `Float64` for
  every element without reading `elem`; `listReduce` took the declared type as a
  parameter, **ignored it**, and derived the real one. `List(Duration)` produced a
  Duration and `List(Float32)` a Float32 against a promised Float64 — the only class
  here where `Eval` *succeeds* and hands back the wrong type. The arm now delegates
  the way `FnListSum` three lines below it always did, and the dead parameter is gone.
- **`Binary` compares on the storage question.** `dispatchCompare` gated its
  offset-and-character path on `IsString()`, which asks whether a value is *text*.
  The question it needed was `HasStringStorage()`, whose own doc says it "is the
  storage question". The gap between the two predicates is exactly Binary.
- **Nested equality is refused at the resolver.** *"Equality is defined for anything
  with a common type"* is true of a `List`, and the kernel dispatches on the physical
  type and gets a List back unchanged. `Promote` is deliberately untouched: `Concat`
  unifies schemas through `Promote(List(T), List(T))`, so narrowing it would break a
  working feature to fix a broken one.
- **`NullColumn` learned a `Struct` arm**, and **a `List` casts by casting its
  elements**, and **`CanCast` stopped promising `String → Int128`** — see §5.

## 4. Three findings about the fixture itself

**The element type is a coverage decision.** `ResolveAggBinding(AggMean, Int64)` *is*
`Float64` — so an integer element is precisely the one for which `list.mean`'s
hard-coded rule is right, and a fixture carrying only `List(Int64)` would have added
three hundred combinations and reported a clean run. The fixture carries three List
columns. Reverting it to one integer list is a recorded tooth, and it silences 35
assertions.

**A one-field Struct cannot test `struct.field`.** The function's claim is that the
output type comes from the *argument*; against a single-field struct that is
indistinguishable from "the receiver's only type". It has two fields of different
types, and three argument sets including a name that is not there.

**Enum is constructible and excused anyway, and the reason is measured.** Adding it
does not produce a gap, it produces a **panic**, in the cast arm, before anything can
be recorded: `IsString()` is true for an Enum, so `CanCast` admits `Enum → numeric`
and `parseFromString` calls `Strings()` on a uint32 payload. `ResolveCall` names the
same hazard and calls it *"latent today only because Enum columns cannot yet be built
from the public API"*. Building one makes it non-latent. The entry is a **debt**, not
an exemption — the walk fails the day someone deletes the line without adding the
column.

## 5. The cast family, which unary.go had already seen three times

`unary.go` records three prior plan-accepts / kernel-rejects divergences — null
casts, string casts, bool casts — and closes each by implementing what `CanCast`
promised. `List → List` is the fourth, and the one that hid longest: **neither cast
instrument could reach it**, because this fixture had no List column and no List cast
target, and `TestCanCastAgreesWithTheKernel` names List as an unsamplable *source*.

The List cast gathers the row window rather than casting the child in place, and the
reason is **strictness, not indexing** — which was checked rather than assumed. A
sliced List keeps absolute offsets over a whole child, and those offsets line up
perfectly well over a wholly-cast child. What does not survive is a strict cast: the
child holds elements belonging to rows the column does not have, so casting in place
refuses a slice because of a row *outside* it. `listGather` is split out of
`listRebuild` so both share one offset walk — the part that must not be copied, since
step 59 measured the same defect twice in two kernels that had each written their own.

`String → Int128` went the other way: `CanCast` stopped promising it. The integer
parser is `strconv.ParseInt` at 64 bits, so a 128-bit target walks past it into
`narrow` and ends at an **`Internalf`** — *"this is a bug in ursus"* — raised by an
ordinary user cast. Both ways of making it work are wrong: through int64 it caps at
exactly the range Int128 exists to exceed, through float64 it rounds above 2^53,
which `unary.go` refuses in as many words. A real 128-bit parse needs `i128`
multiplication, which `i128` does not have. This is the `Decimal` arm directly above
it, one type over, excluded for the same reason.

## 6. What this instrument cannot see

Found by reaching for a tooth, and worth more than the tooth was: **an over-broad
refusal is invisible to it.** Making the nested-equality check refuse *every* type
leaves the matrix green, because a refusal both halves agree on satisfies the
contract. `internal/expr` and `internal/kernel` are what fail.

It proves `Field` and `Eval` agree. It does not prove either is right. The
anti-vacuity counters are all that stand between it and a matrix that refuses
everything — which is the same argument the `within`/`outside` counters make in step
63's sweep, arriving from the opposite direction.

## 7. Teeth

| reintroduce | result |
| --- | --- |
| the List columns removed | **bites** — the coverage walk, 59 lines |
| `TypeList` both built and excused | **bites** — the both-ways half |
| the only List made `List(Int64)` | **bites** — 35 lines; the step's thesis pointed at itself |
| the cast targets hand-written again | **bites** |
| one ratchet entry deleted without a fix | **bites** (after re-aiming, below) |
| an entry that no longer bites | **bites** — stale-entry detection |
| `list.mean` hard-codes `Float64` again | **bites** — both public tests, loudly, via `checkShape` |
| the comparison guard asks `IsString` again | **bites** — six subtests, thirty-two labels |
| the signed byte order asserted for `\xff` | **bites** — that fixture row is load-bearing |
| nested equality unrefused, in either resolver | **bites** — ~35 labels each |
| the nested check made over-broad | **did not bite the matrix** — §6 |
| `NullColumn` loses its Struct arm | **bites** |
| the List cast removed | **bites** |
| `CanCast` promises `String → Int128` again | **bites** |
| the List child cast in place | **did not bite** until re-aimed at a *strict* cast |

**Three teeth had to be re-aimed, and each says something.**

The ratchet-deletion tooth did not apply at all the first time: `gofmt` aligns map
values, so an exact-string patch silently matched nothing. A tooth that fails to
*apply* is not a tooth that fails to bite, and the difference is only visible if you
check the patch landed.

The Binary "signed bytes" tooth weakened the *fixture* — and weakening a fixture
cannot fail a test that still passes. Re-aimed at the expectation instead.

The List "cast in place" tooth did not bite because **my first version of it was
correct**: absolute offsets over a wholly-cast child do index correctly. The comment
claiming otherwise was wrong and has been corrected; the real reason to gather is
strictness, and the test that proves it was written afterwards.

## 8. Two mistakes of mine, both caught by the gate

A global `sed` renaming `Col("xs")` to `Col("tags")` in one test file also hit a test
I had written minutes earlier which used `xs` for its own frame. The targeted run was
green; the full suite caught it.

I asserted that `When().Then(list).Otherwise(list)` must keep working as the
complement to leaving `Promote` alone. It does not — conditionals over a List are
independently unimplemented — so the assertion was wrong, not the code. `Concat` is
the dependency that actually holds, and is what the test asserts now.

## 9. The fixture, not the test, is the unit of coverage

The **thirteenth** recorded instance, and the most direct one yet: not a fixture that
missed a case, but a fixture **axis** that missed seven types, in the file whose whole
argument is that hand-written lists go quiet. The instrument was correct and total
over its operators for twelve steps while being silently partial over its types.

## 10. Still open

- **UDF name uniqueness is unenforced, and it is the strongest remaining defect I
  know of** — the only one on the carried list where the engine answers wrongly and
  says nothing. `checkUDF` refuses an empty name and nothing refuses a duplicate;
  `UDF.String()` renders the name and declared type and **never the closure**; three
  dedup maps key on that rendering. Two different functions under one name over the
  same column collapse into one computation and **both results come from whichever
  was seen first**. `udf.go`'s own doc states the consequence and does not enforce it,
  and `udf_test.go` proves only the positive direction. Same shape as two bugs already
  shipped and fixed here. The trap: `kernel.ColumnUDF` is a named func type, so `==`
  on two `UDFImpl` values **panics** — identity must go through
  `reflect.ValueOf(impl).Pointer()`.
- **`dtype.Interval.Every` accumulates unchecked.** `Every("357913942y")` is **8
  months**, `Every("613566757w")` is **3 days**, `Every("5124096h")` is **25m26s** —
  each with `Err() == nil` and `Negative() == false`, passing every downstream gate.
  It reaches `GroupByDynamic`/`Rolling`, the as-of tolerance (failing **open** on one
  wrap and **closed** on another) and `dt.Truncate`. `Every("1h1d1227133513w")` wraps
  days to zero and **bypasses the mixed-units refusal** outright.
- **The Enum hazard is wider than the cast arm.** `take.go` routes on
  `IsString() || ID() == TypeBinary`, which is true for an Enum, so `Take` over an
  Enum column reaches the string path the same way the cast does. Whether it panics
  was not measured; the excuse entry names the cast because that is what was measured.
- `mean(Datetime)` — a gap, not a defect, and it must **floor** where
  `mean(Duration)` truncates, so its rounding tooth is step 62's with the sign
  reversed.
- **The as-of sink under a memory budget**: surveyed, **no wrong answer reachable**.
  `Probe` materialises a second full copy and the bucket map without accounting
  either, so the ceiling is enforced against roughly half the true peak; the sink has
  no `budget` field so it cannot spill; and `join_asof`, `rolling` and `merge_sorted`
  are missing from `holdsHint`, from `Check`'s hint list and from
  `WithMemoryLimit`'s public doc, so the error that does fire describes the operator
  wrongly.
- **`context_files/README.md`'s forward-looking tail is stale in at least three
  places**, checked: `reverseSink` has had tests since step 50, `JoinWhere` is
  implemented, and the CSV reader already uses `data.NewStringParts`.
- Carried: the byte-flip spill sweep (deferred an eighth time — the validity flag and
  the unit byte are each one-byte flips that change the answer with no error);
  `Optimizer.Verify` off in `Explain`, where the live consequence is that a
  desynchronising rule renders a **clean golden file**; the planner leak, which is
  seventeen sites rather than six; `spill.Writer.Write`'s bare error; `callCache`
  eviction; `Pivot`; the stale benchmark suite.
