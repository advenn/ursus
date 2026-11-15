# Step 513 — as built

**Random queries checked against DuckDB.** The DuckDB half of the random-query
finder: plan step 452's DuckDB facts and 453b (`sqlgen`, `compare`, `duck` and the
translation rules) of [`v0.7.2-plan.md`](./v0.7.2-plan.md), on step 512's pure-Go
qcheck, plugged in where [`step-512-as-built.md`](./step-512-as-built.md) §7 said.
Each case's query, step 512's typed `ast.Query`, runs in ursus inside
`fuzzcheck.Run` and, as SQL `sqlgen` writes, in DuckDB 1.5.5 in the same process,
and the two answers must agree. The shrinker is step 514's.

The SQL computes **ursus's** answer: where ursus answers differently from DuckDB by
a documented rule of its own, the translation follows the rule and names it (step
506 §1: DuckDB, like Polars, is a list of capabilities, not ursus's
specification). Over 47,284 cases compared on CI, the finder found no bug in ursus:
its findings were qcheck's own, two in DuckDB 1.5.5, and one translation that
rounded twice (§8).

## 1. What

Only `qcheck/` and `ci.yml` change; the root module, its `go.mod` and its code gain
nothing.

| package | lines (tests) | what |
| --- | --- | --- |
| `sqlgen` | 1,095 (225) | `Translate(q, tables)`: an `ast.Query` to DuckDB SQL, a chain of CTEs, with a second query counting each frame node's rows; `Rules`, 46 rules each with an ID, the ursus doc or feature-gaps §9 entry it encodes, and what the SQL does; `Macros`, the 41 DuckDB macros the SQL calls; `Excluded`, a query not translated, by a rule |
| `compare` | 1,025 (513) | `FromUrsus` and `FromArrow`, an answer of either engine read into canonical cells by the query's types; `Compare` and `Plan`, the comparator; `Run`, the loop over cases against an `Engine`, every ursus run inside `fuzzcheck.Run`; `Report`, the findings, the rules fired, the allowance fired, and each operation's counts |
| `duck` | 313 (1,884) | `DB`, one in-memory DuckDB on one connection behind the `duckdb` tag (with duckdb-go's own `duckdb_arrow`), a stub without it; `Load` registers each table's record with `__rid` and its Dates as days; `Answer` and `Counts` run the SQL and read Arrow back; the DuckDB tests (§3, §6) |
| `cmd/qcheck` | 126 | `qcheck duck -seed N -duration D [-cases K] [-alloc MiB] [-v]` beside `self` |

`qcheck/go.mod` requires duckdb-go/v2 v2.10505.0, bench's pin (DuckDB 1.5.5); the
qcheck module's go.sum gains its entries and the root's none. Two lines of step
512's code changed: `gen`'s `intLit` wraps its value to its type, and `ast.Check`
refuses a literal its type does not hold (finding 1); `self`'s report showed an
Int128 with its words swapped (finding 6).

## 2. DuckDB

`duck.Open` opens an in-memory database through `database/sql`, takes one
connection, wraps it in duckdb-go's Arrow interface, and runs `Settings` and
`sqlgen.Macros`:

- `threads TO 1`: DuckDB's own answers do not move, its float sums and its row order
  through a pipeline;
- `ieee_floating_point_ops TO true`: x/0 is an infinity and 0/0 NaN, as ursus's are
  (T-DIV-FLOAT), not null;
- `preserve_insertion_order TO true`, `memory_limit TO '2GB'`.

`Load` registers each table's record, as an Arrow view copied into a native table
(a view can be read once), with `__rid`, the rows' numbers, first, and each Date32
column as an Int32 of the same days: DuckDB's DATE reads int32's greatest and
least-but-one days as ±infinity (a fact below), so a Date is an INTEGER in SQL
(T-DATE-DAYS) and every Date function the translation needs is computed from the
days. `Answer` and `Counts` run the SQL through `Arrow.QueryContext`.

duckdb-go is cgo and links 131 MB of static libraries, so `duck`'s real file is
behind the `duckdb` tag, and its Arrow interface behind duckdb-go's `duckdb_arrow`;
without them `duck.Open` returns `ErrUnsupported` and `qcheck duck` exits 2. The
DuckDB tests are not tagged: they skip in a build without DuckDB, so the laptop
vets and compiles them, and `TestTranslationRulesAgainstUrsus` runs every rule's
queries with ursus in DuckDB's place. Nothing was linked against DuckDB on the
laptop; a tagged `go vet` was not run there either.

## 3. The facts

`TestDuckDBSemantics` holds 45 facts the translations lean on, each value checked
against an oracle the test computes: Go's arithmetic, `strconv`, `math/big`,
`regexp`, or ursus's own function where the fact is a translation of one
(`kernel.OrderKeyF64`, `kernel.FloatText`, `dtype.ParseFloat`,
`dtype.FormatTemporal`, `dtype.ParseTemporal`). Three corrected the first draft of
the rules on CI's first run:

- **`fmod()` is not C's fmod:** DuckDB 1.5.5's `fmod(x, y)` is x - y·floor(x/y),
  rounded, so `fmod(-7.5, 2)` is 0.5 and `fmod(5, inf)` NaN. Its `%` on DOUBLE is C's
  fmod, which `u_fmod` now reads (T-FLOAT-MOD); both are facts.
- **TRY_CAST's null is dropped** where the cast is compared with a constant
  (finding 2): `u_irange`, a CASE, replaces it (T-CAST-RANGE); the fact asserts the
  dropped null, so once DuckDB keeps it the fact fails and says the workaround can go.
- **An integer Mean through a double rounds twice** (finding 4): `u_imean` computes
  ursus's nearest double exactly, held to `big.Rat`.

The 45:

- **Order and keys:** ORDER BY puts nulls first or last as each key says; ORDER BY
  `u_fkey(x)` is ursus's total order, NaN above +Inf, -0 tying +0 in input order;
  DuckDB's = and < take NaN as one value above +Inf, unlike IEEE (T-IEEE-CMP's
  reason); -0 equals +0 and is not below it, as IEEE; GROUP BY and count(DISTINCT),
  raw and through `u_fkey`, make one group of every NaN, a negative payload's too, and
  one of ±0; a join on doubles matches NaN with NaN and -0 with +0, raw and through
  `u_fkey`; VARCHAR orders, compares, mins and maxes bytewise, NUL and all; min and
  max of BOOLEAN.
