# Step 504 — as built

**No new hand-typed test values: a ratchet in CI, and the oracle scope's final count.**
The last row of [`oracle-v2-scope.md`](./oracle-v2-scope.md) §3. Waves 496 to 503 put
2,598 of Polars 2.1.0's answers where the suite's typed ones were; this step counts
what is left, by a classifier checked in as `internal/gen/testinventory`, freezes the
count a function as a baseline, and makes CI's `build` job fail a test function that
holds more. The maintainer's rule of 2026-11-05, "make it test dynamic values, not
handcrafted tests", is enforced from here, not only written down. The waves' findings
are collected in one list, [`feature-gaps.md`](./feature-gaps.md) §12.

## 1. What

- **`internal/gen/testinventory`**, a command in the style of `internal/gen/api`:
  `main.go` (the walk, the totals, `-list`, `-lines`, `-check`, `-write`),
  `classify.go` (the rules, §2), `main_test.go` (§7), and `baseline.txt`, a function a
  line, `file<TAB>function<TAB>count`, 312 functions, 1,225 lines, under a header
  saying what it is and how it is rewritten. It imports the standard library alone and
  takes `internal/gen`'s level, 0, so `internal/gen/levels` needed no row. A scan of
  the module takes 0.3 s and gives the same lines every time.
