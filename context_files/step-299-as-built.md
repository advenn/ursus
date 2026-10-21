# Step 299 — as built

**C2: `MatchToSchema`** (`v0.6.7-scope.md` §2), as Polars' `match_to_schema`: a frame
made to a schema's columns, order and types.

## 1. The API

**`MatchToSchema(schema, MatchOptions)` on both frames.**

**`MatchOptions`** has a field for each of Polars' keyword arguments:

- `MissingColumns`, `ExtraColumns`;
- `MissingStructFields`, `ExtraStructFields`;
- `IntegerCast`, `FloatCast`.

Each takes a `MatchAction`, and two actions apply to each field:

- `MatchRaise` or `MatchInsert` for a missing one;
- `MatchRaise` or `MatchIgnore` for an extra one;
- `MatchForbid` or `MatchUpcast` for a cast.

**The zero value is Polars' default**, refusing every difference. An action in the
wrong field is refused, naming the two it may be.

**`Columns map[string]ColumnMatch` sets any option for one column,** as Polars'
per-column mappings do.

- A zero field keeps the frame-wide setting.
- `ColumnMatch.Fill` computes a missing column from the frame, Polars'
  `missing_columns={"c": expr}`.
- A name the schema lacks is refused, and so is a `Fill` beside `Missing:
  MatchRaise`.

## 2. What it does

**The answer is a `Select` of the schema's columns, in its order.** Each is one of:

- the frame's column as it is;
- the column widened;
- the column's struct fields matched;
- a null column of the type;
- a fill.

A frame that already is the schema is returned unchanged, so its plan is untouched.

**What is refused,** while the plan is built, in Polars' order:

1. the first type that does not meet, in the schema's order;
2. then every missing column, in one error;
3. then every extra one.

Each error names the column, both types, and the option that would allow it.

**`MatchUpcast` widens only where nothing can be lost:**

- a signed integer to a signed one as wide or wider;
- an unsigned to an unsigned as wide or wider, or to a signed strictly wider;
- `Float32` to `Float64`.

Never an integer to a float, an `Int64` to an `Int32`, an `Int8` to a `UInt16`, or a
`UInt64` to an `Int64`. A `Null` column meets any type. A `Decimal`'s precision and a
`Datetime`'s unit never change, as in Polars.

**A list's element widens as a column does,** through a list cast. A list of structs
whose fields differ is refused as not yet matched.

**A struct's fields are matched by name, at any depth,** with the same options:

- each schema field is read, widened through a `Field` read, or inserted as nulls,
  all through `WithFields`, which keeps a null struct null;
- then a new internal call, `struct.select_fields`, keeps the schema's fields alone, in
  its order, and keeps the struct's validity too.

It is the one new kernel:

- its typing refuses an unknown field, two of one name, and no name at all, since a
  struct of no fields has no length of its own;
- `TestEvaluatorContract` covers four argument sets;
- the call-function count is 209;
- it is not public, since no Polars method answers so.

**Only types are met;** a schema field's nullability is not checked.

## 3. Held to Polars 1.44.1

**Equal:**

- every schema and value of insert, ignore and upcast;
- a fill;
- a reorder;
- a float upcast;
- a column's own options, and a raise over a frame-wide insert;
- UInt8 as Int16, UInt32 as Int64, Int64 as Int128;
- a Null column;
- a list's element;
- a struct reordered, a field inserted, dropped and widened, its null row staying
  null;
- every refusal Polars gives, in its order: a type before a missing column, every
  missing column, every extra one, a narrower integer, Int8 as UInt16, UInt64 as
  Int64, an integer as a float, a forbidden float, text as a number, a wider Decimal,
  another Datetime unit, a missing struct field, an extra one, a forbidden field.

**Different, in `feature-gaps.md` §9: a computed fill meets the schema's type.**

- Polars keeps the expression's own type: a fill of an Int32 `b` gives an Int32 `c`
  under an Int64 schema, and a fill of a String one a String `c`. The answer is then
  not the schema the call was asked for.
- Here a fill meets the type as the frame's own column would. It is widened only as
  `IntegerCast` and `FloatCast` allow, or refused.

## 4. Spill and stream

A `Select` of per-row expressions streams. `TestMatchToSchemaUnderAMemoryLimit`
matches 20,000 rows, inserting, dropping and widening, in batches of 64 under 16 KiB,
and holds them to the run without a limit.

## 5. The site

`docs/concepts/types-and-nulls.md` has "A frame made to a schema", new in 0.6.7, on
`dev`, with a table of the options and `Example_typesMatchToSchema`. The example shows
the default refusal and an answer with the options. The Polars page maps
`match_to_schema`.

## 6. Tests and teeth

**`TestMatchToSchema`:** eleven answers, twenty refusals, five struct answers and
three struct refusals, the unchanged plan, and the eager form; and
`TestMatchToSchemaUnderAMemoryLimit`.

**Teeth, all biting:**

- a UInt64 held by an Int64;
- missing columns let through;
- extra columns kept;
- no `select_fields`, which keeps a dropped field and the old order;
- a fill unchecked;
- the frame always rebuilt;
- a null struct made a struct of nulls in the kernel;
- per-column settings ignored.

Two of them first failed to compile and were rewritten. The kernel's needed a second
import, so it was made by hand and the file restored.

**The first gate failed `TestOnlyNodeReadsTheField`.**

- The rule it holds: only `node()` reads an `Expr`'s field, so a zero `Expr` becomes
  an error node, not a nil.
- `MatchToSchema` had read it to test for an unset `Fill`. It compares with `Expr{}`
  now, which reads nothing.

**Gate:** test-all 125 ok, race 25 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
