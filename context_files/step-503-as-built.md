# Step 503 — as built

**The sub-packages' data values converted to recorded oracles and Go oracles.** Wave
503 of [`oracle-v2-scope.md`](./oracle-v2-scope.md) §3, in step 496's framework
([`step-496-as-built.md`](./step-496-as-built.md)). The recorded oracle's loader and
registry live in the root test package, which a sub-package cannot import, so where a
sub-package table's question has a public-API spelling it is asked at the root: a
supertype as the type `When/Then/Otherwise` gives two columns, an aggregate's type as
its output's, a hive key's type through `ScanParquet` of a directory the test writes
from the recorded layout, a selector's expansion as a `Select`'s columns, a cast's
value through `Cast`. Six groups, recorded by `bench/scripts/oracle/<group>.py` and
spelled in `oracle_<group>_test.go`. The sub-package test keeps what is internal: its
refusals, its laws and its mechanism. The kernel's tables of a mechanism Polars has no
view of (key bitmaps, radix rounds, a select's mask, a sum's carries, a parse's edge
bytes) are held to Go oracles over generated inputs instead: math/big, a model of the
rows, a stable sort of Go values.

The step was finished by a second agent from a first agent's uncommitted work: the
first wrote the six groups, their registries and the four non-kernel files' trims and
was cut off before committing; the second verified them (both recordings byte-identical,
every case passing or skipped as named), spelled two of the decimalagg answers around
ursus's documented rules where the first had typed ursus's values into a difference,
recorded three more cases (two findings and a refusal), pinned the two rules a
skipped finding had left unasserted, and converted the seven kernel files.

## 1. What

| file | lines before | after | hand-typed value lines before | after | cases | generated |
| --- | --- | --- | --- | --- | --- | --- |
| `dtype/dtype_test.go` | 405 | 414 | 66 | 1 | 45 (`dtype`) | 5 |
| `internal/expr/decimalagg_test.go` | 106 | 103 | 23 | 0 | 21 (`decimalagg`) | 1 |
| `internal/source/hive/hive_test.go` | 155 | 130 | 25 | 2 | 33 (`hive_infer`) | 3 |
| `selector/selector_test.go` | 115 | 57 | 33 | 0 | 37 (`selector`) | 6 |
| `internal/kernel/castbool_test.go` | 219 | 158 | 3 | 0 | 27 (`castbool`) | 2 |
| `internal/kernel/caststring_test.go` | 95 | 91 | 5 | 0 | 12 (`caststring`) | 6 |
| `internal/kernel/intkeybits_test.go` | 143 | 319 | 11 | 0 | Go oracle | 53 sets, 2,000 triples |
| `internal/kernel/radix_test.go` | 274 | 395 | 0 | 0 | Go oracle | (its own) |
| `internal/kernel/select_test.go` | 143 | 220 | 8 | 0 | Go oracle | 600 selects |
| `internal/kernel/sumcarry_test.go` | 148 | 194 | 0 | 0 | Go oracle | 40 lists |
| `internal/kernel/parse_test.go` | 425 | 504 | 0 | 0 | Go oracle | 400 texts |
| | 2,228 | 2,585 | 174 | 3 | 175 | 23 |

