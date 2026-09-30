# Step 73 — as built

**No float64 go-between, and `strict` threaded through.** This is `audit.md` §11
item 5, which is §8's patterns 2 and 7: S1, S2 and S4. The same bug turned up in four
more places, and those are fixed too.

Nine commits and this document.

Authoritative where it disagrees with the vision docs and [`design/`](./design/).

Each of these was asserted on its own exit code:

- `make test-all` exit 0, with **105** package-ok lines: 21 packages × 5 SIMD
  configurations.
- `make race` exit 0 (21).
- `make levels` and `go vet` clean in all three modules.
- PDS-H SF=0.1 exit 0, with **22/22** matching the duckdb reference.

No benchmarks. The golden plans did not move. The suite is at **2602** tests.

---

## 1. One helper, three mistakes

`narrow` decided every conversion that touched a float with one test:
`t := T(v)`, and the value was lossy if `float64(t) != v`. That test is right for one
thing and was applied to three:

| what it was given | what went wrong | |
| --- | --- | --- |
| **a string**, parsed into a float64 first | the test compared a value that had already rounded with itself: `"9007199254740993"` → …992 | S1 |
| | `"18446744073709551615"` → Uint64 could not be parsed at all, because every integer went through `ParseInt` at 64 bits | new |
| | the hand-off passed `strict=false` whatever the caller asked, so `"256"` → Uint8 under `Cast` was a null, or `ErrInternal` in a non-nullable column | S4 |
| **the result of a Float32 computation**, which should round | every inexact result was a null: `sqrt(2)`, `ln(2)`, `Round(0.1, 1)` | S2 |
| **a cast to a float** | rounding, which is what a float is, was refused or nulled: 0.1 → Float32; and what did convert rounded twice | S2 |

The two users of the double rounding were new findings too:

- **`i128.Int128.Float64`** was `float64(Hi)*2^64 + float64(Lo)`, which rounds each
  half and then the sum. 2^64+2^63+2^11+1 came back as 2^64+2^63; the nearest float64
  is 2^64+2^63+2^12.
- **Decimal → Float32** went to the nearest float64 and then to a float32.

The design found one more, the same bug one routing line away. `castTo` sent
integer pairs to `castInt` only when both types' logical `IsInteger()` was true, and it
is false for every temporal type. So Datetime(ns) → Uint64 went through a float64, and
every nanosecond timestamp, all of them past 2^53, came back rounded to a multiple of
256.

## 2. The rule, which you chose

**A cast to a float rounds once, to the nearest value of the target's width.** The
only value that is not representable is a finite one whose nearest float is an
infinity: `Cast` refuses it and `CastLossy` nulls it. NaN and ±Inf stay what they are,
and underflow is rounding.

The three engines differ here. Polars and PyArrow give inf for 1e39 → Float32; DuckDB's
`CAST` refuses it. **Every one of them gives inf for the string `"3.4e39"`, and ursus
refuses it.** That is the rule applied to strings as it is to numbers.

The engines agree, and ursus now agrees with them, on:

- string → integer parses exactly;
- `"256"` → UInt8 is an error when strict and null otherwise;
- string → Float32 parses directly to a float32, rounded once;
- Float32 sqrt, cbrt, exp, log1p and round stay Float32, equal to `float32(f64 result)`.

**Float → integer is unchanged:** exact, or refused. J2, the join's two non-strict
key casts, is the join step's.

## 3. Evidence first, in three commits

**E1: 47 cases answered by hand** (`castround_byhand_test.go`). 36 were wrong and 11
controls were right:

- ten casts to a float;
- the two temporal casts;
- thirteen string parses;
- seven maths cases, including a constant-folded `Lit(float32(2)).Sqrt()`, which the
  optimizer had folded to a null literal;
- four `IsIn` cases. `IsIn("300")` on Int8 silently matched nothing, because the
  needle became a null.

**E2: three sweeps, generated, with counted ratchets.** Each ratchet counts the wrong
values per pair, so a partial fix moves the number instead of hiding behind a name.

