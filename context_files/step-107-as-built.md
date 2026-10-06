# Step 107 — as built

**Comparisons read a scalar in place and write a word at a time.** This is the part
of `v0.4-scope.md` item 16 (filters without copying) that step 101's profile
pointed at directly.

## 1. What was wrong

Every comparison except Float64 against a constant, which has SIMD kernels, went
through `cmpNum`, which did two wasteful things:

- **It copied a scalar side n times** (`broadcastVals`) before comparing. That was
  190 MB of PDS-H q7's allocation at SF=1, for one filter's date literals.
- **It appended its result one bit per call,** through `bitmap.Builder.Append`.

**Measured** (`BenchmarkCompareInt32`, 8,192 Date values):

| | per batch | allocated |
| --- | --- | --- |
| against a literal | 53–66 µs | 34 KB |
| against a column | 44–57 µs | — |

That is about 7 ns a row.

## 2. The fix

**`cmpWords`** (`internal/kernel/cmpwords.go`):

- **A scalar on the right is held in a register.** One on the left swaps the
  operands and flips the operator, since c < x is x > c.
- **Two broadcast scalars give one answer, appended n times.** `Binary` never asks
  for this, because two one-element operands make a one-row answer. But a
  both-scalar call must not flip back and forth for ever if anything does ask,
  which a tooth demonstrated with a stack overflow.
- **Results are packed into a `uint64`** and appended 64 at a time, LSB first:
  Arrow's bit order and the Builder's.
- **Floats keep IEEE's order** (NaN compares false), because the loops are Go's own
  operators. Sorting and grouping keep using the total order elsewhere.

`cmpNum` is now one call to it. `eqScalar` through `geScalar` had no other callers
and are gone. The `...Const` loops stay, as the Float64 SIMD kernels' fallback.

## 3. Measured

`BenchmarkCompareInt32`, four runs each:

| | before | after |
| --- | --- | --- |
| against a literal | 53–66 µs, 34 KB | 14–16 µs, 1.5 KB |
| against a column | 44–57 µs | 13–15 µs |

About 3.5× in both cases.

Whole queries at SF=1, best of two runs, on a machine under load (load average 7),
so indicative only:

| query | before | after | allocated |
| --- | --- | --- | --- |
| q6 | 433–502 ms | 378–391 ms | 137 MB less |
| q7 | within noise | within noise | 275 MB less |
| q19 | within noise | within noise | |

## 4. Tests and teeth

**`TestCompareWordsAtEveryBoundary`:**

- **Types:** Int8 with its minimum, Int64 with its maximum, UInt16, and Float32 and
  Float64 with NaNs.
- **Operators:** all six.
- **Shapes:** column against column, column against scalar, scalar against column,
  scalar against scalar.
- **Lengths:** 1, 2, 63, 64, 65, 127, 128, 129 and 1000, around the word
  boundaries.
- **Reference:** Go's operators. It ran at `GODEBUG=simd` 0, 128 and the default.

**`TestCompareWordsBothScalar`** calls the both-scalar branch directly, at 2, 64 and
130 rows.

| tooth | result |
| --- | --- |
| a left scalar's operator not flipped | **bites** |
| a result written one bit off | **bites** |
| the last word appended whole instead of trimmed | **bites** |
| the both-scalar branch removed | **bites:** the stack overflows |

**Gate:** test-all 105 ok, race 21 ok, levels, vet ×3 and the bench engine tests clean; PDS-H SF=0.1 22/22 against DuckDB.