- **Integers:** // truncates toward zero and % takes the dividend's sign, in BIGINT
  and HUGEINT; `u_idiv` and `u_imod` floor, a zero divisor null, `u_w64` wrapping
  MinInt64 // -1; BIGINT arithmetic past int64 raises (T-INT-WRAP's reason) and
  HUGEINT holds a product of two BIGINTs; `u_w8` to `u_w64` wrap as Go does; sum of
  BIGINT is exact past int64, exported as Decimal128(38, 0), count as Int64.
- **Floats:** a double by zero is ±Inf or NaN under `ieee_floating_point_ops`; % is
  C's fmod and `fmod()` is not; `u_fmod` is ursus's remainder over every pair of 13
  values, ±0, ±Inf and NaN among them; floor keeps -0, negation makes -0 of +0, abs
  +0 of -0; signbit; BIGINT to DOUBLE rounds to the nearest, ties to even; a cast of a
  fraction to BIGINT does not truncate (T-CAST-EXACT's reason); round() rounds half
  away from zero; `u_feq` to `u_fge` are IEEE, a null either side null; `u_fless` is
  the total order's <; `u_f2i` keeps an integer in range; `u_imean` is the double
  nearest Σx/n over 310 sums, midpoints among them.
- **Casts and text:** TRY_CAST to a narrower integer is null out of range; TRY_CAST
  of signed digits to BIGINT is `strconv.ParseInt`'s; TRY_CAST's null dropped in a
  comparison with a constant, and `u_irange` keeping it; `u_pfloat` is
  `dtype.ParseFloat` over 499 texts; DOUBLE to VARCHAR writes strconv's shortest
  digits; `u_ftext` is `kernel.FloatText` over 431 floats; `u_dtext` is
  `dtype.FormatTemporal` over 618 days to int32's ends; `u_pdate` is
  `dtype.ParseTemporal`; `u_pbool` is `strconv.ParseBool`; printf('%04d') pads and
  does not cut; lpad cuts; regexp_full_match matches the whole text by RE2;
  decode(from_hex()) holds a NUL.
- **The rest:** list_reduce folds in the list's order from its initial value;
  list(x ORDER BY o) FILTER (WHERE x IS NOT NULL) keeps the order; AND, OR and NOT are
  Kleene; a CASE with a null condition takes the ELSE; a registered table of no rows;
  isinf of a DATE at int32's ends.

`TestArrowRoundTrip` registers generated records as `Load` does and reads each back
bit-exact, a NaN's payload too, until 40 tables and every one of NaN, -0, MinInt64,
int32's greatest day, text of 1,000 bytes and more and a NUL inside text have been
read back; a value changed in what was read must show.

## 4. The translation

A query is a chain of CTEs, one or a few a frame operation, each holding the
frame's columns, named as `ast` names them, and `__o`, the row's place in the
frame's order: a scan's `__rid`; a sort's row number over its keys and the input's
`__o`; a group's least `__o`; a join's row number over the left's and the right's
`__o`. An answer whose order the query promises ends ORDER BY `__o` and compares by
position; any other compares as a multiset. Computed columns go through an alias of
the CTE's own, `__c0`, and a renaming CTE, so no output name shadows an input
column DuckDB would read instead. `Translate` checks the tree with `ast.Check`
first.

Every rule, with the doc or §9 entry it encodes and what the SQL does; each fires
on the generated queries (`TestEveryRuleFires`, 2,000 cases, but the refusals, which
the AST never makes, and T-ISIN-NULL), and each but T-ISIN-NULL has queries chosen
to meet its edges that ursus and DuckDB must agree on (`TestTranslationRules` on
CI; the same queries against ursus itself locally). The R- rules are step 512 §3's
refusals, first: where the AST avoids a refusal SQL never sees it, and `Translate`
refuses a tree of each shape by that rule (`TestRefusalsAreRefused`).

| rule | source | the SQL |
| --- | --- | --- |
| `R-CAST-STRICT` | Cast's doc (expr.go): a strict cast fails the query on the first value it cannot represent | a strict cast only where none can fail (ast.StrictCast); every other is CastLossy, so no SQL cast raises |
| `R-PROMOTE` | feature-gaps §9: a conditional's branches meet by Promote, which refuses text, a Boolean or a Date beside a number | branches, fills and arithmetic meet only where model.Promote does |
| `R-I128-ARITH` | expr.go: Int128 arithmetic is exact or refused; Neg of an unsigned integer is refused | no arithmetic on an Int128 or a Uint64: compared, sorted and carried only |
| `R-MEAN-BOOL` | agg.go: Mean of a Boolean is refused | Mean of integers and floats only |
| `R-JOIN-SUFFIX` | join.go: a join whose suffixed name is taken is refused | no generated name ends in _right, and a join ast cannot type is not drawn |
| `R-JOIN-KEYS` | join.go: keys that do not meet are refused | keys are integer with integer, text with text, float with float |
| `R-SELECT-LIT` | feature-gaps §9: a Select of literals alone is a row for each of the frame's rows | SQL's SELECT of literals over a FROM is a row a row, as ursus's |
| `T-NULLS` | kernel/order.go SortSpec: NullsLast places nulls at the end of the output, independent of direction | every ORDER BY key says NULLS FIRST or NULLS LAST |
| `T-SORT-STABLE` | agg.go Sort's doc: the sort is stable | every ORDER BY ends with the input's order, __o |
| `T-KEY-ZERO` | kernel/order.go OrderKeyF64: sort, group-by and join keys take the total order, NaN one value above +Inf, -0 equal to +0 | a float key, in ORDER BY, a join's ON and count(DISTINCT), is u_fkey(x): -0 made +0 |
| `T-IEEE-CMP` | kernel/order.go: ursus uses IEEE for comparison expressions, NaN equal to nothing and ordered against nothing | a float comparison is u_feq and its kin: false where either side is NaN, true for Ne; DuckDB's NaN equals NaN |
| `T-CMP-PROMOTE` | expr/resolve.go resolveComparison: both sides are cast to the type Promote meets them at | an integer beside a float compares as DOUBLE, an Int128 or Uint64 beside an integer as HUGEINT |
| `T-INT-WRAP` | expr.go arithmetic: an integer result that leaves its type wraps, two's complement; MinInt64 // -1 is MinInt64 | integer +, -, *, Neg and Abs are computed in HUGEINT and wrapped to the width by u_wrap; DuckDB raises on overflow |
| `T-FLOOR` | expr.go FloorDiv's doc: FloorDiv floors, Mod takes the divisor's sign, an integer divisor of zero is null | integer // and % are u_idiv and u_imod over HUGEINT, a zero divisor NULLIF'd to a null; a float // is floor(a / b) |
| `T-FLOAT-MOD` | feature-gaps §9 and kernel/scalar.go modFloatScalar: a float remainder takes the divisor's sign, a zero one too | a float % is u_fmod: fmod, then the divisor added where the signs differ, a zero remainder signed as the divisor |
| `T-DIV-FLOAT` | expr.go Div's doc and feature-gaps §9: Div always returns a float, IEEE, x/0 an infinity or NaN | both sides CAST AS DOUBLE, under ieee_floating_point_ops |
| `T-LIT-TYPE` | feature-gaps §9: a literal has its Go value's type | every literal is CAST to its AST type; a float literal is read from its shortest text, which rounds to its bits |
| `T-PROMOTE` | dtype.Promote (feature-gaps §9): two integers meet at the wider, an integer and a float at Float64 | each operand is CAST to the type its node meets at before the operator |
| `T-KLEENE` | expr.go: And, Or and Not are Kleene logic | SQL's AND, OR and NOT, which are |
| `T-WHEN-NULL` | cond.go: a null condition is false (step 311) | SQL's CASE WHEN, which takes the ELSE for a null condition |
| `T-FILL-WEAK` | fill.go and feature-gaps §9: a fill's literal is weak, a value that fits keeping the column's type | COALESCE at the fill's AST type, the literal cast to it |
| `T-CAST-EXACT` | feature-gaps §9 and castnum.go floatToInt: a float cast to an integer is exact or null when lossy | CASE x >= lo AND x < hi AND x = trunc(x): DuckDB's cast rounds |
| `T-CAST-RANGE` | castnum.go narrowI128 and Cast's doc: an integer cast lossy to a narrower one, or to a Date, is null out of range | u_irange, a CASE on the target's range, an Int64 to a Date on int32's; not TRY_CAST, whose null DuckDB 1.5.5 drops in a comparison with a constant |
| `T-PARSE-INT` | castparse.go parseSigned: text to an integer is strconv.ParseInt, base 10, at the target's width | regexp_full_match [+-]?[0-9]+ before TRY_CAST: DuckDB's cast trims spaces and reads more |
| `T-PARSE-FLOAT` | dtype/numtext.go ParseFloat: ursus's float grammar, inf, infinity and nan in any case, an overflow null | u_pfloat: the grammar as a regexp, the words by name, an infinity from digits null |
| `T-PARSE-BOOL` | castparse.go parseBools: text to a Boolean is strconv.ParseBool | u_pbool: strconv.ParseBool's twelve words |
| `T-PARSE-DATE` | dtype/temporal.go ParseTemporal: text to a Date is time.Parse("2006-01-02"), the day one that exists | u_pdate: four, two and two digits, the month and day checked, the days counted by Hinnant's days_from_civil |
| `T-DATE-DAYS` | dtype.Date: a Date is int32 days, over the whole range | a Date is an INTEGER of days in SQL: DuckDB's DATE reads int32's ends as ±infinity |
| `T-DATE-TEXT` | dtype.FormatTemporal: a Date's text is Go's 2006-01-02 of the proleptic Gregorian day, a year of four digits at least | u_dtext: Hinnant's civil_from_days, printf'd, '-' before a negative year |
| `T-FLOAT-TEXT` | kernel/caststring.go FloatText (step 325): the shortest digits, plain from 1e-5 to below 1e16, else 1.5e-7 | u_ftext: DuckDB's shortest digits taken apart and written as FloatText writes them |
| `T-TEXT` | kernel/caststring.go formatToString: an integer as strconv's decimal, a Boolean as true or false | CAST AS VARCHAR, which writes both alike |
| `T-SUM` | agg.go and feature-gaps §9: an integer's or a Boolean's Sum is an exact Int128, null for a group of no value | sum(CAST(x AS HUGEINT)), a Boolean through INTEGER |
| `T-FLOAT-AGG` | kernel/aggsum.go sumAcc and aggwhole.go floatWhole: a float Sum adds the values in the rows' order from +0, Mean divides it | list_reduce over list(x ORDER BY __o) from +0: DuckDB's sum may add in any order |
| `T-EXTREMUM` | kernel/aggminmax.go: float Min and Max take the total order, NaN above all, the first of equal values kept | list_reduce over list(x ORDER BY __o) keeping the first of equal values; other types min and max |
| `T-MEAN-INT` | kernel/aggsum.go finishDecimal: an integer Mean is the exact sum divided once, rounded to the nearest float | u_imean: \|Σx\| // n and thirty digits of the remainder over n, read by DuckDB's exact parser; avg rounds twice |
| `T-NUNIQUE` | feature-gaps §9 and kernel/aggcount.go: NUnique does not count null, and counts by grouping equality | count(DISTINCT u_fkey(x)) for a float |
| `T-COUNT` | feature-gaps §9: Count, Len and NUnique are a Uint64 | CAST(count(...) AS UBIGINT) |
| `T-GROUP-NULL` | join.go JoinNullsEqual's doc: in GroupBy null forms its own group | SQL's GROUP BY, which groups nulls |
| `T-MAINTAIN-ORDER` | agg.go: MaintainOrder keeps the groups in the order each first appears | a group's __o is min(__o), and an ordered answer is ORDER BY __o |
| `T-WITH-SEQ` | agg.go WithColumns: later expressions read the columns earlier ones added | one CTE per column |
| `T-JOIN-NULL` | join.go JoinNullsEqual: by default a null key matches nothing | SQL's = in ON and EXISTS |
| `T-JOIN-SHAPE` | join.go and feature-gaps §9: the left's columns, a coalesced key at the type the keys meet at, the right's others suffixed _right; JoinLeftOn and JoinRightOn keep both keys | an explicit select list |
| `T-JOIN-ORDER` | join.go JoinOrderLeft: the left's rows in order, each row's matches in the right's; a semi or anti join keeps the left's | row_number() OVER (ORDER BY l.__o, r.__o); EXISTS keeps l.__o |
| `T-SLICE` | frameops.go Slice: a negative offset counts from the end, the window clipped to the frame; a negative length is to the end | row_number and count(*) OVER (), the window's bounds computed |
| `T-ISIN-NULL` | not used: step 512's AST generates no IsIn | none |
| `T-ORDER` | ast.Info: an answer whose order the query promises compares by position, any other as a multiset | an ordered answer ends ORDER BY __o |

A literal is cast to its AST type from its text: an integer's decimal, a float's
shortest digits, which DuckDB reads to the bits (a fact; `TestLiteralsKeepTheirBits`
holds the text to strconv), text quoted, or `decode(from_hex(...))` where it holds
a byte outside printable ASCII, as a NUL cannot be in a query's text.

## 5. The comparator

`FromUrsus` reads ursus's frame and `FromArrow` DuckDB's records into canonical
cells by the query's model types, whatever Arrow type held them: an integer of any
width, a Decimal128 of scale 0 (DuckDB's HUGEINT, ursus's Int128) and a Date as
decimal text; a float as its bits, any NaN one NaN, so -0 is not +0; text as it
is; a Boolean as 0 or 1. Text from DuckDB is copied out of its record, whose buffer
it otherwise aliases (finding 5).

**Exact, but for one allowance.** A noisy column, a float Sum or Mean of a
group-by that runs on every thread (`ast.Bounded`), may differ from DuckDB's when
ursus ran on several threads: |Δ| ≤ 2·n·eps·Σ|x| for a Sum and 2·eps·Σ|x| + 2 ulp
for a Mean, the critic's error model with `ast.Bounded`'s Σ|x| and n, the larger
Σ|x| of the two answers. On one thread it compares exactly: the SQL adds a float
Sum or Mean in the rows' order from +0 (T-FLOAT-AGG), as ursus's accumulator does.
**Floats from arithmetic compare exactly** too, where the brief allowed the error
model: each engine rounds each IEEE operation once and the SQL does the same
operations, and over 47,284 cases none differed; an integer Mean is exact since
`u_imean`, and a float Min or Max keeps the first of equal values as ursus does.
So the one allowance fired 2,364 times on CI and nothing else may differ.

**Outcomes.** A refusal by ursus where DuckDB answered is a `refused` finding; an
error from DuckDB a `sql` finding (sqlgen's bug, or DuckDB's); ErrInternal, a panic,
a hang, an allocation past the bound or a goroutine left behind in ursus's run a
`fuzzcheck` finding, whatever it answered; an answer whose columns are not the
query's a `schema` finding. Each case runs ursus under one configuration,
`ConfigOf`: a row of step 512's covering array picked by the seed, so threads {1,
4, 8}, batch sizes 1 to 65,536, the optimizer on or off with `WithVerify`, and 1 or
k records meet DuckDB over a run, always under `WithMemoryLimit(0)`.