- **`TestStringParsesAreExactOrRefused`** parses every string into every numeric
  type a String casts to, **derived: 10**, against `big.Int` and `big.Rat`.
  - The strings are every integer type's edges in several spellings; the midpoints
    between adjacent floats of each width, nudged by a hair either side, far below a
    float64's reach; overflow; underflow; the specials; and malformed text, which the
    oracle must reject too.
  - All ten targets were wrong.
  - **The plan predicted nine.** It expected Float64 to be right. It had 7 wrong: an
    overflow was refused as "cannot parse".
- **`TestCastsToAFloatRoundOnce`** casts every numeric, temporal and Decimal source
  to both floats, against `big.Rat`'s own nearest float.
  - The values sit at and either side of each width's midpoints, and around the
    Float32 overflow threshold 2^128 − 2^103.
  - 20 pairs were wrong: every cast to Float32 but the exact ones, and Int128 → Float64.
- **`TestTemporalIntegerCastsAreExact`** covers every temporal type ↔ every integer
  width, both ways: 8 pairs were wrong.

Two guards landed green. `TestFloatToIntegerCastsAreExactOrRefused` covers the range
check still to come. And the step-69 strict loops now check the value a strict cast
returns; they used to check only that it did not refuse.

**E3: the Float32 twin, and a nullability half.**

- `TestFloat32MathMatchesStdlib` is `TestMathMatchesStdlib` at Float32, compared by
  bit pattern against `float32(math.F(float64(x)))`. `TestFloat32RoundIsTheFloat64RoundRounded`
  does the same for Round.
- The evaluator contract now fails a null in a column its Field declared non-nullable.
  Its fixture declared every column nullable, which made that question unaskable. It
  found exactly the six Float32 maths functions and nothing else.

**Every ratchet is empty.**

## 4. The fixes

1. **A computation rounds** (`floatResult`). The result is stored at the output's
   width and never nulled. An overflow is +Inf, as float32 arithmetic gives it.
2. **Int128 rounds once.** `top64` keeps the magnitude's 64 most significant bits and
   ORs the rest into bit 0, so Go's single uint64 conversion is the single rounding.
   `Float32` is new, and is not `float32(Float64())`.

   The counted ratchet showed a side effect: Int128 → Float32 went from 332 wrong to
   344. The correct float64 fails narrow's round trip more often than the
   double-rounded one had, and that pair was the next fix's.
3. **A string parses at the target's width** (`castparse.go`).
   - `ParseInt` and `ParseUint` at each integer width, and `ParseFloat` at 32 or 64.
     The parse is the conversion.
   - Unsigned targets keep the sign grammar, so `"+5"` and `"-0"` parse and `"-1"` is
     out of range.
   - A strict cast says "cannot parse" for text that is no number, "not representable"
     with the target's range for a number that does not fit, and gives the float
     refusal for an overflow.
   - **The sweep caught `strconv` reporting a range error before it checks syntax.**
     `"16777217.5"` was "out of range" for an Int8, though it is no integer at all.
     `decimalInteger` decides first.
4. **A cast to a float rounds once** (`castToFloat`).
   - Each value converts from its own type, and Decimal through `decimalToFloat32`.
   - `fromFloat64` and `narrow` are gone. What is left is `floatToInt` and
     `floatToInt128`, both with a float source.
   - `IsIn(0.1)` on Float32 matches.
5. **Integer pairs route by physical type**, so a tick count is an integer.
6. **A float → integer cast checks its range before converting.** Go leaves an
   out-of-range conversion to the platform. On arm64 it saturates, so 2^63 → Int64
   would round-trip as MaxInt64.

**A strict cast was the backstop for weak-literal exactness, and is not any more.**
Until now, a `FitsExactly` that wrongly admitted 0.1 to Float32 failed loudly in
`evalCond`'s strict cast; it would now round silently. The docs say so, and five new
`never` cases pin exactness at a float's width.

## 5. Teeth

Every patch was checked to have applied, and every one ran against a green baseline.
**One did not compile**: removing the overflow check left `math` unused. It was
re-aimed.

