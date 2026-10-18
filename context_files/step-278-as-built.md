# Step 278 — as built

**M2 of `v0.6.6-scope.md`: integers as bits.** `And`, `Or`, `Xor` and `Not` of
integers are their bits', as Polars' `&`, `|`, `^` and `~` are. They took Booleans
only.

## 1. What changed

**The typing** (`resolveLogical`, `ResolveUnary`):

- **Two Booleans** are Kleene logic, as before.
- **Two integers** are their bits, at their common type, as arithmetic promotes
  them:
  - a UInt8 and an Int8 are an Int16, and a UInt32 and an Int32 an Int64, as
    Polars';
  - a UInt64 and an Int64 meet at Int128 here and are answered, where Polars goes
    to Float64 and refuses;
  - Int128 is taken, as Polars takes it.
- **A null of no type beside an integer** is a null integer.
- **A Boolean beside an integer** is refused, as "mixing logic and bits". Polars
  casts the Boolean and deprecates it for the 2.0 that refuses it, so this refuses
  now. The old comment's reason holds: `Col("count") & Col("flag")` is likelier a
  mistake than an intent.
- **A float** is refused, as Polars refuses it.
- **`Not` of an integer** is its type, ~x.

**The kernels** (`internal/kernel/bitwise.go`):

- **`&`, `|` and `^`** of the eight integer types, generic, and of Int128 by its
  halves.
- **Nulls:** a null operand makes a null, as arithmetic does, not Kleene. An
  integer's bits have no absorbing value a null could take.
- **`~x`:** −x−1 for a signed integer, its maximum less x for an unsigned one.
- **An all-null operand** with no values is read through `readable`, its bits as
  zero.
- **Dispatch:** `kernel.Binary` routes a logical operator by its output type; the
  unary `OpNot` by its input's.

**A literal keeps the column's type** (`ResolveBinaryNode`):

- **The case:** Polars keeps `Col("u8") & 6` a UInt8. ursus types a Go integer as an
  Int64, strongly, for arithmetic, where a sum can overflow its operands' type.
- **Why narrowing is safe here:** a bitwise result is never wider than its
  operands, so nothing can overflow.
- **How:** `And`, `Or` and `Xor` lift a Go value weakly, as `FillNullWith` does.
  `ResolveBinaryNode` types an integer beside a weak literal that fits its type
  exactly as that type.
- **One typing:** `Binary.Field` and the evaluator both ask it, where both asked
  `ResolveBinary`.
- **No fit, no narrowing:** a literal that does not fit, `Col("u8").And(300)`, is
  promoted as before.

**The optimizer needed nothing:**

- `Not(Not(x))` is x for bits too.
- The AND/OR identities fire only on a Boolean literal, and an integer's operand
  never is one.
- Predicate splitting, join-side implication and Parquet pruning walk AND and OR
  only in a predicate, which a Filter refuses unless it is a Boolean.

**Not built:** shifts. Polars has none: `pl.col("a") << 1` is a TypeError. The
gaps list's "and shifts" is gone.

**Tests moved:** `TestFieldResolutionErrors`' "and on ints" refusal becomes the two
refusals that remain, a float and a Boolean beside an integer. Resolution cases are
added for an integer's `&`, a UInt64's `|` with an Int64, and an integer's `Not`.

## 2. Tests and teeth

Every answer is Polars 1.44.1's.

- **`TestIntegersAsBits`:**
  - and, or, xor and not over Polars' frame, nulls among them;
  - a UInt8's not;
  - a constant, kept at the UInt8's type;
  - a UInt8 with an Int8, in Int16, and with an Int64;
  - not not;
  - the types;
  - Int128;
  - a null of no type;
  - Booleans unchanged;
  - four refusals, a Filter of bits among them.

| tooth | result |
| --- | --- |
| integers refused | **bites** |
| a Boolean beside an integer unnamed | **bites** |
| xor is or | **bites** |
| a null operand ignored | **bites** |
| not is negation | **bites** |
| Int128's low halves or'd | **bites** |
| a literal widens | **bites** |
| not of an integer refused | **bites** |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
