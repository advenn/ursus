# Step 146 — as built

**The first half of item 8 of `v0.5-scope.md`: the everyday expressions that need a
kernel.** `ConcatStr`, `Struct`, `List().Join`, `Str().Join`, and the six bit counts.

The other half (`ValueCounts`, `Cut`, `QCut`, `Describe`) is composed from what
exists, and is step 147.

## 1. Evidence first

**ursus had no way to join two strings.** There was no string concatenation at all:
not as an operator, a call or a kernel. Polars' `concat_str` is among the first
calls a user reaches for, so it got its own kernel.

**`ConcatStr` and `Struct` do not fit a `Call` as it was.** A `Call` is a receiver and
literal parameters: `CallArgs` refuses any argument after the first that is not a
literal. These two take N column-valued operands.

**A new IR node was the obvious alternative.** `Coalesce`'s comment counts what that
costs: a new node must reach the type switches in `OutputName`, `Rebuild`, the
aggregate and window extractors, and both predicate-pushdown walkers. Reading each of
those found that **every one already descends into all of a `Call`'s arguments**:

- `Rebuild` copies them;
- `extractAggs` descends into each;
- both pushdown walkers rewrite each;
- `RootNames` and projection pushdown see them as children.

So a `Call` whose arguments are all operands needs only two places taught:

- its type rule, which must read every operand's type rather than a receiver's;
- the evaluator, which must evaluate every operand.

## 2. What changed

### The horizontal family (`internal/expr/call.go`)

**A sixth family, from 600:** `FnConcatStr` and `FnStructOf`, classified by
`IsHorizontal`. Every argument is an operand, and none is a parameter.

**Typing:**

- `Call.Field` resolves every operand's field and asks `ResolveHorizontal`.
- `ResolveCall` refuses a horizontal call as internal: it sees only a receiver's
  type, so it cannot type one.
- The answer is named after the first operand, as Polars names both calls.
  `OutputName` says so too: an expansion's names come from it.

**`String()` renders a horizontal call as a function,**
`concat_str(col("first"), lit("-"), col("last"))`, not as a method on its first
operand.

### `ConcatStr(sep, exprs...)`

- **The separator is an operand:** a literal between each two expressions, so the
  kernel has no parameter, and Explain shows the call as written.
- **Any operand that can be cast to String is formatted** as that cast formats it, as
  Polars formats it. A List or a Struct is refused while the query is planned.
- **A row is null where any operand is,** as Polars' default `ignore_nulls=False`
  has it. `FillNull("")` skips one.
- **The kernel measures the rows first,** so the characters are allocated once, at
  their exact size, and a result past the 32-bit offsets is refused before it is
  built.

### `Struct(exprs...)`

`ursus.StructOf` names the type, and its doc had left the bare name free for this.

- Each field is named as its operand.
- Two operands of one name are refused, since `Field` could not tell their fields
  apart.
- The struct itself is never null, as Polars' is not: a row of nulls is a struct of
  nulls.
- A literal field is repeated to the batch's height.

### The evaluator (`physical/eval.go`)

`evalHorizontal` types the answer through `Call.Field` over the batch's schema, so a
struct's field names are the plan's, not whatever an evaluated column is called.

**Its operands go through `Eval`, not `evalColumn`.** The first version used
`evalColumn`, which broadcasts a literal to the batch's height. That copied each of
`ConcatStr`'s separators once per row before reading them, and left the kernel's own
literal handling unexercised; two teeth were silent because of it. A literal now
stays one row long, and the kernel reads it at every row.

### `List().Join(sep)` and `Str().Join(sep)`

- **`List().Join`:** a new list call, String from a List of String. Null elements are
  skipped, as Polars' default has it. An empty list joins to `""`, and a null list
  stays null.
- **`Str().Join`** is `Implode`, then `List().Join`: an aggregate, as Polars' is.
  `GroupBy().Agg` gives one row for the frame, and `GroupBy(keys).Agg` one per group.

