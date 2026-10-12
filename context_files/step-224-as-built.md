# Step 224 — as built

**F1's last item and the first of F6 in `v0.6.3-scope.md`: `List().ToStruct(names...)`
and `Struct().RenameFields(names...)`.**

## 1. Why

- **Polars' `list.to_struct`** turns each list's first elements into fields. A
  plan's schema cannot wait for the data to say how long the lists are, so ursus
  takes the field names, as Polars does when given `fields`.
- **`struct.rename_fields`** renames a struct's fields in order.

Both take only constant names, so both are ordinary calls with literal arguments.

## 2. What changed

**`internal/expr/call.go`:**

- `FnListToStruct` ends the list family, and `FnStructRenameFields` follows
  `FnStructField`.
- `callFieldNames` reads the names and refuses two alike.
- **to_struct** is a Struct of the element type, one field a name, and needs at
  least one name.
- **rename_fields** must name every field. Polars renames as many as it is given
  and drops the rest, warning that 2.0 will refuse it; this refuses it now, as a
  schema error naming the counts.

**The kernels:**

- **`listToStruct`** (`listfn.go`) takes each field as a Take of element i: null
  where the list is shorter, and the list's validity is the struct's.
- **rename_fields** (`structfn.go`) renames each field column and keeps the
  struct's validity.

**`expr_list.go`, `expr_struct.go`:** the two methods. The contract test drives
both, and the names test counts 98.

**One difference from Polars:** a null list is a null struct, where Polars gives a
struct of nulls. A null list is not an empty one anywhere else in the namespace.

## 3. Tests and teeth

- **`TestAListsToStructAnswersAsPolars`:**
  - Polars 1.44.1's answers, with that difference;
  - in batches of one, whose lists are slices, and of five;
  - no names, and one name twice, refused.
- **`TestAStructsRenameFieldsAnswersAsPolars`:**
  - Polars' answers, and the renamed field reads back;
  - too few names, too many, and one name twice, refused.

| tooth | result |
| --- | --- |
| a short list's missing element read past its end | **bites:** the to_struct test and the contract |
| a null list a struct of nulls | **bites** |
| element i read from the list's start only | **bites** |
| a renamed struct loses its validity | **bites** |
| rename_fields takes too few names | **bites:** the test and the contract |
| two names alike admitted | **bites:** both tests |
| to_struct with no names admitted | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
