# Step 226 — as built

**F3 of `v0.6.3-scope.md`: `Replace`, `ReplaceStrict` and `ReplaceOr`, a value
map.**

## 1. Why

Polars maps values with `replace` (unmatched values keep themselves) and
`replace_strict` (unmatched values are refused, or take a default). ursus had only
the long form: a chain of `When(...).Then(...)`.

## 2. What changed

**The meaning,** so that each part has a rule ursus already states:

- the old values meet the column as `IsIn`'s do, so `Replace(old, new)` replaces
  exactly the rows `IsIn(old...)` holds, NaN matching NaN and −0 matching 0;
- **`Replace`** answers the type the conditional
  `When(x.IsIn(old_j)).Then(new_j)….Otherwise(x)` has. Its new values are weak
  literals, as `FillNullWith`'s value is: 20 into an Int32 keeps the Int32, and 2.5
  into an Int64 makes a Float64, where Polars truncates it to 2;
- **`ReplaceStrict` and `ReplaceOr`** answer the type their new values (and
  default) meet at, as Polars' replace_strict does;
- **a null:**
  - `Replace` and `ReplaceStrict` keep it null;
  - `ReplaceOr` gives it the default, as Polars' replace_strict with a default
    does;
  - `ReplaceStrict` refuses an unmapped value, naming it and its row;
- **refused:** old and new of different lengths, an old value twice (as Polars
  refuses), and `ReplaceStrict` with nothing to answer with.

**`internal/expr/call.go`:**

- three general-family constants after `FnIsIn`;
- **the arguments** are the receiver, the count of old values, the old values, the
  new ones and the default, read by `ReplaceParts`;
- `replaceOut` types them;
- `MembershipType`'s loop became `membershipOf`, which reads only the old values.

**`internal/physical/eval.go`:** `compileCall` builds a `kernel.ValueMap` once a
call, as is_in's set is built:

- the old values encoded at the meeting type, each with its position;
- the new values and default cast to the answer's type.

**`internal/kernel/valuemap.go`:** `ValueMap.Apply` picks each row's answer:

- for `Replace`, from the column cast to the answer's type with the new values
  after it;
- otherwise, from the new values;
- with one Take.

**Elsewhere:**

- **`internal/plan/fallible.go`** counts `replace_strict` as fallible.
- **`expr.go`:** the three methods, generic over the old and new values' Go types.

## 3. Tests and teeth

- **`TestReplaceAnswersAsPolars`:**
  - Polars 1.44.1's answers for integers, strings, an Int32 kept by a fitting value,
    and floats where NaN and −0 match;
  - the truncation difference marked;
  - the strict refusal naming its value and row;
  - four refusals at planning.
- **`TestAStrictReplaceIsNotPushedPastAJoin`.**
- **The contract test** drives the three, `replace_strict` with an empty map as
  is_in is driven: any fixed map meets an unmapped value in some receiver, and a
  value refusal is not a contract breach.

| tooth | result |
| --- | --- |
| a matched row keeps itself | **bites:** both tests |
| a null keeps null under ReplaceOr | **bites** |
| an unmapped value passes ReplaceStrict | **bites** |
| an old value twice admitted | **bites** |
| Replace's new values strong | **bites:** the Int32 widens |
| replace_strict not fallible | **bites:** the join test |
| the column kept at its own type | **bites:** the test and the contract |
| a match read from the column, not the meeting type | **bites:** the test, and the contract's Null column |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