**Anti-vacuity:** each frame operation's rows, from DuckDB's count of every node in
an agreeing query; an operation fed rows 20 times that answered rows in under a
twentieth of them fails `vacuous`, step 512's thresholds. In the green run Filter
answered rows in 7,639 of 15,075 nodes fed them, an inner join 639 of 1,528, Slice
1,978 of 3,277.

## 6. Teeth

`TestComparatorTeeth` (local, 150 cases a break) breaks the engine's answer through
`Options.Corrupt`, over the cases a break applies to, and holds the run to a
mismatch in each case broken and in no other; DuckDB is ursus at the reference,
through DuckDB's Arrow types, as the laptop links no DuckDB. `TestDuckTeeth` (CI,
120 cases a break) breaks DuckDB's own answers the same way.

TEETH

`TestRunTeeth`: an ErrInternal returned, and a panic, inside ursus's run in batches
of 3 and 7 are each a `fuzzcheck` finding, every time (10 of 10 each).
`TestRunAgainstUrsus` runs 1,000 cases through the whole loop against ursus at the
reference and finds nothing, the noisy allowance firing, so a finding against
DuckDB is not the comparator's own. `TestDuckSmoke` runs seed 20,261,115's 400
cases against DuckDB, finding nothing and running every operation; `TestDuckFound`
runs each case of §8 alone.

