# Step 74 — as built

**A join key is compared exactly.** This is `audit.md` §11 item 6, the join promotion
paths — J1, J2, O6 and J8 — with J3, J5, J6 and J10's hint, which share its question:
**at what type is a key compared?** It is the first step of the road to 0.3, whose
remaining steps are listed in the plan that opened it.

Nine commits and this document.

Authoritative where it disagrees with the vision docs and [`design/`](./design/).

Each of these was asserted on its own exit code:

- `make test-all` exit 0, with **105** package-ok lines: 21 packages × 5 SIMD
  configurations.
- `make race` exit 0 (21).
- `make levels` and `go vet` clean in all three modules.
- PDS-H SF=0.1 exit 0, with **22/22** matching the duckdb reference.

No benchmarks. The golden plans did not move.

**The suite is 2624 passing tests and subtests**, counted from `go test -json`'s pass
events. Earlier steps' counts came from a different tool, which no longer runs in this
environment, so 2624 is not comparable with step 73's 2602.

---

## 1. The rule, measured first

The reference engines disagree. Measured with Polars 1.44, DuckDB 1.5 and PyArrow 25:

| | Polars | DuckDB | PyArrow |
| --- | --- | --- | --- |
| Int64 key vs Float64 key | refused | **matches 2^53+1 to 2^53** | refused |
| Int32 vs Int64, Int64 vs Uint64 | exact widening, to Int128 for the latter | the same | refused |
| a Datetime(ms) past the ns range vs Datetime(ns) | refused at plan time | refused at run time, for that value | refused |
| a null as-of by-key | matches nothing | matches nothing | matches null, buggily |
| Validate 1:m with an unmatched duplicate | refused | — | — |
| strict Concat of Int64 and Float64 | refused | lossy | refused |

So ursus's "exact or refused" rule gives this:

- A join key, a Concat column and an Unpivot value meet at a type that holds **both**
  exactly: `dtype.PromoteExact`.
- Keys are cast there strictly.
- A pair with no such type is refused at plan time, with a cast the caller can write.

**Arithmetic and comparisons keep `Promote`**, where rounding is expected.

## 2. Evidence first, in two commits

**E1 is 18 cases answered by hand** (`join_byhand_test.go`), with a two-way ratchet.
13 were wrong:

| | today |
| --- | --- |
| J1, AsOfBy Int32 vs Int64 | no match: the by-keys were never cast |
| J2, a year-3000 Datetime(ms) key vs Datetime(ns) | a null key: `ErrInternal` on a non-nullable key, silent on a nullable one, and **matched to a null** under NullsEqual |
| J3, a spilled right join with key (1, null) | k1 came back null |
| J5, 1:m with unmatched duplicates, and with null duplicates under NullsEqual | passed |
| J6, null as-of by-keys | matched each other |
| J8, an Int64 join to Float64, and Concat and Unpivot of the two | 2^53+1 matched or became 2^53 |
| O6, `k*k > 2e9` over a coalesced key of Int32 and Int64 | 0 rows; the answer is 1 |
| J10, UTC vs naive | the hint said `.Cast(ursus.Int64)` |

**Two of my first fixtures tested nothing**, and both were caught before they were
committed:

- **The J3 case did not spill at first.** A single-batch `Frame` never crossed the
  limit, and once it did, the filter that picked out the row was pushed into the build
  side and shrank it to one row. The case now builds the right side from sixteen
  frames, asserts `MemoryStats.Spills > 0`, and keeps its filter reading both sides.
  With that it reproduced; before it, it "passed".
- **The J1 case had the narrow by-key on the left**, so leaving the right side uncast
  changed nothing, and that tooth was silent. A swapped case was added after the
  teeth, in its own commit.

**E2 is `TestJoinKeysCompareExactly`.** It joins and concatenates every pair of
numeric key types, 121 pairs, over each type's boundaries and the points where a float
stops holding every integer.

- **Joins:** the expected matches are the pairs equal as exact rationals, compared
  with the join's own (left row, right row) multiset.
