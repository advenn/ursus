# Step 275 — as built

**T of `v0.6.6-scope.md`: `Struct().Field` of several names, a regex, or `"*"`.**
Polars' `struct.field` of several names is several columns, a field each, written out
as `Unnest` is. On the way:

- a field read alone is named after the field, as Polars names it, where it was named
  after its struct, a small break;
- an `Unnest` under a `Suffix` keeps the suffix, which its field names had undone.

## 1. What changed

**`Field(names...)`** (`expr_struct.go`):

- **One plain name** is the call it was, `FnStructField`, so a plan reads as before.
- **Several names, a regex between `^` and `$`, or `"*"`** are an `Unnest` with a
  selection: `expr.Unnest` gains `Fields`. Expansion writes it out over the fields it
  selects:
  - in the struct's order, whatever the names', each field once, as Polars gives them:
    `Field("b", "a")` is a then b, and `Field("^a.*$", "a")` a and ab;
  - only `^…$` is a regex, as Polars reads it: `"^a"` is a field's name;
  - a name the struct does not have is refused, naming its fields; a regex may match
    none, and gives no column.
- **Like a selection of several columns,** it may be part of an expression:
  - `Field("a", "ab").Mul(2)` and `.Sum()` each field's own;
  - an Alias over several is refused, as Polars refuses it for its duplicate names;
  - `Suffix` names each.
- **Contexts:** `WithColumns` adds the fields after the frame's columns, and an
  aggregation takes each.
- **Rebuilding:** `Rebuild` carries `Fields`, so a selection whose struct `Expand`
  builds first, a fold's, keeps it.

**A field is named after the field:**

- **Before:** `Col("s").Struct().Field("b")` was named `s`, after its struct, by the
  call's rule of naming after the receiver. Polars names it `b`.
- **Now:** `expr.CallOutputName` names a struct field after the field. The typing,
  `OutputName` and the evaluated column all ask it.
- **Fallout:** four tests read the old name, and now read Polars':
  - three folds' fields;
  - SplitN's `field_1`, two places.
- **One test pinned the old naming:** `TestWithFieldsNamesAFieldAsPlanned`.
  `WithFields(p.Field("name"))` added a field `p`; now it sets `name`, as Polars does.
  The test checks Polars' answer: `q` is age and name, and has no `p`.
- **The CHANGELOG lists the break.** Inside an operator an expression is still named
  after its leftmost column, by ursus's documented rule. Polars names it after its
  leftmost input: `Field("c").Mul(2)` is `c` there and `s` here, as
  `Col("a").Alias("x").Add(1)` is `x` there and `a` here. That rule is wider than
  this step; `feature-gaps.md` §9 records it.

**`Unnest().Suffix(...)` keeps its suffix:**

- **The bug:** expansion named each field's copy with an Alias over the whole
  expression. Above a `Suffix` the Alias won, so `Unnest().Suffix("_x")` was each
  field's bare name.
- **The fix:** the Alias goes below the renames at the top (`nameUnder`). Polars gives
  `a_x, b_x`, and so does this.

## 2. Tests and teeth

Every answer is Polars 1.44.1's.

- **`TestStructFieldOfSeveralNames`:**
  - two names, one, a regex, a regex inside, every field;
  - a name and a regex, a name twice, a regex and its match, a regex of none;
  - an aggregate each, arithmetic each, a suffix each;
  - unnest's suffix, and one field's name;
  - WithColumns, a group's first, and a fold's fields;
  - six refusals.
- **`TestWithFieldsNamesAFieldAsPlanned`:** rewritten to Polars' answer, above.

| tooth | result |
| --- | --- |
| in the names' order reversed | **bites** |
| a pattern needs no $ | **bites** |
| a star is a regex | **bites** |
| a field it has not is skipped | **bites** |
| the alias above a suffix | **bites** |
| Rebuild drops the selection | **bites** |
| a field is named after its struct | **bites** |
| one name is a selection | silent, by design |

One name through the selection answers as the call does, the same column under the
same name. The call is kept only so a plan reads as before.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
