# Step 109 — as built

**An as-of join and `MergeSorted` run on every integer, float, temporal and string
key.** This is `v0.4-scope.md` item 23, `audit.md` J7, reproduced in step 108's
probe.

## 1. What was wrong

Both operators read their key as int64 ticks (`temporalTicks`), which only the
temporal types and Int64 are.

- **An as-of join on a Float, Int8 or UInt64 key** passed planning, since the
  resolver asked only for "numeric or temporal", then failed at `Collect` with
  *"column "k" is Float64 (physical Float64) and cannot be read as Int64"*.
- **`MergeSorted`** refused Float64, Int8, UInt64 and String at `Collect`, with the
  hint *"the key must be numeric or temporal"*. Three of the four are numeric.

## 2. The fix

**`orderKeys`** (`internal/physical/orderkeys.go`) reads any of these keys as int64s
in the key's own order:

- **A signed integer, or an unsigned one narrower than 64 bits,** is its value.
- **A UInt64 has its top bit flipped.** That moves [0, 2^64) onto int64's range in
  order, and keeps every difference exact modulo 2^64, so "nearest"'s `absDiffU64`
  is still the exact distance.
- **A float is its IEEE bits arranged to sort as integers,** under ursus's total
  order: every NaN is one key, above everything, and −0 is +0. `floatOfKey` undoes
  it.
- **An Enum is its category index,** which is its order.

**The as-of join** keeps its int64 search, sortedness checks and buckets unchanged.
Only "nearest" with a float key measures its distances on the floats, because an
order key says nothing about distance.

**`MergeSorted`** compares `(order key, bytes)`, so a String or Binary key merges
by its bytes, with its order key left zero.

**Both refuse what has no order key while the query is planned,** not at `Collect`:

| operator | refused |
| --- | --- |
| as-of join | Decimal, Int128, Boolean, Enum, nested types (a Decimal key's hint says to cast to Float64) |
| `MergeSorted` | Decimal, Int128, Boolean, nested types |

## 3. Tests and teeth

**`TestAsOfJoinOnEveryKeyType`:**

- **Keys:** Int8 at both its extremes, Int16, UInt32, UInt64 past Int64's range,
  Float64 with negatives, fractions and 1e300, and Float32.
- **Each joined backward, forward and nearest,** with duplicates on both sides.
- **The oracle is brute force:** for each left key, the last right key ≤ it, the
  first ≥ it, and the closer of the two, ties going backward.
- **A Decimal key** is a type error while planning.

**`TestMergeSortedOnEveryKeyType`:**

- **Keys:** Float64 with −0 and 0, Int8, UInt64 straddling 2^63, and String with ""
  and a shared prefix.
- **The oracle** is the stable sort of the two frames concatenated, which is
  `MergeSorted`'s answer: ties take the left row first.
- **A Boolean key** is a type error while planning.

| tooth | result |
| --- | --- |
| a UInt64's top bit not flipped | **bites:** both operators |
| negative floats not inverted | **bites:** both |
| "nearest" measured on order keys, not floats | **bites:** both float as-of cases |
| string keys ignored when merging | **bites** |
| a Decimal as-of key admitted | **bites** |

**Gate:** test-all 105 ok, race 21 ok, levels, vet ×3 and the bench engine tests clean; PDS-H SF=0.1 22/22 against DuckDB.
