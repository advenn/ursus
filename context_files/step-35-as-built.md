# Step 35 — as built

**`Unnest` — a struct column replaced by its fields, in place.** It is to Struct what
`Explode` is to List: afterwards the nested column is gone and the fields are ordinary
columns, so every filter, aggregate, join and sort applies to them directly.

Authoritative where it disagrees with [`step-34-as-built.md`](./step-34-as-built.md),
the vision docs and [`design/`](./design/).

**1467 test cases green** — 1460 before this step — under `GOEXPERIMENT=simd` ×
`GODEBUG=simd={512,256,128,0}`, with the experiment off, and under `-race`.
`make levels` and `go vet` clean in all three modules. PDS-H SF=0.1 validates 22/22
for ursus (155/155 across all engines).

```go
ScanParquet(f).Unnest("person").
    Filter(Col("age").Gt(int64(30))).
    GroupBy(Col("city")).Agg(Col("age").Sum())
```

---

## 1. Explode's simpler twin, and the half of its argument that carried over

Step 30 had to argue that a `BatchOp` may change the ROW count — Filter already did,
so explode needed no new operator shape. Unnest needs the other half of that
sentence: a `BatchOp` may change the COLUMN count, and `projectOp` already did, so
there was nothing to argue.

What is left is smaller than explode in three ways. The row count does not change, so
there is no gather at all — a struct's fields are parallel to it and already the
right length. There is no zip, so unnesting several columns needs no agreement
between them. And `Apply` is a splice: walk the input columns, and where one is
named, emit its fields instead.

---

## 2. In place, and the test has to look at positions

The fields land where the struct stood, so `{person, id}` becomes
`{age, city, id}` — not `{id, age, city}`. Appending is the easy mistake and it does
not error: the schema is right, every value is right, and a test that reads columns
BY NAME passes.

So the fixture has a column after the struct and the assertion is on the whole
ordered list:

```
columns are [id age city], want [age city id] — the fields replace the struct where it stood
```

---

## 3. The validity mask is the operation's definition, not a defensive copy

What is `age` for a row whose `person` is null? Null.

Today that is already true without doing anything, because every struct in existence
comes from the Parquet reader, which marks a field null wherever its group is absent
— a definition level below the group's is below the field's too. **But nothing
enforces it.** `data.NewStruct` does not check, and `Slice`, `takeStruct` and
`concatStruct` carry whatever they are handed.

So `unnestOp` ANDs the struct's validity into each field. Rather than leave that as
code justified by an argument nobody can test — which step 32 named as worse than no
code — `internal/physical/unnest_test.go` builds the inconsistent column by hand:

```go
age    := NewFixed("age", Int64, []int64{7, 99, 8}, AllSet(3))
person := NewStruct("person", []*Column{age}, /* valid: */ {true, false, true})
```

A perfectly valid 99 sitting under an absent struct. Dropping the mask fails that
test and nothing else — which is exactly the point: it is unreachable through the
reader today and it is one new producer away.

The mask is skipped when the struct has no nulls, which keeps a field's no-storage
validity — the form downstream kernels take their fast path on — from being replaced
by a full bitmap for nothing. `data.Column` gained `WithValidity` for this.

---

## 4. The collision refusal earns its place

Fields keep their own names, which is what makes the result ordinary and is also what
lets them collide with a column already in the frame. `dtype.NewSchema` catches a
duplicate on its own, so refusing in `Unnest.Schema()` looks redundant — until you
read the two messages:

```
NewSchema:  duplicate column "age"
            appears at positions 1 and 3; column names must be unique
            use .Alias(...) to rename one of them          <- to rename WHAT?

Unnest:     two columns would be called "age": a field of "person", and the
            column already in the frame
            rename the existing column before unnesting
```

The generic hint tells the caller to alias a column that does not exist yet. The
teeth check for this asserts on the MESSAGE, not just that an error occurred — the
weaker assertion passes either way.

The check runs against every name the OUTPUT will hold rather than the ones emitted
so far, because a field can collide with a column that comes AFTER the struct just as
easily as one before it, and a running check only sees one of those.

---

## 5. `Expr.Unnest()` is refused for a different reason than `Expr.Explode()`

Both are refused; the reasons are not the same and the message says which.

`Explode` is not an expression because it changes the frame's **height**. `Unnest`
leaves the height alone and changes its **width**: an expression produces exactly one
column and this produces one per field. A caller who reaches for
`Col("person").Unnest()` is pointed at `lf.Unnest("person")` for all of it, or
`Col("person").Struct().Field("age")` for one field.

---

## 6. What it erases

A present struct holding nulls and an absent struct produce identical output. That is
the same erasure `Explode` performs on an empty list against a null one, and for the
same reason: the distinction belongs to the struct, and this is what consumes it.
`Col("person").IsNull()` keeps it, before unnesting.

`TestUnnestErasesTheNullDistinction` checks both halves — that the two rows ARE
distinguishable before and are NOT after — because a consequence stated only in a doc
comment is a consequence nobody has checked.

---

## 7. Verification

`unnest_test.go`: the fixture at four batch sizes with the column order asserted as a
list; the erasure, before and after; `Unnest → Filter → GroupBy → Sort`, which is the
point of the operation; and five refusals (no columns, unknown column, non-struct,
named twice, the collision) plus the expression form.

The optimizer was **tested rather than trusted**, as step 30 did: a `Select` above an
`Unnest` keeps the right columns, and a `Filter` above one is not pushed into a scan
where the column it names does not exist yet. `rule_predicate`'s default is
`pushClosed` and `rule_projection`'s is the conservative "require everything the
children can produce"; neither needed changing, and now that is a fact rather than a
reading.

`internal/physical/unnest_test.go`: the hand-built inconsistent struct, and that an
all-valid struct hands its field through untouched.

`git status bench/results/REPORT.md`: untouched.

---

## 8. What is still open

- **Building a struct** — `as_struct(cols)`, the inverse of this. It is a new
  expression producing a nested column from flat ones, and it is where every producer
  question the reader answered comes back: what does it do with a row that should be
  a null struct rather than a struct of nulls?
- **Writing** a Struct or List column. This is the one real asymmetry left in the
  nested arc: ursus reads nested Parquet and cannot write it, which is why every
  nested fixture in the test suite is built by hand at the arrow-go layer.
- **Nested structs, list-in-struct, struct-in-list** — named and refused.
- **`rename_fields`, `with_fields`**; **Map**, **Array**; spilling a nested column
  (`internal/spill` refuses it).
- **`.list` set operations and `gather`** — still open from step 33; they take a
  second list column.

Unchanged: **Pivot/Unpivot**, SQL, cloud stores, plan serialization. And the
performance ledger — **join reordering** (no cost model; PDS-H is 10.7x polars at
SF=1), **join parallelism** (8 threads slower than 1), **memory** (4.7x polars), and
the ~30% measurement noise floor.