175 cases in six groups, 23 generated (`Gen`, seeds 5031 to 5036, 50 to 150 rows,
nulls and the type's edges), 10 refusals recorded; the JSON files are 28 KB to 174
KB, 461 KB together. `make oracle` recorded twice to the same bytes, and every other
group's recording is unchanged. The files grew by 357 lines: the kernel's Go oracles
and their generators are longer than the tables they replace.

**The three hand-typed lines that stay** each pin a documented ursus rule where
Polars 2.1 answers otherwise and the case is a finding (§4): `TestPromote`'s two
Enums of different categories meeting at the String (step 79), and `TestInfer`'s key
of dates in five layouts, a Date, each value read by its own pattern as CSV dates are;
and `TestInfer`'s key of no values, a String, which no directory can hold and so no
recording can ask.

**Per file.**

- `dtype_test.go`: `TestPromote`'s table of 24 pairs and `TestPromoteTemporalUnits`'
  eleven are the `dtype` group's `when_<a>_<b>` cases, `When(c).Then(a).Otherwise(b)`
  of two columns of the two types (`ResolveCond` meets two columns by `Promote`), and
  `TestPromoteExact`'s resolved pairs its `unpivot_<a>_<b>` cases, the value column
  `Unpivot` melts two columns to (`nodes_unpivot.go` meets them by `PromoteExact`).
  Polars 2.1 has no `Datetime(s)` and no `Time(us)`, so those pairs stay in Go. What
  the test keeps: the laws over 26 types (a type meets itself, Null meets anything at
  it, the meet commutes), the refusals, PromoteExact against its own rule over every
  pair (Promote's answer, refused where a float meets an integer of 64 bits or a
  Decimal, the cast MeetHint names permitted), and the finer unit held to Go's time
  constants over every pair of the four units in five kinds (a naive, a UTC and a
  Paris Datetime, a Duration, a Time): the unit met at is the one whose tick is the
  shorter. `TestFormatDecimal`'s eleven are held to `big.Rat`'s `FloatString` over
  them and 300 seeded unscaled values of up to 39 digits at every scale.
- `decimalagg_test.go`: the walk of every `AggOp` over a Decimal stays, each op
  refused for its stated reason, typed as over an Int64, or "recorded": the 23 output
  types it typed are the `decimalagg` group's, every aggregate of a `Decimal(10, 2)`
  by group, its type and its values (exact: the values are multiples of a quarter).
- `hive_test.go`: `TestInfer`'s 25 keys are the `hive_infer` group's, each written as
  `k=<value>/<row>.parquet`, one file a value, below a directory, and scanned with
  hive partitioning on both sides; the five layouts of one key are five cases and the
  five together a sixth.
- `selector_test.go`: the 28 expansions, the selector as an expression doubled and
  `Expand`'s two orders are the `selector` group's: the frame each selects over a frame
  of eight types, and `expand_selector`'s names as a String column; six generated
  frames of thirteen types under names of several shapes. The refusals and the
  renderings stay.
- `castbool_test.go`, `caststring_test.go`: the values are the `castbool` and
  `caststring` groups', through `Cast`: a Boolean to each of eleven number types and
  back, NaN, the infinities, −0 and a subnormal, each width's edges to text, Uint64's
  above 2^53, Int128's ends, Decimals at three precisions, generated columns of each.
  The kernel tests keep the agreement with `CanCast` (no refusal, the type promised, a
  null carried through) and `TestDecimalCastDivergenceIsClosed`'s refusals.
  `TestNanCastsToTrue` went: its four values are `castbool/float_specials_to_bool`'s.
- `intkeybits_test.go`: whether a key bitmap and a direct index are built was a table
  of eleven sets and the answer typed beside each. It is `denseRule` now, the rule
  their docs state computed in `math/big`, so a span a uint64 wraps shows, over 53
  generated key sets placed at each edge of each rule (the span a few either side of
  the anyway bound; past it, perKey times the count plus −edge, −1, 0, perKey−1,
  perKey and +edge, which floor division splits; both ends of the int64 range; a few
  keys anywhere; none) across one to three tables, a null key in some; each rule must
  answer both ways at each edge. A bitmap built is held to the set's membership, and
  `DenseIndexFits` to the rule over 2,000 generated ends and counts. The map model of
  the dense range and the index's differential against the tables stay.
- `radix_test.go`: already a differential against `NewComparator` and `ArgSort`; it
  is held to a Go oracle besides, `modelArgSort`: `slices.SortStableFunc` over the rows
  by their values as `data.Values` and `Bools` read them, false below true, NaN above
  every number, −0 equal to 0, descending reversed, nulls first or last whatever the
  direction, which shares no code with either path.
- `select_test.go`: three tests' typed rows are `TestSelectAnswersAsTheRowModel`: 600
  generated selects of Int64, Int8, Float64, String and Bool branches and of a
  payload-free null branch, each of the mask and the branches the frame's length or
  one row, the mask's payload bit random under a null; a row takes a where the mask is
  valid and true, b otherwise, and is null where the branch taken is. Every path must
  occur, the null mask over a true bit among them. `TestSelectRefusals` stays.
- `sumcarry_test.go`: the seven lists that show each wrap hazard stay, against
  `math/big`; forty generated lists of two to five values near each end of the Int128
  range, near a quarter of it, small or anywhere join them, every ordering and
  partitioning of each.
- `parse_test.go`: already `big.Int` and `big.Rat` against every listed edge; 400
  seeded texts over a number's bytes join them, a third with a byte changed, inserted
  or dropped. Two of them had an exponent of eight and eleven digits, which `big.Rat`
  refuses to compute and the oracle called malformed where the kernel says, rightly,
  not representable; the oracle reads an exponent past ten thousand itself now, as an
  overflow, or a zero for a zero mantissa or a negative exponent.

## 2. Differences named

In `oracleDifferences`, each documented in `feature-gaps.md` §9:

- `decimalagg/counts`: a count, `Count`, `Len`, `NullCount` and `NUnique`, is a Uint64
  here, Polars' a UInt32.
- `hive_infer/two_digit_year`: a key of a two-digit year, `24-01-02`, is text here,
  where Polars reads the year 2 (step 347).

Spelled around in the query: `NUnique` does not count a null, so Polars is asked it
of the values without their nulls; a `Product` is a Float64 for every number, so
Polars is asked the product of the floats the Decimals hold, exact over multiples of a
quarter, where its own `Decimal(38, 2)` product rounds 1.50 × 2.25 to 3.38; an
`ArgMin`'s position is cast to Int64 on both sides (a UInt32 there, a Uint64 here); a
`TopK`'s list is sorted on both sides. Tolerances, each with its reason in the note:
`skew_kurtosis` at rel:1e-12 (Polars raises the second moment to the 1.5, ursus
multiplies it by its square root), `entropy` at rel:1e-15 (Go's log against glibc's)
and `gen` at rel:1e-15, Polars' mean of a Decimal an ulp from the correctly rounded
quotient in two of five groups, checked against `fractions.Fraction`, where ursus
divides the exact sum.

## 3. What stays, and why

- **Refusals**, as they were: `TestPromote`'s seven pairs (text, a Boolean, an Enum
  and an instant beside a number, a Date beside a Datetime, a List, a Struct),
  `TestPromoteTemporalUnits`' zone and kind mismatches, `TestPromoteExact`'s rounding
  pairs and `MeetHint`, the decimalagg walk's refusals ("Boolean only", "a pair of
  inputs", "an integer or a Boolean"), `TestDecimalCastDivergenceIsClosed`,
  `TestSelectRefusals`, the selector's unknown names, and seven recorded refusals that
  ursus refuses too (an Enum beside an Int64, three zone mismatches, a Duration beside
  a Datetime, a Time beside a Datetime, a Boolean to a Decimal) and a bitwise aggregate
  of a Decimal.
