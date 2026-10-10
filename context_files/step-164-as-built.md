# Step 164 — as built

**Item 2 of `v0.6-scope.md`: the predicate kernels.**

## 1. Evidence first

Profiles on step 163's build, SF=1, three iterations after a warm-up:

| query | filters (`filterOp.Apply`) | Kleene AND/OR (`kleene`) | `InSet` |
| --- | --: | --: | --: |
| q6 | 33% | 21% | |
| q15 | 17% | 9% | |
| q16 | 6% | | 1.6% |

**The Kleene kernel went row by row,** three closure calls a row to read two values
and two validities, and appended two bits through a builder.

**A filter applies its predicates in turn,** each over the previous ones' survivors
(step 142). But `IsBetween` is one predicate, an `And` of two comparisons, so its
second comparison ran over every row the first had already dropped, and the `And`
then combined them.

**`IsIn` on an integer column** encoded every row's key to look it up. That is 1.6%
of q16 now: the scope named it from step 141, and the steps since took most of q16's
other costs.

## 2. What changed

**Kleene AND, OR and XOR a word at a time** (`kernel/dispatch.go`). With `a` the left
value bits under the left validity, and `b` the right's:

- AND: value `a & b`; valid `(lv & rv) | (lv &^ a) | (rv &^ b)`, a known false
  deciding;
- OR: value `a | b`; valid `(lv & rv) | a | b`, a known true deciding;
- XOR: value `a ^ b`, cleared under a null; valid `lv & rv`.

Each is a few passes of `bitmap`'s word loops, which handle offsets. With no nulls on
either side the validity is all-set without any pass. A scalar side is spread over
the batch first (`spread`), and a payload-free column's value bits read as clear.

**A filter's top-level `And`s are its conjuncts** (`physical.conjuncts`). The
operator applies each side in turn, left first, so `Filter(a.And(b))` runs as
`Filter(a, b)` always has:

- a row passes `a AND b` exactly when it passes a and then b, under Kleene logic as
  under two-valued;
- an error b would raise only on rows a drops is not raised, as with two filter
  arguments;
- an `And` under anything else, an `Or` or a `Not`, stays one predicate.

**`IsIn` on an integer column compares its own values** (`kernel.IntMembers`,
`kernel.InInts`):

- once per query, the set's encoded keys are decoded back to int64s at their type:
  `keyWriter`'s 0x01 and big-endian bytes, a signed type's sign bit flipped back, and
  the value widened as `IntKeys` widens a column;
- each row's value is read from the column and compared, against a few members one by
  one and against more than eight through a map, as `InStrings` does for strings.

## 3. Measured

Step 163's runner against this one, alternated, three rounds of five iterations at
SF=1. CPU time per iteration is from rusage, warm-up included:

| query | CPU per iteration | median wall |
| --- | --- | --- |
| q6 | 706 → 546 ms (−23%) | 146 → 129 ms |
| q15 | 763 → 578 ms (−24%) | 162 → 145 ms |
| q14 | 985 → 851 ms (−14%) | 222 → 211 ms |
| q7 | 2,702 → 2,406 ms (−11%) | 523 → 495 ms |
| q16 | 300 → 277 ms (−8%) | 97 → 90 ms |
| q19 | 1,501 → 1,466 ms (−2%) | 380 → 382 ms |

## 4. Tests

- **`TestKleeneAnswersAsTheTruthTable`:** AND, OR and XOR against the row-by-row truth
  table, for every pair of nine kinds of side:
  - with no nulls, with nulls, and with mostly nulls;
  - sliced at an odd offset;
  - the scalars true, false and null;
  - an all-null column, with and without a payload.

  Payload bits are set under some nulls, which no answer may read.
- **`TestIntMembersDecodeWhatTheEncoderWrote`:** every integer width, Date and
  Datetime, at their extremes, encoded as `is_in` encodes its set, decode as `IntKeys`
  widens the same values. A key of the wrong width, or a Float64 set, is not read as
  integers.
- **`TestInIntsAnswersAsInSet`:** random columns of every width with nulls, against
  sets of 0 to 53 members, so both of `InInts`' paths run.
- **`TestAFiltersAndsAreItsConjuncts`:** nested `And`s split, left first; an `And` under
  an `Or` or a `Not` kept whole.
- **`TestAFilterOfAndsKeepsWhatKleeneKeeps`:** nested `And`s, an `IsBetween` and an `Or`
  over nullable columns, against the truth table in Go.

## 5. Teeth

| tooth | result |
| --- | --- |
| false AND null taken as null | **bites:** `TestKleeneAnswersAsTheTruthTable`, `TestNullSemantics` |
| true OR null taken as null | **bites:** `TestKleeneAnswersAsTheTruthTable` |
| a null lane's payload trusted | **bites:** the same |
| a scalar not spread | **bites:** the same |
| an `Or` split as an `And` | **bites:** both filter tests |
| an `And`'s right side first | **bites:** `TestAFiltersAndsAreItsConjuncts` |
| an unsigned member sign-extended | **bites:** both `IntMembers` tests, `TestIsInAgreesWithEq` |
| a signed member's sign bit left flipped | **bites:** the same, `TestHoistedIsInDoesNotCacheTheWrongEncoding` |
| XOR's value left set under a null | **silent, by design** |

**XOR's tooth is silent because nothing reads a null lane's value:** the kernel's own
rule, which the old XOR relied on too, since it left the bit set. It is cleared so
the three operators agree, not because anything depends on it.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