| reintroduce | fails |
| --- | --- |
| a computation nulls an inexact Float32, or nulls an overflow | by-hand maths cases, the twin, the contract |
| Round spoils an inexact Float32 | the Round twin, by-hand Round |
| Int128's two-halves Float64; no sticky bit; `Float32` via `Float64` | the i128 test, the float sweep, by-hand Int128 |
| Int64 parsed through a float64 | the parse sweep, S1, ToInteger, IsIn |
| strict dropped out of range | the parse sweep, `"256"`, `"-1"`, IsIn("300") |
| Float32 parsed through a float64 | the parse sweep's midpoints, 0x3f800001 |
| a float overflow called malformed | the sweep's message class, `"1e400"`, `"3.4e39"` |
| no sign handling for unsigned | `"+5"`/`"-0"`, `"-1"` |
| underflow refused | `"1e-50"`, the sweep |
| range before syntax | the parse sweep |
| integers → Float32 through a float64 | the float sweep, by-hand 2^53+2^29+1 |
| no overflow check; overflow as `\|v\| > MaxFloat32` | 1e39 controls; 3.4028235e38, the sweep |
| Decimal → Float32 through a float64; fast path to 2^53 | the float sweep |
| strict ignored in the overflow | the 1e39 control, the sweep |
| `FitsExactly` admits any finite Float32 | both weakfit tests |
| logical `IsInteger` routing | the temporal sweep, both temporal cases |
| the range guard removed, **with the conversion patched to saturate as arm64's does** | `TestFloatToIntegerCastsAreExactOrRefused` at 2^63 and 2^64 |

**Silent, and why:**

- **The range guard removed on its own.** amd64's out-of-range conversion never
  survives the round trip, so the test cannot see the guard on this machine.
  **No runner here is arm64.** The saturating control shows the test would catch it
  there, and that the guard does not break it.
- **Integers of 32 bits or fewer converted through a float64** were not tried on their
  own. They are exact in a float64, so they are silent by construction; the tooth over
  all widths bit through the 64-bit sources.

## 6. Behaviour changes

- A cast to Float32 rounds. 0.1, every Int64 and Int128, and every Decimal convert, and
  none is refused. A Float64 between MaxFloat32 and the overflow threshold rounds down
  to MaxFloat32.
- Int128 and Decimal convert to either float with one rounding. **The public
  `i128.Int128.Float64` is one ulp different at |v| ≥ 2^64**, and so are aggregates over
  Int128 that go through it. `Float32` is new.
- String → integer is exact at every width, including Uint64 past MaxInt64. A value
  out of range is refused by `Cast` with the target's range; it used to be null, or
  `ErrInternal` in tests.
- String → float rounds once. An overflow is refused by `Cast` and null under
  `CastLossy`; it used to be "cannot parse". Underflow rounds.
- Float32 Sqrt, Cbrt, Exp, Ln, Log10, Log1p and Round return the float32 nearest the
  Float64 result. `Exp(89)` is +Inf.
- `IsIn` and `list.contains`: a float needle rounds to the column's width, and a
  string needle parses at it.
- A temporal tick count casts to an integer of another width exactly.
- Constant folding gives values where it gave null literals.

## 7. Still open

- **The join promotion paths**, §11's next item: J1, J2, O6 and J8.
- **S6.** `IsIn` casts the set to the column's type, so `IsIn(0.1)` on a Float32
  matches where `Eq(0.1)` does not. Its refusals still say "use a non-strict cast".
- **S25, new.** String → float accepts Go's `"1_000"` and hex floats; Polars rejects
  both. The parse sweep skips underscore strings for floats rather than pin either
  answer.
- **S21.** String → Int128 is refused, and `i128.Parse` does not detect overflow.
- **Enum → numeric.** `CanCast` allows it, because `IsString` includes Enum, and it
  reaches `parseFromString`. Not examined in this step.
- **I24, J8 and S18.**
- **Rows in cast errors are numbered within the batch** — A15's shape, for casts.
- README debt.
