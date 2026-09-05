# Step 28 — as built

A Parquet file containing a nested column can now be opened. **The nested column
is named in the schema and refused on read, instead of the whole file being
rejected.** First work on v0.3.

Authoritative where it disagrees with [`step-27-as-built.md`](./step-27-as-built.md),
the vision docs and [`design/`](./design/).

**1421 test cases green** — 1415 before this step — under `GOEXPERIMENT=simd` ×
`GODEBUG=simd={512,256,128,0}`, with the experiment off, and under `-race`.
`make levels` and `go vet` clean in all three modules. PDS-H SF=0.1 still
validates 22/22.

```
a file with id, name, tags: List(Int64), user: Struct{age, city}

  before                          after
  ScanParquet -> error            Schema()          -> all four columns, typed
  (no column readable)            Select(id, name)  -> works
                                  Select(tags)      -> the same error, same hint
                                  select-all        -> the same error
```

---

## 1. Why this and not the namespaces

v0.2 is complete — `CollectStruct`, `AsOf`, `GroupByDynamic`, `Rolling` and
`Explain` all exist — so completeness means v0.3, whose first item is the
list/struct namespaces. None of it exists: `TypeList`, `TypeStruct`, `TypeArray`
and `TypeEnum` were declared in step 1 and appear only in `dtype/` and one spill
refusal.

But the blocking problem was smaller and sharper than the namespaces.
`fileSchema` "refus[ed] the whole file if any column is unsupported", so a file
with twenty flat columns and one struct had **no readable columns at all**, and
no workaround. Nested columns are ordinary in real Parquet — anything
JSON-derived, any event stream with a tags array — so that was plausibly a new
user's first encounter with ursus on their own data.

The refusal's reasoning was right and is kept verbatim: *"Dropping an unreadable
column would give a frame that silently lacks data the user asked for, and the
most likely reaction is not to notice."* This narrows the refusal from the file
to the column, and keeps the guarantee: the column is **visible**, selecting it
fails, and selecting everything fails.

---

## 2. Two things had to change, and only one was expected

**Iterate fields, not leaves.** `fileSchema` looped `sc.NumColumns()`, which
counts LEAF columns. For a flat file leaves and top-level fields coincide, which
is why it worked; with one struct in the file they do not. It would have produced
five fields for a four-column file and named two of them `age` and `city`. Worse,
`Open` used that index as the *file column index* to open — so every read after a
nested column would have been off by one. It now walks the root group's fields
and carries a separate `leaves[i]` mapping.

**Naming a type is not the same question as reading it.** This is the one the
tests caught. `toDataType` answered both at once — what type is this, and is it a
flat readable column — and inside a nested column the second question is
meaningless: every leaf under a struct has a definition level above 1, and every
leaf under a list has a repetition level above 0, *because that is what nested
means*. So the first version returned `Struct(age: Int64, city: Null)` and
`List(Null)`: the right shape with the inside erased.

Split into `leafType` (physical and logical type, no questions about position)
and `nodeType` (recursive, total, no errors — an unnameable inner shape becomes
`Null` rather than aborting, because failing to name the inside of a column is no
reason to hide the column). `fieldType` then answers the readability question
alone, and `refusalFor` keeps the user-facing wording identical to what
`toDataType` always produced.

The mapping handles the standard three-level LIST encoding, MAP as
`List(Struct{key, value})` the way Arrow represents it, plain groups as `Struct`,
and a bare repeated primitive as `List` — testing the node's OWN repetition, not
its max repetition level, since an element inside a LIST group inherits a
non-zero level from its parent and wrapping there would give `List(List(T))`.

---

## 3. The fixture, and a dependency avoided

ursus cannot write nested Parquet (`types.go`: "Enum and the nested types are
simply not written yet"), so the fixture has to come from elsewhere.

`pqarrow.WriteTable` was the first attempt — four lines instead of forty. It
pulls **grpc, protobuf, genproto, x/net and x/text** into the ROOT module's
dependency graph, for a test fixture, in a library that otherwise depends on
arrow-go alone. The package doc already explains that pqarrow was rejected for
exactly this reason on the read side; importing it in a test would have undone
that quietly.

So the fixture uses the low-level writer with hand-written definition and
repetition levels. Forty lines instead of four, and it **documents the encoding a
later step has to decode**, which is the hard part of reading nested Parquet:

```
tags: optional group (LIST) { repeated group list { optional int64 element } }
  max def 3, max rep 1
  [10,11] -> (def 3, rep 0), (def 3, rep 1)
  []      -> (def 2, rep 0)      <- present but empty: def stops one short
  [12]    -> (def 3, rep 0)
```

`go mod tidy` also removed **eight stale indirect entries** — the duckdb Go
bindings for six platforms, duckdb-go itself and go-viper/mapstructure — which
belong to `bench/engines/go` and had no business in the library's graph.
`go get github.com/advenn/ursus` no longer mentions duckdb.

---

## 4. Teeth

**Drop the per-column refusal in `Open`.** `TestNestedColumnIsRefusedNotHidden`
fails with a panic — `index out of range [-1]` — because `leaves[i]` is -1 for an
unreadable field. In production the check is what stands between a user and that.

**Hide unreadable columns instead of naming them**, which is the design's central
choice. The failure is worth quoting because it is exactly what the original
whole-file refusal existed to prevent:

```
got 2 fields, want 4 (one per top-level field)
    {id: Int64!, name: String}
ursus: select: unknown column "tags"
  available: id, name
```

A user asking for their own data is told it does not exist. That is the silent
loss, and it is why the column stays in the schema.

---

## 5. Verification

Four new tests in `internal/source/parquet/nested_test.go`: the file opens and
names all four columns with the right types; the flat columns read with the right
VALUES (a field-index-as-leaf-index slip reads another column rather than
failing, so checking row count alone would miss it); each nested column is
refused with the unchanged message and `ErrUnsupported`; and select-all still
refuses.

Plus the full gate, and **PDS-H SF=0.1 revalidated 22/22** — every existing
Parquet path now goes through the rewritten schema walk, so flat files were the
real regression risk.

`git status bench/results/REPORT.md` checked before committing: untouched.

---

## 6. What this sets up, and what it does not

Nested columns are **visible and refused**, not usable. In order:

1. a fourth payload shape in `data.Column` — offsets plus a **child column**;
   there is no child concept today, only fixed / bits / offs+chars
2. repetition-level decoding in the reader — §3's fixture is the encoding it has
   to invert
3. `Take`/`Filter`/`Concat` over a list column, then `Explode`
4. the `.list` (37 methods) and `.struct` namespaces

Still open, unchanged: **Pivot/Unpivot** (the other v0.3 item, no type-system
work), SQL, cloud stores, plan serialization. And the whole performance ledger —
**join reordering** (no cost model exists at all, and PDS-H is 10.7x polars at
SF=1 with the multi-way joins worst), **join parallelism** (8 threads is slower
than 1), and **memory** (ursus is the most memory-hungry engine in the field:
4.7x polars at PDS-H SF=1).