- **Laws and agreements:** the meet's identity, null and commutativity over 26 types;
  PromoteExact against Promote over every pair; the casts' agreement with `CanCast`;
  `TestCastToStringPreservesNulls`.
- **Go oracles:** `TestFormatDecimal` (`big.Rat`), the finer unit (Go's time
  constants), the key bitmap's and index's rule (`math/big`), the radix sort
  (`slices.SortStableFunc`), the select (a row model), the Int128 sum (`math/big`), the
  parse (`big.Int`, `big.Rat`).
- **Pins of a documented rule a finding leaves unasserted** (§1): the Enum meet, the
  five date layouts, and a key of no values.

## 4. Found

Each recorded, skipped in the registry with `oracleFinding`, and the maintainer's to
decide; none is documented in `feature-gaps.md` §9 or §11, so none is named a
difference and no recording was changed. Twelve cases in six findings:

- **Polars 2.1's supertype meets more than `Promote` does** (`dtype/when_string_int64`,
  `when_bool_int64`, `when_date_dt_us`, `when_dt_ns_int64`): `when/then/otherwise` of
  a String and an Int64 is a String there, of a Boolean and an Int64 an Int64, of a
  Date and a Datetime the Datetime, and of a Datetime(ns) and an Int64 an Int64, its
  ticks. `Promote` refuses each ("Non-numeric types promote only to themselves", its
  doc; `ResolveCond` hints a cast). `dtype.Supertype`, a relaxed concat's meet since
  step 307, answers the first three as Polars does; a conditional, the arithmetic and
  a comparison take `Promote`. §9 lists the same refusal for `Unpivot` and `Append`,
  not for a conditional.
