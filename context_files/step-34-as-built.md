# Step 34 — as built

**A Struct column reads from Parquet, survives a query, and can be read from.**
`person: Struct(age: Int64, city: String)` opens, renders, filters, and answers
`.Struct().Field("age")`. With `List` (steps 29–33) that is both of the nested types
a Parquet file is likely to contain.

Authoritative where it disagrees with [`step-33-as-built.md`](./step-33-as-built.md),
the vision docs and [`design/`](./design/).

**1460 test cases green** — 1453 before this step — under `GOEXPERIMENT=simd` ×
`GODEBUG=simd={512,256,128,0}`, with the experiment off, and under `-race`.
`make levels` and `go vet` clean in all three modules. PDS-H SF=0.1 validates 22/22
for ursus (155/155 across all engines).

```
┌──────────────────────────────────┬───────┐
│ person                           │ id    │
│ Struct(age: Int64, city: String) │ Int64 │
├──────────────────────────────────┼───────┤
│ {age: 30, city: "NY"}            │ 1     │
│ {age: null, city: "SF"}          │ 2     │
│ {age: null, city: null}          │ 3     │   <- present, holding nothing
│ null                             │ 4     │   <- absent
└──────────────────────────────────┴───────┘
```

---

## 1. A latent bug the step had to fix before it could add anything

`openNext` opened its column chunks like this:

```go
for i, ci := range r.cols {                       // ci is a FILE LEAF index
    checkEncodings(rgMeta, ci, r.full.Field(ci).Name)   // ... used as a SCHEMA FIELD index
```

That was correct, and correct only by coincidence. Every column readable before this
step consumed exactly one Parquet leaf — a flat column obviously, and a List because
its single element leaf carries the whole thing — so the two numberings advanced in
lockstep and could be used interchangeably. A struct with two fields ends the
agreement for **every column after it**.

The fix is an explicit pair rather than a shared integer:

```go
type colPlan struct {
    field  int   // index into the file schema
    leaves []int // file column indexes, in field order
}
```

`fieldType` and `fileSchema` return `[]int` and `[][]int` for the same reason.

This is why the fixture puts the struct **first** and asserts on the `id` column
after it. Reverting to `r.full.Field(p.leaves[0])` panics with `index out of range
[2] with length 2` — and on a wider file it would not panic at all, it would read the
wrong chunk under the right name.

---

## 2. The struct's own validity cannot be recovered from its fields

For `optional group person { optional int64 age }` the definition levels say three
different things:

```
def 2   person present, age present
def 1   person present, age NULL
def 0   person NULL
```

`fixedCol` already read `age` correctly at any depth — it thresholds `defs[i] ==
maxDef`, which is the field's own question. What no reader produced was **person's**
answer, and once the levels are consumed def 1 and def 0 leave identical evidence: a
null age either way.

So the three leaf readers gained a `parentTracker` hook — when `parentDef > 0`, also
record `defs[i] >= parentDef`. Any one leaf can answer, since every leaf under the
group shares the group's level whatever its own optionality adds, so `structCol` asks
field 0 and the rest just read values.

Both of the obvious shortcuts are wrong, and both were checked by writing them:

| shortcut | what breaks |
| --- | --- |
| infer "null struct" from "every field is null" | `{age: null, city: null}` becomes absent — one row, no error |
| threshold at `== parentDef` instead of `>=` | every FULLY POPULATED struct becomes null |

The first is the empty-versus-null distinction this arc keeps returning to, in its
struct form. `IsNull()` is where it becomes reachable from an expression, and the
semantics test pins all four rows through it.

---

## 3. `field()` is a projection, not a gather

A struct's fields ARE columns — each already as long as the struct, addressed by the
same row number — so `.Struct().Field("age")` hands one back and costs a rename. No
kernel work, no `Take`, nothing per row.

The typing is where the work went. `.struct.field` is the **first call whose output
type depends on an argument**: `field("age")` is Int64 and `field("city")` is String
from the same receiver, where `.list.get(i)` and `.str.slice(o, l)` answer from the
receiver alone. So `ResolveCall(fn, in)` became `ResolveCall(c *Call, in)` — both of
its two callers already held the `*Call` — and an unknown field name is refused while
the query is still being PLANNED, with the names that do exist listed.

The returned column carries the FIELD's validity, not the struct's, and no post-mask
is needed: a definition level below the group's is below the field's too, so the
reader has already marked the field null wherever the group is absent. That is a
property of how the levels work rather than a coincidence, which is why the test pins
the row rather than trusting it.

`takeStruct` and `concatStruct` are the recursive case in its simplest form — the
same `sel` applied to every field, and a field-wise concat with nothing to rebase,
unlike their List counterparts.

---

## 4. A test that promised something it did not check

`TestStructSurvivesTakeAndFilter` was written with a table of expected ages **and
cities**, and asserted only the ages. The `city` column of that table was dead — and
a test that declares an expectation it never evaluates reads exactly like one that
does.

It surfaced because the tooth aimed at it did not bite: gathering field 1 with a
zeroed selection — the way a struct actually goes wrong, every field the right length
and describing different rows — passed. With both fields checked it fails
immediately (`city = "a", want "b"`).

Worth stating as a rule: for a struct, checking ONE field proves nothing about
alignment. The failure mode is that the fields disagree with each other.

---

## 5. The refusal list keeps shrinking, and has to keep existing

`TestNestedColumnIsRefusedNotHidden` guards "a column that cannot be read is refused
rather than silently dropped". Its list has been emptying as the arc progressed —
`tags` left in step 29, `user` left here — so the fixture gained
`meta: Struct(inner: Struct(x: Int64))`, which is still unreadable because
`structFieldLeaves` declines any field that is not a plain primitive.

That one gate covers struct-in-struct, list-in-struct and repeated fields, and
`nodeType` still NAMES all of them, so a user sees the shape of what they cannot yet
read.

---

## 6. Verification

`struct_test.go`: the four-row table at four batch sizes, checked on the struct's
validity, both fields, and `IsNull`; a filter with both fields verified; `GroupBy` on
an extracted field, proving it is an ordinary column; the projection that must not
open the struct at all; and the two refusals. `internal/source/parquet/nested_test.go`
grew the unreadable column and the fifth field.

Checked rather than assumed: grouping by or sorting on a struct column is refused
with a message naming the type (`cannot group by col("person"), which is a
Struct(...)`) rather than panicking.

`git status bench/results/REPORT.md`: untouched.

---

## 7. What is still open

- **`Unnest`** — expanding a struct into top-level columns. A plan node plus a
  `BatchOp` plus a name-collision rule, which is the shape `Explode` got its own step
  for. `.struct.field()` is the primitive it would be built on.
- **Nested structs, list-in-struct, struct-in-list** — named and refused. Each needs
  the level machine and the leaf readers to compose, which is a different problem from
  either alone.
- **`rename_fields`, `with_fields`**; **Map** and **Array**; **writing** a Struct or
  List column, which is why every nested fixture is still built at the arrow-go layer.
- **`.list` set operations and `gather`** — still open from step 33; they take a
  second list column.
- **Spilling a struct** — `internal/spill` refuses `TypeStruct` explicitly, so a
  memory-limited query over one fails rather than misbehaves.

Unchanged: **Pivot/Unpivot**, SQL, cloud stores, plan serialization. And the
performance ledger — **join reordering** (no cost model; PDS-H is 10.7x polars at
SF=1), **join parallelism** (8 threads slower than 1), **memory** (4.7x polars), and
the ~30% measurement noise floor.
