# Step 79 — as built

**An Enum you can build, and use.** This is `v0.3-scope.md` §2.3. That section
said nothing in the public API builds an Enum column, and that making one buildable
without fixing its cast surface "turns a latent panic into a reachable one". It was
the sixth step of the road to 0.3.

Seven commits and this document.

Authoritative where it disagrees with the vision docs and [`design/`](./design/).

Each of these was asserted on its own exit code:

- `make test-all` exit 0, with **105** package-ok lines: 21 packages × 5 SIMD
  configurations.
- `make race` exit 0 (21).
- `make levels` and `go vet` clean in all three modules.
- PDS-H SF=0.1 exit 0, with **22/22** matching the duckdb reference.

No benchmarks. The golden plans did not move.

**The suite is 2785 passing tests and subtests** (2753 at step 78).

---

## 1. What was wrong, measured

An Enum column can be built the way the evaluator fixture said it could:
`data.NewFixed` under `dtype.Enum`, physically Uint32 indices. Built that way and
measured through a `go test -overlay` file, it was **unusable, not merely
uncastable**:

| | today |
| --- | --- |
| printing the frame | **panicked** |
| sort, group-by, unique, join, concat, shift, cum_max | **panicked**, recovered as ErrInternal |
| Cast to Int64 or Uint32 | **panicked**, recovered |
| Enum → String, String → Enum, Enum → Enum | refused |
| `e == "mid"`, `e < "mid"`, `IsIn("lo")`, `FillNullWith("lo")`, Concat or a join with a String | refused: no common type |
| CSV | neither written nor read |

Every panic had one cause. `dtype.IsString()` is true for an Enum, because an Enum is
a string logically. `kernel.Take`, `concatColumn`, the select kernel, the string
accessor the renderer uses, and `castTo`'s routing all asked it a *storage* question,
and read uint32 indices through the string accessor.

**Polars 1.44, measured:**
- Order is by category: sort, min/max, rank and `<`.
- `==` against a string is by value, and an unknown string is false.
- String → Enum refuses an unknown value when strict, and nulls it otherwise.
- Enum → UInt32 is the physical index, and deprecated.
- Concat with a String is String; a join to a String key is refused.
- CSV gets the text.

## 2. Evidence

**E1 is `enum_byhand_test.go`**: 30 cases over `[hi, lo, mid, null]` with categories
`(lo, mid, hi)`, so category order and text order disagree.
- Every answer is read off the column's indices and the type's own category list, so
  no case checks a cast with that cast.
- 28 were wrong and listed in a two-way `knownEnumDefects` ratchet.
- Two were already right: Min and Max, by category, because the extremum kernel reads
  the Uint32 physical; and `.str`'s refusal with a cast hint.
- Two cases were added after the teeth (§4), bringing it to 31.

**E2 is the evaluator contract.** It now holds an Enum column, and `noContractColumn`
no longer excuses the type.
- It joined in the first fix's commit: the fixture does not recover panics, so adding
  it earlier would have crashed the test rather than recorded anything.
- **It caught two things:**
  - Enum → Decimal reached Decimal's cast routing before the Enum's.
  - A strict ordering comparison against the fixture's String column failed on its
    data. The fixture's Enum now uses that column's own texts as its categories, out
    of order, so the comparison has an answer on every row.

## 3. The rules, and the fixes

1. **An Enum is a string with a fixed, ordered vocabulary, stored as indices.**
   - Storage questions ask `HasStringStorage`, so an Enum takes the fixed-width path
     its Physical type names. Its order is therefore the categories' order: sort,
     min/max, rank, cumulative extremes.
   - `TypedColumn[string]` decodes an Enum to its text, so the renderer,
     `df.Column[string]` and `df.At[string]` read one.
2. **Casts (`castEnum`):**
   - Enum → String decodes.
   - String → Enum encodes. Strict refuses a value outside the categories, naming it
     and them; `CastLossy` nulls it.
   - Enum → Enum re-encodes the text.
   - Enum → anything else casts the text, so `Enum("1","2")` → Int64 is 1, 2, not
     Polars' deprecated index.
   - `CanCast` promises exactly these. A number → Enum is refused, with a hint to go
     through String.
3. **Meeting other types:**
   - An Enum and a String, or another Enum, meet at **String**, which holds both
     exactly. So `==` is by value, an unknown string is false, `IsIn` follows, and
     Concat and a join on a String key work. Polars refuses that join; ursus answers
     it, by the step-74 rule.
   - **Ordering against a String meets at the Enum.** The string is cast strictly, so
     `e < "mid"` is category order and `e < "zzz"` is refused. Text order would
     contradict the column's own sort.
   - Two different Enums have no common order, and are refused.
   - A weak string literal that is a category fits the Enum, so `FillNullWith("lo")`
     keeps it.
4. **CSV** writes the text and reads an Enum schema, strictly. Parquet and Arrow are
   unchanged.

## 4. Teeth

Every patch was checked to have applied, and every one ran against a green baseline.
**All 16 bite.**

| reintroduce | fails |
| --- | --- |
| `Take` / concat / select asking `IsString` | seven row operations / concat / `FillNullWith("lo")` |
| no Enum decode in the string accessor | rendering (after the case was fixed, below) |
| Enum → String not decoding | seven cases |
| String → Enum not strict | the strict refusal, `< "zzz"` |
| Enum → Int64 by index | `Enum(1,2)`, Enum → Enum, the contract |
| the Enum routed after Decimal | eight cases |
| `CanCast` without String → Enum | three casts, the contract |
| equality meeting at the Enum | six cases, `TestPromote` |
| ordering meeting at String | `< "mid"`, `< "zzz"` |
| two Enums ordered as text | the two-Enum case (added) |
| no weak-literal fit | `FillNullWith("lo")` |
| CSV writing the index / reading no category | the CSV cases |

**Silent at first, and fixed:**

- **The rendering case was vacuous.** It looked for the categories in the whole
  printed frame. The header's type line, `Enum(lo, mid, hi)`, names every one, so
  cells printing indices passed. It now reads the cells below the header rule, and
  `df.At[string]`.
- **No case compared two different Enums.** One was added.

Both went in one commit, after the teeth.

## 5. Behaviour changes

- **An Enum column can be built** with `Cast(String → Enum)`, and printed, read,
  sorted, grouped, joined and windowed.
- **Enum casts exist both ways.** An Enum casts to anything through its text.
- **`e == "x"` compares by value; `e < "x"` by category**, refusing a string that is
  no category.
- **An Enum and a String meet at String** in Concat, joins and conditionals, unless
  the literal is a category.
- **CSV reads and writes Enums.**

## 6. Still open

- **Parquet and Arrow write of an Enum.**
- **O13:** an Enum's categories render unquoted in `DataType.String()`.
- The rest of the road to 0.3: the Decimal `+ - * /` precision rule next.