## 7. The CLI and CI

`qcheck duck -seed N -duration D` prints its seed first, then each finding: its
kind, its seed, ursus's configuration, the differing row of both answers, both
answers' first rows, the tables and the query as Go, and the SQL. `qcheck duck
-seed S -cases 1` runs case S alone.

The `qcheck-duckdb` job in `ci.yml`, beside `qcheck`, with cgo: `go vet -tags
duckdb,duckdb_arrow ./...`, the `duck` package's tests verbose, `compare`'s and
`sqlgen`'s, then `qcheck duck -seed <run number>000000 -duration 5m`. Its modules and
build cache are kept by `actions/cache` under a key of its own,
`qcheck-duckdb-<os>-<hash of qcheck/go.sum>`, so the pure-Go `qcheck` job, whose
cache holds neither the bindings nor their cgo build, does not overwrite it. Not a
required check: the maintainer decides that.

RUNS

## 8. Findings

None is an ursus bug: no CHANGELOG "Fixed" line.

| # | found by | what | class | done |
| --- | --- | --- | --- | --- |
| 1 | 33 `sql` findings, seed 804,000,458 among them | `gen`'s `intLit` typed a FloorDiv divisor of 1–1000 as an Int8 key's type: `Lit(int8(243))`, which `build` wrapped to -13 without a word and `Query.Go` printed as `int8(243)`, which does not compile | qcheck's own (step 512's generator) | `intLit` wraps its value to the type, as `build` did; `ast.Check` refuses a literal its type does not hold; the case is in `TestDuckFound` |
| 2 | seed 804,000,121 | DuckDB 1.5.5 drops TRY_CAST's null where the cast is compared with a constant: `TRY_CAST(x AS INTEGER) > 18988` of an x past int32 is `x > 18988`, true or false, not null; TRY_CAST alone is null | (c) DuckDB's | T-CAST-RANGE renders `u_irange`, a CASE on the range, for a cast to a narrower integer and an Int64 to a Date; a fact asserts the dropped null, and fails when DuckDB keeps it |
| 3 | the facts | DuckDB's `fmod()` is x - y·floor(x/y), rounded, NaN for an infinite divisor; not C's fmod, which its % is | (c) DuckDB's behaviour, not a bug: its own definition | `u_fmod` reads %; both facts kept |
| 4 | seeds 804,002,697, 804,006,513, 804,011,153, 804,013,588 and 804,018,440 | an integer Mean, which ursus rounds once to the double nearest the exact mean (`finishDecimal`), was in the SQL a HUGEINT sum cast to DOUBLE and divided, two roundings, an ulp apart past 2^53; the comparator's ulp for it did not follow a mean through a `Max`, an alias, a replacement by itself and a division | sqlgen's own | `u_imean` computes the nearest double exactly (|Σx| // n and thirty digits of the remainder over n, read by DuckDB's exact parser), held to `big.Rat` by a fact; the allowance is gone; the cases are in `TestDuckFound` |
| 5 | CI's first run: garbage text, then a SIGSEGV in `memeqbody` | a test helper kept arrow-go's `String.Value`, which aliases the record's buffer, after DuckDB freed the record | qcheck's own (step 513's test) | text cloned; `FromArrow` always copied |
| 6 | `TestTranslationRulesAgainstUrsus`, before any push | `self`'s report printed an Int128 with its words swapped: `i128.Int128` is `{Hi, Lo}`, Hi first in memory; `compare`'s reader had copied it | qcheck's own (step 512's report; its comparison, of the bytes, was right) | both read Hi first |
| 7 | `TestEveryRuleFires` | step 512 §3 says a Select of literals alone is drawn; `gen`'s Select always keeps a column, so it never is | qcheck's own doc | R-SELECT-LIT's query is built by hand in `TestTranslationRules`; noted here for 512's generator |

Not findings: master's `FuzzScanCSV` crashed while minimizing a 296-byte input on
this PR's fuzz run (af20102), "fuzzing process hung or terminated unexpectedly",
with no line of ursus changed by this branch; and `FuzzOwnPagesMatchArrow`, steps
520 and 521's.

## 9. What 514 and the rest plug into

- **514, the shrinker:** a DuckDB finding is a seed and ursus's configuration
  (`compare.Finding`, `ConfigOf`); the SQL comes from `sqlgen.Translate` of the
  shrunk tree, and the comparison from `compare.Compare`, so a shrink step keeps the
  signature by re-running one case. `TestDuckFound` is the corpus's first entries.
- **470, 471:** an op or a type is a rule with its source and a `ruleCases` entry,
  and `TestEveryRuleFires` and `TestTranslationRules` hold the table whole.
- **R-SELECT-LIT and T-ISIN-NULL** are ready for a generator that draws a Select of
  literals alone, or IsIn.

## 10. Checks

CHECKS