- **Two time units meet at the coarser in Polars 2.1** (`dtype/when_dt_us_utc_dt_ns_utc`,
  `when_dt_ms_dt_ns`, `when_dur_ms_dur_ns`, `unpivot_dt_ms_dt_ns`): a Datetime(us, UTC)
  beside a Datetime(ns, UTC) is a Datetime(us, UTC), its 2 ns read as 0 µs; ms beside ns
  is ms, for durations too, and an unpivot's value column likewise. `Promote` and
  `PromoteExact` meet at the finer (their docs: "no precision is silently discarded";
  `TimeUnit.Finer`), which `TestPromoteTemporalUnits` pins.
- **Two Enums of different categories meet at the String here** (`dtype/when_enum_a_enum_b`,
  step 79's rule); Polars 2.1 refuses them, "failed to determine supertype of enum and
  enum". `TestPromote` pins ursus's.
- **A Decimal cast to a Boolean** (`castbool/decimal_to_bool`): refused here, "a truth
  value is not an amount" (`TestDecimalCastDivergenceIsClosed`, step 69); Polars 2.1
  answers, true where it is not zero. A Boolean to a Decimal is refused by both.
- **`Any` and `AllTrue` of a Decimal** (`decimalagg/any_all`): refused here, "requires a
  Boolean operand" (`ResolveAggBinding`); Polars 2.1 answers `any` and `all`, true where
  a value is not zero. The same rule as the cast's.
- **A hive key of dates in five layouts** (`hive_infer/date_layouts_mixed`): a Date
  here, each value read by its own pattern, as CSV dates are (§9 says so of CSV, not of
  a hive key); Polars 2.1 reads every value by the first's layout and refuses the third,
  "could not parse '2024/01/02' with pattern 'DateDMY'". `TestInfer` pins ursus's.

Found and known: every other pasted value is Polars 2.1.0's answer too. Polars 2.1
meets a Uint64 and an Int64 at an Int128, as `Promote` does, in a conditional, an
unpivot and a relaxed concat alike, so §9's relaxed-concat line, written against
1.44.1 ("where Polars' supertype is a Float64"), no longer describes a difference;
every other hive key infers alike, the zoned instants to `Datetime(us, UTC)`; every
selector's expansion and `expand_selector`'s order are Polars'; a Uint64 above 2^53
and an Int128's ends cast to the same text.

## 5. Checks

`make oracle` recorded twice to the same bytes (`md5sum -c`, 6 of 6), and every group
recorded again left the other recordings unchanged. `go test -run TestOracle .`: the
175 cases pass or skip as named. `go test ./dtype/... ./internal/... ./selector/...`,
`go test .` in full, `go vet ./...`, `go run ./internal/gen/api -check` (0 gone;
nothing exported), `gofmt -l .` empty, all under `GOEXPERIMENT=simd GOAMD64=v3`, on
the branch and again on it merged with master's waves 500, 501 and 502, every group
recorded again there to the same bytes.
`TestEveryCitedTestExists` passes: no Go comment cites `TestNanCastsToTrue`, the one
name gone. Teeth: the radix oracle's NaN made the least bites ("the radix sort and the
Go oracle diverge at 0"); the select model taking a under a null mask bites; the
bitmap's rule read as `span > 64n`, the doc's words without the rounding down, bites
("bits per key: a bitmap is false, the rule says true"); each undone.