- **CI**: `.github/workflows/ci.yml`'s `build` job runs `go run
  ./internal/gen/testinventory -check` right after the public API's check, as "no new
  hand-typed test values". `make testinventory` runs the same; `make
  testinventory-write` rewrites the baseline. `TestTheTreeHoldsToItsBaseline` runs
  the ratchet over the module inside `go test ./internal/gen/...` too.
- **The count** (§3) in the scope's §1, as an "after" column; §3's row 504 done; §4
  says the rule is enforced by the `build` job.
- **`feature-gaps.md`**: §12, the twenty-five findings in thirty-nine cases (§5); in §9,
  the relaxed concat's Int64 and UInt64 met at Int128 "as Polars 2.1 does too", and
  the Arr bullet's `ToStruct` of a null array a null struct "as Polars 2.1's is", the
  two lines waves 503 and 499 measured as stale.
- **`context_files/README.md`**: ten non-step rows a merge script had left behind their
  steps moved back (§6), and this record's row.
- The CHANGELOG's Unreleased Tests and the README's sentence on testing say so.

## 2. The classifier, as built

It parses every `*_test.go` of the module with `go/parser` but `bench/`,
`docs/examples/`, any `testdata/`, a nested module and the oracle registries,
`oracle_<group>_test.go` (107 files: the 106 groups' registries and `oracle_files_test.go`,
their shared layouts, whose literals are a query's parameters). `oracle_test.go`, the
loader, is scanned. In each `Test` and `Fuzz` function (not `TestMain`) it counts the
source lines holding a literal (a number, a string, a rune, `true` or `false`, and
`nil` among a table's or a constructor's values) in an **expected position**:

- the right side of an assignment, `:=`, `var` or `want[i] =` to a want: an
  identifier `want…`, `expect…`, `golden…`, `exp` or `expX`; and a want ranged over,
  `for _, want := range []int64{10, 20}`;
- a want field of a composite literal, keyed or by its place in the struct, the
  struct's fields read from the literal's type, inline or a named type of the package's
  test files, or from its table's element type where the row's is elided;
- a number or a non-empty string compared by `==` or `!=` with a value read from a
  frame: a reader's call (`Get`, `At`, `Item`, `Value`, `Values`, `Int64`…, the root
  package's `cells`, `texts`, `numbers`, `i64col`, `int64Col`), or a variable named
  `got…`, `v`, `r`, `val`, `out`, `row` or the like whose assignment in effect there
  (an `if`'s own, else the last before) reads a frame: a reader, a query collected, or a
  helper that takes a frame or whose body reads one and does not answer an int or a
  Boolean;
- an argument after the first of `slices.Equal`, `slices.EqualFunc`,
  `reflect.DeepEqual`, `bytes.Equal`, `maps.Equal`, an `Equals`, `Equal` or `Cmp`
  method, `ursustest.AssertFrameEqual`'s and `AssertSeriesEqual`'s want; and an argument
  of a test helper, or of the function's own closure, at a parameter named as a want
  (`assertPivot(t, name, got, err, want ...*ursus.Column)`, `wantInts(name, want
  ...int64)`);
- through a variable or a case's field met in one of these and used nowhere else but
  there, printed or tested for being set: `assertPivot(t, …, k, tc.x, tc.y)` counts
  `k`'s value and the table's `x` and `y` values, one step deep.

In an expected position only the value's **data** is counted: a literal, a
composite's elements, a constructor's arguments (`ursus.Values`, `ValuesNullable`,
`Frame`, `FromRows`, `Lit`, `time.Date`, `big.NewInt`, `i128`'s, a test helper's where
its parameter takes data: a slice, an `any`, a frame, a variadic, a string not named
`name`, `col` or `path`), a conversion's operand, a constant expression's operands
(`1 << 40`, `math.MaxInt64 - 1`), an indexed literal table. Not a method's arguments, a
computation's (`math`, `strconv`, `fmt`, a Go oracle's helper named `naive…`,
`model…`, `brute…`, `goX…`), a map's keys, a name (a first string argument of several,
`Col("x")`), a file's name, a helper's lone integer or Boolean parameter (a size, a
seed, a flag), or a formula with a variable in it.

**Not counted, as structure:** a want named for an error, a plan, a type, a shape, an
order or a mechanism (`wantErr`, `wantPlan`, `wantType`, `wantHeight`, `wantOp`,
`wantSub`, `wantNull`…); a want whose every use, within its case table's loop or a
ranged want's, is an error's or a plan's text (`strings.Contains`, `errors.Is`,
`regexp`) or is compared with a shape, a type, a name or an error (`Height()`,
`len()`, `DataType()`, `Names()`, `Columns()`, `Error()`, a case's `dt` or `kind`,
through a method chain, `strings.Join`, `fmt.Sprint`, a concatenation, an `append` or a
variable holding one), its prints and its tests for `""`, `nil` or 0 aside; a want
built a row at a time in a loop, a model's; a literal compared inside a failure's
message; a closure's own input (a UDF's value) and a range's index; a helper taking an
`error`, or named for an error, a refusal, a plan or a resource; a function named for
a refusal (`…Refus…`, `…Reject…`), or whose every `t.Error` and `t.Fatal` sits under a
condition on an error. A line counts once, under its Test function.

**What it does not see.** It is a ratchet, not a proof. A value named otherwise than a
want and compared with something it does not take for a frame's read (a plain
function's output, `FormatTemporal(dt, 0) != "1970-…"`: the comparison rule asks for
a value read from a frame); a package-level table; a golden file on disk; `oracleDifferences`' cells,
ursus's documented answers; a hand-typed input, which is no expected position; a want
built through a method (`i64("a", -7, 7).Select(…)` in a want field). And a line it sees
may be a documented rule's pin, an ursus-only mechanism's (a key encoding's name, a
memory limit, whether a join's sides swap) or a Go-API shape's, not a pasted answer:
the baseline allows those as it found them, and nothing new. A renamed or moved
function fails the check as new until `-write` is run, which a reviewer sees as the
baseline's diff.

## 3. The count

The 2026-11-05 inventory (step 486's six surveys) was a reading: it classed whole
tests and counted the lines of hand-typed data in the PASTED and HAND ones, a pasted
case's inputs with its answers; no script of it is checked in, and it cannot be run
again. Like for like, the classifier was run over `cd48056`, master on 2026-11-05
before step 496 (extracted by `git archive`), and over this branch:

| | the inventory, 2026-11-05 | the classifier at `cd48056` | after, step 504 |
| --- | --- | --- | --- |
| test functions | 1,626 | 1,629 | 1,573, 10 of them the classifier's own; `TestOracle` one, its 2,598 cases subtests |
| lines of test code | | 107,554 in 541 files | 105,902 in 650 files, 6,324 of them in 107 registries |
| hand-typed expected-data lines | about 6,230 (3,940 PASTED, 2,290 HAND) | 3,111 in 476 functions of 260 files | **1,225 in 312 functions of 181 files** |
| recorded groups, cases | 1, 46 (Polars 1.44.1) | 1, 46 | 106, 2,598 (Polars 2.1.0), 84 of them refusals |
| generated cases | 0 | 0 | 396 |
| `testdata/oracle` | 24 KB | 24 KB | 9.3 MB, 9,275,420 bytes |

The 2,598 cases are counted from the JSON, and the waves' records sum to the same
(261 + 264 + 260 + 321 + 464 + 482 + 371 + 175). A recording does not mark a case generated; 396 is
the records' sum (27 + 72 + 48 + 58 + 63 + 70 + 35 + 23), and the JSON agrees within
its means: 386 cases are named `…_gen`, 367 have an input of 50 rows or more.

**The two methods differ by about half.** The waves' own records counted the 110 files
they took at 3,107 hand-typed lines before and 53 after; the classifier counts the
same files at 1,966 before and 80 after. Where the records count more, they count a
pasted case's inputs (`numodds_test.go`: 118 in step 496's record, 82 here) and
refusal tables; where the classifier counts more after, it counts what a wave called
structure or kept on purpose: the frame's Go accessors (`TestFrameConveniences`' `Row`
and `ItemAt`), a fill strategy's printed name, the walk of every aggregate over a
Decimal (`TestEveryAggregateOverADecimalIsAccountedFor`' labels), a database driver's
types (`TestReadDatabaseDriverTypes`), the pins of fill.go's fallback. By rule, the
1,225 lines are 649 want fields, 169 want variables, 63 wants ranged over, 161
literals compared with a read, 81 comparison helpers' arguments, 47 want-taking
helpers' arguments, and 55 reached through a variable or a case's field; the root
package holds 890 of them.

The baseline's largest:

| lines | file | function |
| --- | --- | --- |
| 38 | `expr_meta_test.go` | `TestMetaNamespace` |
| 28 | `expr_name_test.go` | `TestNameNamespace` |
| 28 | `categorical_test.go` | `TestCategorical` |
| 23 | `zipselect_test.go` | `TestLengthChangingExpressionsInSelect` |
| 22 | `database_test.go` | `TestReadDatabaseDriverTypes` |
| 21 | `lines_test.go` | `TestScanLines` |
| 18 | `seriesops_test.go` | `TestSeriesOperations` |
| 18 | `optimizer_byhand_test.go` | `TestOptimizerByHand` |
| 18 | `erf_test.go` | `TestErfAndErfc` |
| 17 | `uniquekeep_test.go` | `TestUniqueKeep` |
| 16 | `internal/physical/groupkeys_internal_test.go` | `TestAnIntegerGroupKeyIsNotEncoded` |
| 16 | `decimal_round_test.go` | `TestDecimalRounding` |
| 15 | `arrshape_test.go` | `TestReshapeAndConcatArr` |
| 14 | `internal/kernel/likechain_internal_test.go` | `TestALiteralChainIsOnlyLiteralsAndDotStars` |
| 13 | `topkby_test.go` | `TestTopKBy` |

Most are in files no wave took: expected names, cells rendered as text, Polars 2.1's
erf values pasted by step 488. `TestReadDatabaseDriverTypes`, a database driver's types,
is what wave 502 left of `database_test.go`. They are the next conversion's list, if the
maintainer wants one; the ratchet only keeps them from growing.

## 4. Calibration

The rules were fixed against a reading of the list, the 30 functions with the most
lines and 60 drawn at random, more than once, and against the waves' own per-file
counts on the pre-oracle tree. The first cut counted 1,370 lines; what changed:

- **A map's keys** were counted: `TestEveryAggregateOverADecimalIsAccountedFor`'s 41
  aggregate names. Not data; the walk's refusal labels stay (13 lines).
- **Two tables in one function** shared the field name `want`, an error table's
  phrases and a data table's cells (`TestReshapeAndConcatArr`); a field's uses are its
  table's loop's now.
- **A reused `got`**: a schema's text compared in one `if` and a cell in the next. A use
  is read against the assignment in effect, the `if`'s own or the last before.
- **A model's want** built in a loop (`want[g] = "∅"`, a Go oracle's sentinel) is not
  counted; a want assigned once in a loop is.
- **Helpers**: a closure, `check := func(got *ursus.DataFrame, want ...*ursus.Column)`,
  was missed (pivot); `got, want float64` marked both parameters a want; a helper
  taking an `error` (`refused(t, err, want)`) is a refusal's.
- **A column of a want beside it**: pivot's index `k`, written once and passed in a
  want position, and `TestPivotCellsOfNone`'s `x` and `y` fields were missed; a ref is
  followed now, and only where it is used nowhere else, so a round trip's input
  compared with itself is not taken for a want.
- **A helper's parameters**: `frame(10)`, `collect(false, 0)` and `runs(t, lf, "m")`
  counted a size, a flag and a name; a parameter's type and name decide now, and
  `goParsedTicks`' layouts are a Go oracle's question.
- **Structure by shape**: a call named for a shape anywhere in an expression made it
  structure, so a read under `WithSchema(want.Schema())` was dropped; it is the value's
  own shape now, through its receivers, conversions, `strings.Join`, `append` and a
  concatenation. Column names (`Names()`, `Columns()`) and a case's `dt` and `kind`
  are structure, as step 501 called `TestAsOfCoalesceAsPolars`' names.
- **A value read from a frame** was any `got`; `got := entries(dir)`, `aggWorkers`,
  `limitOf`, `resumed` and `sizes` are counts of files, workers, a limit, fallbacks and
  batches. A `got` must read a frame now.
- **A want ranged over**, `for i, want := range []int64{10, 20, -3, -4}`, and a literal
  table indexed were missed: 63 lines, scoped to their loop, so error phrases ranged
  over are not.

## 5. Found, for the maintainer

[`feature-gaps.md`](./feature-gaps.md) §12, collected from the registries
(`oracleFinding` and three `t.Skip` notes): twenty-five findings in thirty-nine cases,
none decided. By the step that found them: 497 two (a zero remainder's sign,
`ToDecimal`'s tie), 498 four (a month's rounding, a dynamic day at a midnight gap, a
zoned combine's day, business days counted back), 499 one (an Enum filled outside its
categories), 500 five (a float's ends, a skew near 1e9, a one-value entropy, a rolling
offset's end, nulls in a `TopK`), 501 three (a fill past the type, the mean strategy's
type, `AsOfNearest`'s tie), 502 four (an all-NaN CSV column, Monrovia's offset, no rows
and no columns written, a non-object in an NDJSON struct), 503 six (text and instants
beside numbers, two time units, two Enums, a Decimal to a Boolean, `Any` of a Decimal,
a hive key's date layouts). Grouped by area there: types, expressions and casts,
temporal, aggregates, enums, frames and joins, I/O.

## 6. The index

A merge script of the waves sorted `context_files/README.md`'s step rows and left its
ten non-step rows where they were, so each sat four step rows below its own (`audit.md`
five). Each is back directly after the step row it followed at `e8a4375`, rows moved
and no text changed: `v0.7-scope.md` after 375, `v0.6.8-scope.md`
after 365, `polars-parity.md` after 313, `docs-site-scope.md` after 294,
`v0.6.6-scope.md` and `v0.6.5-scope.md` after 249, `audit-0.6-midpoint.md` after 166,
`audit.md` after 70, `../bench/` and `design/` after 16. A script checked: every step
row of `e8a4375` present, a row for every record on disk, none twice, the steps
strictly descending, one **Start here.**, each of the ten after its step and its text
as at `e8a4375`. Step 505's record, merged before the waves, is the top row; this
step's row follows it, so the steps stay descending, and carries **Start here.**, taken
from 505's.

## 7. Teeth

| mutation | |
| --- | --- |
| the want-field rule by place off (`if false && i < len(fields) …`) | **bites:** `TestAWantTableIsCounted`: `counted lines [24 26], marked [16 18 24 26]`; `TestAnOracleRegistryIsSkipped` too |
| `strings.Contains` of a want read as data | **bites:** `TestAnErrorSubstringIsNotCounted`: `counted lines [17 18], marked []` |
| `Height()` and `len()` not a shape | **bites:** `TestAHeightCheckIsNotCounted`: `counted lines [17], marked []` |
| a hand table added to `TestTrigonometry`, `want := []float64{0.9092974268256817, …}` | **bites:** `-check`: `! trig_test.go TestTrigonometry: 1 line, a function the baseline has not`, the line and its literal, exit 1 |

Each undone by editing back; `trig_test.go` is unchanged. The unit tests' expected
lines are not typed: each synthetic source marks the lines that must be counted with a
`// counted` comment, and the test compares the classifier's lines with the marks. The
test file holds no hand-typed expected value by its own rules.

## 8. Checks

After the last edit, under `GOFLAGS=-p=1 GOMAXPROCS=4 GOEXPERIMENT=simd GOAMD64=v3`:
`go test ./internal/gen/... ./internal/...`, `go test .` in full (with
`TestEveryCitedTestExists`), `GOEXPERIMENT=simd go vet ./...`, `go run
./internal/gen/levels` (ok), `go run ./internal/gen/api -check` (0 gone, nothing
exported), `go run ./internal/gen/testinventory -check` (ok on its own baseline),
`gofmt -l .` (the toolchain's) empty. The widths and the race detector are left to the
pull request's CI.
