# Step 260 — as built

**E4 of `v0.6.5-scope.md`, last part: `Expr.Unnest`,** the last of the four refusal
stubs. It is now Polars' `struct.unnest()`: a struct's fields, a column each, named
after its field. E4 is then built.

## 1. What changed

**`expr.Unnest`** (`internal/expr/unnest.go`) denotes several columns, as
`Col("a", "b")` does, and `Expand` writes it out when the plan resolves:

- a copy of the expression for each field, the Unnest replaced by
  `struct.field(name)`;
- each copy named after its field, unless the expression is aliased. An alias names
  every field alike, and a struct of two fields or more is then refused for the
  duplicate names, as Polars refuses it;
- one Unnest may stand at several places in an expression and expands in lockstep.
  Two different ones, or one beside a multi-column selection, are refused, as two
  selections are.

**So:**

- `Select(Col("person").Unnest(), Col("id"))` is age, city, id;
- `WithColumns(Col("person").Unnest())` adds the fields after the frame's columns,
  keeping the struct;
- `Col("s").Unnest().Add(1)` and `.Sum()` are each field alone.

A null struct's fields are null. A column that is not a Struct is refused while the
plan resolves. `Unnest.Field` refuses too, should one reach a place where one column
is wanted.

**Public:**

- `Expr.Unnest()`, the stub replaced;
- `StructExpr.Unnest()`, Polars' spelling.

The frame-level `lf.Unnest` is unchanged, the fields in place of the struct.

**Inside a length-changing expression** an Unnest is refused like a multi-column
selection: the zipper's `hasMatch` counts it.

## 2. Tests and teeth

**`TestUnnestIsAnExpression`.** Every answer is Polars 1.44.1's:

- alone, before a column and after one;
- in WithColumns;
- by the struct's namespace;
- plus one and summed;
- refused: a column that is not a struct, two structs, a struct beside a selection.

**`TestUnnestRefusals`** now checks an aliased Unnest of two fields, where it checked
the stub's refusal.

| tooth | result |
| --- | --- |
| the fields keep the struct's name | **bites** |
| every copy is the first field | **bites** |
| an alias names every field alike, unrefused | **bites** |
| two structs pair | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
