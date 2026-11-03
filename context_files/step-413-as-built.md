# Step 413 — as built

**`FromRows` of a recursive struct type is refused, naming the field.** H1 of
`v0.7.2-scope.md`, the plan's 420.

## 1. What was wrong

`FromRows` walks its struct type to build a column a field, through a pointer, a
slice or an array to the type within, and a nil pointer's zero value in turn. A type
that holds itself, a linked list's `Next *node` or a tree's `Kids []tree`, was walked
forever, however few rows, even none: `fatal error: stack overflow`, which no recover
catches, and the process ended. `MapRows` builds its answers so, a struct `R` through
`FromRows` and any other `R` as a struct's one field, and ended the same way. Such
types are common in Go, and `FromRows`' doc says a field type with no column type is
refused, naming the field.

## 2. The change

- **`refuseRecursive`,** before either builds a column: `recursiveField` walks the
  fields `structColumns` reads, exported and not tagged `ursus:"-"`, through what
  `encodeField` goes through, and names the field, "T.F", through which a struct holds
  a type it is within. The answer is an `ErrType`, "field T.F holds its own type: a
  recursive type has no column type", with a hint to leave the field out or flatten
  the type.
- **Once a type, not once a path:** a type found to hold no cycle is not walked again,
  so a type reaching one type by many paths is walked in time linear in its types.
- **Not a change:** `Rows` and a `MapRows` input decode by the frame's columns, which
  their depth bounds.

## 3. Tests

- **`TestFromRowsRefusesARecursiveType`** (root): a linked list, a tree, two types
  holding each other, a pointer to an array of the type itself, and no rows, each an
  `ErrType` naming the field; `MapRows` to a linked list and to a pointer to one; a
  type recursive only through a field tagged `ursus:"-"` and an unexported one reads.
  Before the change the test binary ended with the overflow, under a 64 MB stack.
- **`TestRecursiveFieldWalksEachTypeOnce`** (in the package): thirty-two levels each of
  two fields holding the level below, 2^32 paths and no cycle, walked at once.

**Teeth: 6 of 6 bite:** `FromRows` unchecked and `MapRows` unchecked each end the
binary by the overflow, as does a walk that skips slices or arrays; a walk reading a
left-out field refuses `recSkipped`; a walk with no memory of clean types times out
on `dag32`.

**Gate:** the root package's row, struct, array, list and example tests, vet; CI's
every width, the scalar build and the race detector on the pull request.
