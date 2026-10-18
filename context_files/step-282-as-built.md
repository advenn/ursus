# Step 282 — as built

**M4 of `v0.6.6-scope.md`, the rest: `append`, `extend_constant`, `sample` and
`shuffle`.** This finishes M4. `RepeatBy` was built at step 263, `Rle` and `RleID` at
step 281.

## 1. What changed

**`Append(other, upcast)`, a reshape** (`ReshapeAppend`):

- **The answer:** the values, then other's, a column of its own length in a `Select`
  and a list a group in an aggregation, as Polars' are.
- **The type,** `AppendType`, a mode in the `Reshape`'s arguments:
  - `AppendUpcast` meets the two at `Promote`'s common type, as Polars' default does,
    so Int64 and Float64 are a Float64;
  - `AppendStrict` takes one type only, as Polars' `upcast=False` does;
  - a Null side takes the other's type.
- **A difference:** Polars meets text with anything as text, and `Promote` and
  `Concat` here do not, so text appended by a number is refused, with a hint to cast.
- **In a `Select`,** the zipper's frame form (`appendOf`): each side's plan, a
  constant's its row of one, cast to the `Reshape`'s type by the input's schema and
  stacked by `Concat`. Its first draft left the type to `Concat`, which refused Int64
  and Float64 and let a strict append of an Int8 through; the root tests caught both.
- **In an aggregation,** `list.append` (`internal/kernel/listmode.go`), the
  horizontal block's: each side cast to the answer's list type, then the two lists a
  row joined. The appended side is a list a group as well:
  - a literal, a list of one;
  - a generator's list, as it is;
  - another reshape, lowered;
  - a constant, its first value imploded;
  - otherwise its values imploded.

**`ExtendConstant(value, n)`:**

- **The answer:** the values, then value n times, as Polars' `extend_constant`. n is
  an int or an Expr.
- **Written as** an append of `RepeatBy`'s generator, exploded, in a third mode,
  `AppendCast`: value is cast to the column's type, as Polars casts it.
  - A float to an integer is truncated toward zero, so −1.5 is −1.
  - A value out of the type's range is null: 300 to an Int8.
  - A number to text is its text.
- **Truncation in each context:**
  - in a `Select`, by `When(v ≥ 0).Then(Floor).Otherwise(Ceil)` before a lossy cast;
  - in a list, by `truncatedFor`, which truncates each list's own elements through
    `listGather`, so a sliced list keeps its window.

**`SampleN`, `SampleFraction` and `Shuffle`, a reshape** (`ReshapeSample`):

- **The arguments:** n, or −1 and a fraction's bits; with replacement; shuffled; the
  seed.
- **In a `Select`,** the column imploded, `list.sample`, exploded. In an aggregation,
  `list.sample` a group. That kernel was built before this step.
  - Its draw is seeded by the seed and a hash of the list's values, so a group's
    sample does not depend on how its rows came in batches.
  - A fraction's count is truncated, as Polars'.
  - More than there are without replacement is refused, as Polars refuses it.
  - Unshuffled, the drawn values keep their order.
- **`Shuffle(seed)`** is all of them, shuffled.
- **The values are ursus's,** not Polars', whose generator is its own (§6.3).

**The sweeps:**

- `FnListAppend` joins the horizontal block;
- the names test counts 205;
- the contract drives `list.append` upcast and cast.

## 2. Tests and teeth

Every answer is Polars 1.44.1's, but the drawn values.

- **`TestAppendAndExtend`:**
  - append, of a constant, by group, of a constant by group;
  - extend_constant, by a null, a float truncated, a negative truncated;
  - past an Int8, a number to text, n an expression;
  - extended by group, a float truncated by group;
  - the types;
  - a nullable side's answer nullable, verified;
  - without upcast refused, text and a number refused.
- **`TestSampleAndShuffle`:**
  - sizes of 3 of 6, half, 0.4 of 6 truncated, 10 with replacement, and shuffled;
  - each drawn value the column's;
  - one seed drawing alike twice, and seeds 7 and 8 apart;
  - a shuffle each value once, not in the order it was;
  - all of them unshuffled in order;
  - 2 a group, 0.4 of a group of 3;
  - too many without replacement refused.

| tooth | result |
| --- | --- |
| a float floored | **bites** |
| strict as upcast | **bites** |
| a list's float floored | **bites** |
| a list's sides swapped | **bites** |
| a constant appended twice a group | **bites**, once a group case was added |
| a shuffle sorted | **bites**, once a check of the order was added |
| unshuffled left drawn | **bites** |
| the seed ignored | **bites**, once two seeds were compared |
| a fraction rounded up | **bites**, once 0.4 of 6 was added |
| Shuffle unshuffled | **bites** |
| extend_constant once | **bites** |
| the appended side's nulls not typed | silent |

The silent tooth drops the other side's nullability from the `Reshape`'s own type.
Nothing reads that flag: a `Select` that reshapes is the zipped plan, typed by its own
`Concat`, and an aggregation is typed by the lowered `list.append`. So no test can
see it. The flag is kept true to the answer, and a test of the `Select`'s schema and
of a verified run holds the plan's own.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
