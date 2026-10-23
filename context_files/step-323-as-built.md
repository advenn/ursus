# Step 323 — as built

**The name namespace and `struct.drop`:** Polars' `Expr.name` and
`Expr.struct.drop` (`polars-parity.md` §2).

## 1. The API

`Expr.Name()` is Polars' name namespace, every function naming each column a
multi-column expression makes:

| Method | Polars | What it does |
| --- | --- | --- |
| `Keep()` | `name.keep` | the first **column** the expression reads, through any alias or rename: `(lit(1) + col(b))` is `b`; an expression reading none refused |
| `Map(fn)`, `Prefix(p)`, `Suffix(s)` | `name.map`, `.prefix`, `.suffix` | as `MapName`, `Prefix` and `Suffix` |
| `ToLower()`, `ToUpper()` | `name.to_lowercase`, `.to_uppercase` | every script's: `Ünï` is `ÜNÏ`; spelled as `Str()`'s are |
| `Replace(pattern, value, literal)` | `name.replace` | every match: a regular expression, `$1` a group, or the text; a pattern that does not compile refused at planning |
| `PrefixFields(p)`, `SuffixFields(s)`, `MapFields(fn)` | `name.prefix_fields` and the rest | a struct's own fields renamed, a nested struct's not, the column's name kept; a non-struct refused, and two fields of one new name |

`Struct().Drop(names...)` is `struct.drop`: the fields not named, in their order; a
name that is no field refused, as Polars' default, strict, refuses it, whose
`strict=False` has no counterpart here; a null struct stays null. Dropping every
field is refused, where Polars makes a struct of none: ursus's struct has no length
of its own without a field.

**The API is `ursus-api.md` §4's,** `expr_name.go`'s `NameExpr` and `StructExpr.Drop`,
with `MapFields` beside its `PrefixFields` and `SuffixFields`, and `Replace` taking
`Str().Replace`'s pattern, value and literal, as Polars' does.

Every rule above is Polars 1.44.1's, probed, `keep` in an aggregation and over a
window included.

## 2. How

**`expr.Rename`** carried a name function; it carries the namespace's others now:

- `Keep`, the output named by `RootNames(child)[0]`; and `Fields`, a function renaming
  a struct's fields, which `Field` types and the evaluator applies, the struct's
  validity kept.
- **A Rename with `Fields` is a computation, not only a name:** it changes the
  value's type. So `stripNaming`, which strips names from an expression substituted
  into a pushed-down predicate, keeps it: a filter reading a field the rename named
  is pushed below it and finds the field.
- **Three hand-written copies** of a Rename, two in the predicate rule and one in a
  group-by's aggregate extraction, would have dropped the new fields. They use
  `expr.Rebuild`, which copies every field, as `Unary`'s copies already did.
- **Identity.** A rename by a function its label cannot render, `MapName`'s and
  `MapFields`', gets an id as a UDF does, `NewRename`, and `Identity` reads it: a
  struct renamed by two functions is two types, in a window too, whose temporaries are
  shared by identity.

**`struct.drop`** is a struct call, `FnStructDropFields`, its `strict` the first
argument; the evaluator contract sweeps it.

A rename and a drop cost nothing per row: each renames or drops a struct's field
columns, a batch at a time.

## 3. Tests

**`TestNameNamespace`,** every answer Polars':

- the names of 17 expressions: `Keep` of a product, a sum read from b, past a
  literal, through an inner alias, before and after a suffix, of two columns, in an
  aggregation and over a window; the cases; three replaces; the old three;
- the fields of 11: each field rename, in an aggregation, outside a window's
  aggregate and inside one, under a filter pushed down that reads a renamed field,
  two `MapFields` of one struct in a `Select` and inside two windows, and `Drop`, the
  data's fields held to the schema's;
- the values: a renamed field's, a null struct kept, a dropped struct's nested field;
- refused: `Keep` of a literal and of a count, a pattern that won't compile, a
  number's fields, two fields of one name, a field not there, every field, a number
  dropped from, and a panic in a field's name.

`TestEvaluatorContract` sweeps `struct.drop`'s four argument sets, and
`TestCallFnFamiliesDoNotOverlap` counts 211 call functions, one more.

## 4. Docs

- **Selecting:** a paragraph and an example on naming, `Alias` and `Name()`.
- **Lists and structs:** the field renames and `Drop`.
- **Coming from Polars:** a row.
- **The CHANGELOG, `feature-gaps.md`, `polars-parity.md`:** the features.

## 5. Found on the way

**`Over` takes only an aggregate or a window function.** Polars computes any
expression over a window, an elementwise one being the expression itself, so its
`col("s").name.prefix_fields("p_").over("a")` answers; ursus refuses it, "is not a
window function". The tests window an aggregate, `First`, with the rename outside it
and inside it; `feature-gaps.md` records the refusal.

**A rename's id is load-bearing.** The tooth that left it out of `Identity` bit: two
`MapFields` inside two windows over one struct shared one window temporary, and the
second came back with the first's fields.

## 6. Teeth, all 10 biting

- `Keep` keeping nothing;
- two fields of one name taken;
- the fields renamed in the schema only, not the data;
- a fields rename stripped from a pushed-down predicate;
- a rename's id left out of its identity;
- an aggregation's rename rebuilt by hand, as it was;
- `Struct().Drop` never strict;
- every field dropped taken;
- `ToLower` as `ToUpper`;
- a literal replace by pattern.

**Gate:** test-all 130 ok, race 26 ok (the root package 513 s here), levels, vet ×3
and the bench engine tests clean; the 22 PDS-H answers match DuckDB.