- **Concat:** every value must come back exactly.
- **Today:** 109 pairs were right, and the 12 that pair a 64- or 128-bit integer with
  a float joined and stacked at Float64.
- **Coverage:** the type list is checked against the TypeID names. Uint128 is excluded
  by name: it is declared, and no column of it can be built.

My first version built its columns by parsing strings, which cannot reach Int128 (S21),
and compared a Float32's shortest text as an exact value. Both were fixture mistakes,
found and fixed before the commit.

## 3. The fixes

1. **`PromoteExact`.** It is `Promote`, except that an integer wider than 32 bits
   with a float has no type. Joins, Concat and Unpivot use it.
   - `MeetHint` names a cast that makes the two meet: the float side to the integer,
     which is exact or refused, or both sides to Float64.
   - For a zone mismatch, `MeetHint` names a cast to the other side's type, not to
     Int64.
   - `resolve_union.go`'s "holds both exactly" claim is now true.
2. **`castKey`.** Keys are cast strictly. A value the compared type cannot hold is
   KindValue, naming the key and the type, as the as-of key already was. `gatherOut`
   uses the same cast.

   The as-of by-keys take `KeyTypes[1:]`; they used to take nil.
3. **A null as-of by-key matches nothing.** Right rows with one join no bucket, and a
   left row with one gets nulls. `AsOfBy`'s doc says so.
4. **`checkSeen` runs for every key this level decides**, resident or matching
   nothing. A key routed to a spilled bucket is still the sub-join's to police, so
   the map is not O(all probe keys) at every level.
5. **`nullPadOp` coalesces from the right on a Right join.** It assumed "a null-keyed
   build row has a NULL key", which is true of one key and false of several.
6. **A pushed coalesced key is cast to its output type** when the side's type
   differs. The optimizer differential's O6 count, 1 since step 70, is now 0.

## 4. Teeth

Every patch was checked to have applied, and every one ran against a green baseline.

| reintroduce | fails |
| --- | --- |
| `PromoteExact` returns `Promote` | the sweep, the three J8 cases, `TestPromoteExact` |
| keys cast non-strictly | the three J2 cases |
| the right side's by-keys uncast / the left side's | J1 swapped / J1 |
| `checkSeen` skipped for unmatched keys | both J5 cases |
| `nullPadOp` pads from the left | J3 |
| the pushed key not cast | O6 |
| both as-of null checks removed | J6 |

**Silent, and why:**

- **Either as-of null check alone.** Each covers the other: a null left key that is
  encoded finds no bucket once the null right rows are excluded, and the reverse.
  Removing both bites.
- **`gatherOut`'s strict cast.** `castKey` has refused the same values first.
- **The right-side by-key tooth, until the swapped case existed** (§2).

## 5. Behaviour changes

- **A join of a 64- or 128-bit integer key with a float key is refused at plan time**,
  as are Concat and Unpivot of such columns. The hint names the cast.
- **Int32 or Uint32 with a float meets at Float64**, and a narrower integer with
  Float32 at Float32, as before.
- **A temporal key the compared unit cannot hold is a KindValue refusal**, in an equi-
  or as-of join. It used to be a null key.
- **As-of by-keys of different integer types match.** A null by-key matches nothing.
- **Validate 1:m and 1:1 refuse a duplicated left key whether or not it matches**, and
  duplicated null keys under NullsEqual.
- **A spilled right join keeps the non-null half of a partly-null key.**
- **A filter on a coalesced key of two widths sees the key's output type** when it is
  pushed.

## 6. Still open

- **J9:** Full with `JoinCoalesce(true)` is still refused.
- **J11, J12.**
- **`WhereExists`'s `JoinSuffix` advice** (the rest of J10).
- **A comparison** `Col(i64) == 9007199254740993.0` still meets at Float64, and so do
  `When` and `Coalesce` of Int64 with Float64. Those follow `Promote`, where the rule
  for arithmetic applies.
- The rest of the road to 0.3: aggregations and windows next (A2–A5, A14, W1).
