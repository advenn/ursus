# Step 171 — as built

**F5 of `v0.6-scope.md`: `WithFields`,** now that step 146's horizontal calls exist.

## 1. What it is

`Col("p").Struct().WithFields(exprs...)` sets fields of each struct, as Polars'
`struct.with_fields` does:

- an expression named as one of the struct's fields replaces it, in place;
- one of a new name is added after the struct's fields, in order;
- each is named as any expression is, so a `Field` read, which is named after its
  struct, is aliased to set a field of another name;
- the struct's own validity is the answer's, so a null struct stays null;
- two expressions of one name would set one field twice, and are refused, as is a
  receiver that is not a struct.

**Where Polars differs:** its expressions read the struct's fields through
`pl.field()`, a context ursus does not have. Here each expression is read from the
frame, and `Col("p").Struct().Field("age")` reads a field as it does everywhere.

## 2. How

- **`expr.FnStructWithFields`** joins the horizontal family. Its first operand is the
  struct and the rest are its fields, as step 146's `struct` has. `withFieldsOut`
  types it: the struct's fields, replaced in place, then the new ones.
- **`kernel.withFields`** builds the children from the operands and the struct's own
  fields, spreading a one-row literal operand over the batch, with the struct's
  validity.
- **`evalHorizontal`** renames each operand after the first to its name in the plan,
  which is what the typing used.
  - Every shape probed (a column, arithmetic, a conditional, a literal, a field read)
    already evaluates to a column of its plan name, so the rename changes nothing that
    was found.
  - It stays because `evalHorizontal`'s own comment says the two need not agree, and
    `struct` already guards against it, positionally.

**Not reached:** a conditional over a struct is not implemented, so `When(…)`
cannot make a null struct from the public API. The null-struct case is tested in the
kernel.

## 3. Tests

- **`TestWithFields`** (root):
  - replacing `age` with `age + 1`, aliased, keeps `age` before `name`;
  - adding `city` and a literal `tag` appends them;
  - a null `age` inside a struct stays null after the arithmetic.
- **`TestWithFieldsRefuses`:** a receiver that is not a struct, and one name set twice.
- **`TestWithFieldsNamesAFieldAsPlanned`:** an unaliased `Field("name")` read, named `p`
  in the plan, adds a field `p` and leaves `name` as it was.
- **`TestWithFieldsKeepsANullStruct`** (kernel): the struct's validity carries through,
  and a literal operand is spread to every row.
- **`TestStructOfSpreadsALiteralOperand`** (kernel): `struct`'s own spreading of a
  literal. This file was created by mistake in this step, empty, by a shell append.
  Rather than deleting it, it holds this test, which nothing covered directly.
- **The contract tables** gain the new call: `TestCallFnFamiliesDoNotOverlap` counts 86,
  and the evaluation contract drives it against a fixture with no struct, where it is
  an agreed refusal.

## 4. Teeth

| tooth | result |
| --- | --- |
| a null struct made valid | **bites:** `TestWithFieldsKeepsANullStruct` |
| a replaced field moved to the end | **bites:** `TestWithFields` |
| one name set twice | **bites:** `TestWithFieldsRefuses` |
| operands named as evaluated, not as planned | **silent:** no expression found evaluates to a column of another name (§2) |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
