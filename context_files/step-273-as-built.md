# Step 273 — as built

**C of `v0.6.6-scope.md`: the categorical namespace on an Enum, and `Str()` of an
Enum.** These are Polars' `cat.get_categories`, `starts_with`, `ends_with`,
`len_bytes`, `len_chars`, `slice` and `physical`. `Str()` now reads an Enum as its
text, where it refused one.

On the way, `ursus.Len()` was found named `literal` in most plans, where Polars'
`pl.len()` is `len`. It is named `len` now.

## 1. What changed

**`Str()` of an Enum:**

- **The typing:** the string family accepts an Enum receiver, and the per-row string
  functions accept an Enum operand: `contains`, `starts_with` and the rest of an
  expression, and `to_integer`.
- **The kernels:** `kernel.StrCall`, and `HorizontalCall` for those functions, decode
  each Enum to its text first, with `castEnum`'s `decodeEnum`. So every string
  function answers an Enum as it answers the same text. That includes errors that
  name a row.
- **Faster later:** a function could run over the categories alone and be gathered by
  index, but it must then skip the categories no row uses, which may fail where no
  row does. That waits for 0.7.
- **Polars refuses:** its `.str` takes no Enum (`expected String type, got: enum`).
  This is a superset, not a difference in any answer.
- **The old refusal:** `enum_byhand_test`'s ".str is refused" case now checks the
  answer.

**`Cat()`** (`expr_cat.go`):

- **The text methods:** `StartsWith` and `EndsWith` take a constant, as Polars'
  do. `LenBytes` and `LenChars` are UInt32. `Slice(offset, length...)` with no length
  is the rest of the text.
- **They are `Str()`'s:** each delegates, so each takes a String too. Polars' `.cat`
  panics on one.
- **`Physical()`:** each value's index, at Polars' width: UInt8 up to 255 categories,
  UInt16 up to 65,535, UInt32 past them. Polars' boundaries were probed at 255, 256,
  65,535 and 65,536.
  - The kernel, `kernel.EnumPhysical`, narrows ursus's UInt32 indices, which are
    below the count.
  - `FnCatPhysical` is typed by `expr.EnumPhysical`.
- **`GetCategories()`:** the Enum's categories, a String column named as the
  expression:
  - in a selection, a column of their own length;
  - beside a column of another length, refused, as Polars refuses it;
  - in an aggregation, a list a group;
  - an aggregate over it, one value.

  This matches Polars in every context probed.

**How `GetCategories` is built** (`internal/expr/cat.go`):

- **The node:** `FnCatGetCategories` stands until the receiver's type is known.
- **Expansion writes it out:**
  - the categories become a String List literal, named as the receiver, then
    exploded;
  - where it is the whole expression, the explode is named as well;
  - below an aggregate, an alias there would hide the explode from the reshape
    lowering, which is why it is named on the literal.
- **Earlier write-out:** `Select`, `WithColumns`, `Filter` and `Agg` decide what
  changes a length, and lower reshapes, before planning. Each writes the categories
  out first (`LazyFrame.writeCategories`), resolving the input's schema only where an
  expression holds one.
- **Never evaluated:** `FnCatGetCategories` is never typed or run; both refuse it as
  internal.

**CallFns:**

- **A block of its own at 1000:** `FnCatGetCategories` and `FnCatPhysical`.
  `IsGeneral` covers it, so no dispatcher needed a new arm.
- **The names tests:** the range of 1000, sweeps to 1100, and 188 functions.
- **The contract sweep** also runs to 1100. Its fixture has an Enum column, so every
  string function is now driven over an Enum, and all agree.

**`Len()` is named `len`:**

- **The bug:** it was `Lit(1).Len()`, which took the literal's name, `literal`, in a
  `Select` after a `Filter` or a `Head`, and elsewhere. Polars' `pl.len()` is `len`.
  Found by a test of this step.
- **The fix:** `Len()` is aliased `len`.
- **No plan changes:** `Expr.Alias` of an alias now replaces the name rather than
  nesting a second alias, since the outer name wins either way.
  - So `Len().Alias("n")` is the node it was. The golden plans, and the benchmarks'
    queries that alias `Len()`, render as before.
  - Two golden plans showed the nested form until `Alias` collapsed it.

## 2. Tests and teeth

Every answer is Polars 1.44.1's.

- **`TestCatMethods`:** the text methods and `physical` over Polars' Enum of four
  categories, a null and an empty string among them; their types; and `physical`'s
  width and last index at 255, 256, 65,535 and 65,536 categories.
- **`TestGetCategories`:**
  - a selection, and its length;
  - an aggregation;
  - `WithColumns` beside a frame of its length;
  - an aliased receiver, and its length a group;
  - `Physical` in a `Filter`;
  - a `Filter` by the category count, all rows and none;
  - three refusals.
- **`TestStrOfAnEnum`:** seven string functions, one of an expression, over the Enum
  and over its text, alike.
- **`TestEnumByHand`:** `.str` reads the text, where it was refused.

| tooth | result |
| --- | --- |
| 255 categories are UInt16 | **bites** |
| 65,535 categories are UInt32 | **bites** |
| the categories reversed | **bites** |
| a selection's get_categories unnamed | **bites** |
| str reads an Enum's indices | **bites** |
| a per-row string function reads indices | **bites** |
| str refuses an Enum | **bites** |
| physical off by one | **bites** |
| Select writes no categories | **bites** |
| Agg writes no categories | **bites** |
| WithColumns writes no categories | **bites** |
| Filter writes no categories | **bites** |
| slice to the end is empty | **bites** |
| Len is named literal | **bites** |
| an alias of an alias nests (the golden plans) | **bites** |

The Filter tooth was silent at first: no test put `GetCategories` in a predicate.
The two Filter-by-count cases were added for it. They found the `Len` naming.

**A race flake, fixed:**

- **The failure:** the gate's race run failed `TestPanicsAreErrors`, "Parquet, every
  byte flipped": one of its 1,146 corrupted reads ran past its one-second deadline,
  which the test counts as a hang.
- **Load, not a hang:** this step touches no Parquet code. The case passed three times
  run alone under `-race`. The deadline expired only while the race detector ran the
  whole repository's tests at once.
- **The fix:** a read past its second is read again with a minute. A read that hangs
  hangs again; one slowed by load finishes.

**Gate:** test-all 115 ok, levels, vet ×3 and the bench engine tests clean; the 22
PDS-H answers match DuckDB. Race 23 ok, run again after the flake's fix.