### The bit counts

`BitwiseCountOnes`, `CountZeros`, `LeadingOnes`, `LeadingZeros`, `TrailingOnes` and
`TrailingZeros`, Polars' names. Each is a maths call and answers Uint32.

- **Counted in the type's own width,** so an Int8 1 has seven leading zeros, and a
  negative number is its two's complement within that width.
- **Zero's trailing zeros are its width.**
- **Int8 to Int64 and Uint8 to Uint64 only.** Int128 and Bool are refused while the
  query is planned.

## 3. Tests

**`horizontal_test.go`:**

- **`TestConcatStr`:**
  - nulls;
  - formatting of Int64, Float64, Bool and Date;
  - a literal operand, a lone operand, and literals only;
  - the name, with a column first and with a literal first;
  - expansion over `Col("first", "last")`, and its collision when a literal comes
    first;
  - a filter pushed down through a projection of it;
  - aggregates as operands, inside `Agg`;
  - Explain;
  - two refusals.
- **`TestStruct`:**
  - the type;
  - fields read back;
  - a literal field the struct's own length, seen through `Unnest`;
  - a struct of nulls that is not null;
  - two fields of one name refused.
- **`TestJoinStrings`:**
  - `Str().Join` over the frame and per group, where a group of only nulls joins
    to `""`;
  - `List().Join`;
  - a null list from a left join;
  - a non-String refused.
- **`TestBitCounts`:** all six counts for Int8, Uint16, Int64 and Uint64, at -1, 0, 1,
  the extremes and a few between; nulls; Float64 and Bool refused.

**Elsewhere:**

- **The evaluator contract** walks to 700 and includes the horizontal family. It
  drives each horizontal call over every fixture column, alone and with a literal.
- **`TestEveryListFunctionHonoursASlicedList`** gains a `List(String)` twin of its
  fixture, built on the same offsets, so `list.join` is swept over sliced lists. A
  cast would have rebuilt the offsets from zero, the shape that file must not test.
- **`TestCallFnFamiliesDoNotOverlap`:** 83 classified functions.
- **The contract test's comment** gives the families' real starting values, read
  from the compiled constants through a build overlay. It had said 127 for
  `FnDtYear`, stale since step 113.

## 4. Teeth

| tooth | result |
| --- | --- |
| `concat_str` ignores a null operand | **bites:** `TestConcatStr` |
| a literal operand is read at its row, not at 0 | **bites:** `TestConcatStr`, the contract |
| a struct's literal field is not repeated | **bites:** `TestStruct/unnested back` |
| a struct may have two fields of one name | **bites:** its refusal case |
| a horizontal call is named after its first column | **bites:** the expansion case |
| a horizontal call renders as a method | **bites:** the Explain case |
| `list.join` keeps null elements | **bites:** all four join cases |
| `list.join` answers a null list with `""` | **bites:** the left-join case |
| leading zeros counted in 64 bits | **bites:** `TestBitCounts` |
| zero's trailing zeros are 64 at every width | **bites:** `TestBitCounts` |
| ones not masked to the width | **bites:** `TestBitCounts` |
| a negative number not masked to its width | **bites:** `TestBitCounts` |
| bit counts accept a Bool | **bites:** its refusal case, the contract |

**Three were silent at first, each for a reason worth keeping:**

- **The literal operand** was broadcast by `evalColumn` before the kernel saw it (§2).
- **The struct's literal field** was read only through `Field`. A one-row child comes
  out of `Field` one row long and is repeated afterwards, which hides it. `Unnest`
  does not.
- **The name** was checked only through `Field` and `Rename`, both of which read the
  child's `Field`. The expansion check is what reads `OutputName`.

A first version of one tooth patched the index `broadcastIdx` already gives, and two
others did not build. All three were rewritten and rerun.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. PDS-H uses none of these calls.
