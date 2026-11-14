# Step 512 — as built

**Random queries checked against themselves: the pure-Go qcheck.** The pure-Go half
of [`v0.7.2-plan.md`](./v0.7.2-plan.md)'s H5/H6, split as the critic's correction to
453 splits it: plan step 452's module without DuckDB, 453a (the generator, the typed
AST and its builder) and 454 (the self check). It is the random-query finder of the
bug hunt on master, before v0.8.0 ([`step-506-as-built.md`](./step-506-as-built.md)
§3, "then"). DuckDB is step 513's and the shrinker step 514's; §7 says where each
plugs in. The self check found three bugs, each an allocation quadratic in the
batches at batch size 1, and each is fixed (§6).

## 1. What

`qcheck/` is a module of its own, so the root module's `./...` and `go.mod` gain
nothing. Its requirements are ursus (`replace … => ..`) and arrow-go v18.7.0, the
root's; no new dependency.

**Its path is `github.com/advenn/ursus/qcheck`, not `ursusqcheck`** as the plan and
the brief named it. The brief also asks that every run go through `fuzzcheck.Run`,
and `internal/fuzzcheck` is internal: Go allows the import only from a package whose
import path is under the internal directory's parent, by import path in module mode
(golang.org/issue/23970). Under `module ursusqcheck` the build says `use of internal
package github.com/advenn/ursus/internal/fuzzcheck not allowed`; under the nested
path it builds, and the root's `./...` still stops at `qcheck/go.mod`. `go.mod`
says so in a comment.

| package | lines (tests) | what |
| --- | --- | --- |
| `model` | 346 (74) | the types a column or an answer takes, `Promote`, the profiles, a generated `Table` (columns and one Arrow record), `Config` (threads, batch, optimizer off, WithVerify, records), the `Factor`s and `Pairwise`, the covering array |
| `ast` | 1,218 | the typed AST: expressions (`Col`, `Lit`, `Bin`, `Un`, `When`, `FillNull`, `Cast`, `Agg`) built only through constructors that refuse an ill-typed node; frames (`Scan`, `Filter`, `Select`, `WithColumns`, `GroupBy`, `Sort`, `Head`, `Slice`, `Join`) and `Query` (collected, or `Count`); `Schema`, each frame's columns, origins, noise, promised order and key sets; `Check`; `Bounded`, a noisy column's error bounds beside it; `Go`, a query as Go source over ursus's API |
| `gen` | 1,038 (153) | tables and queries from a seed: `Table(seed, name, maxRows)`, `NewCase(seed)` |
| `build` | 313 | an AST to a `LazyFrame` over `ScanArrowRecords`, a `Config` to its `CollectOption`s, a table cut into k records |
| `self` | 1,036 (265) | the self check: `Run(Options) *Report`, `Finding`, the comparator, bisection, the canary, the anti-vacuity counts |
| `cmd/qcheck` | 61 | `qcheck self -seed N -duration D [-cases K] [-variants V] [-alloc MiB] [-v]` |
| `sqlgen`, `compare`, `shrink`, `duck` | a `doc.go` each | left to 513 and 514; each doc says what goes there |

`self` blank-imports `internal/fuzzcheck/invariants`, so data's boundary checks
(`CheckNonNullable`, `CheckTimeRange`) are on in every run.

## 2. The tables

`gen.Table(seed, name, maxRows)`, step 453's list:

- **Types:** two to six columns of Int64, Int32, Float64, String, Bool and Date, an
  Arrow record built with arrow-go's builders; the first column an integer, so that
  a group-by always has a key. Names are type and letter, `i64a`, `f64c`, `strd`, so
  two tables share names and a join suffixes them.
- **Sizes:** 0 (5%), 1–10 (15%), 11–300 (30%), 301–3000 (38%), and 8191, 8192 or 8193
  (12%), the reference batch's edges.
- **Null rates:** 0, 0.1, 0.5 or 1 per column; a third of the columns forced to 0.
- **Profiles**, per column:
  - *tame*: integers in [−100, 100], floats in quarters (whose sums are exact), a dozen
    short words, dates of 2020–2021; half of them drawn from a pool of 1 to 100
    distinct values, so keys repeat and groups grow;
  - *wild*: integers over the whole range, floats NormFloat64·10^k for k in [−5, 10]
    and one in ten of arbitrary finite bits, ASCII text of 0–20 bytes and numbers as
    text, dates across ±2,700 years;
  - *extreme*, among the wild: NaN, ±0, ±Inf, ±MaxFloat64, the subnormals, 2^53 and
    2^53 + 1; each integer type's least and greatest and their neighbours; Unicode
    (`Zoë`, `日本語`, `🐻`, a combining accent, NUL inside a string, a right-to-left
    override, U+10FFFF, a flag of a ZWJ sequence), text of 1,000–5,000 bytes; dates at
    int32's ends and 0001-01-01, 9999-12-31 and past them; a Boolean column all true or
    all false.
- **Seeds:** a case's seed alone rebuilds it on any machine. Each draw is from a PCG
  seeded by SplitMix64 of the seed: PCGs seeded with neighbouring seeds drew alike, and
  a run's cases have neighbouring seeds. `TestACaseIsItsSeed` holds two draws of a seed
  to the bit (the records' IPC bytes) and the character (the query's Go).

## 3. The queries

A query is a scan of the first table and one to four operations, each drawn among
those its frame allows, at most one join and two group-bys, and a `Count` instead of
`Collect` one time in ten. The ops are 453's:

- `Filter`, `Select`, `WithColumns` (one to three columns, each over the columns the
  ones before it left, as `agg.go` documents), `Count`;
- `GroupBy` on one to three signed-integer keys of mixed widths: a column, its
  `CastLossy` to Int8, Int16, Int32 or Int64, its `Mod` by 2–10, its `FloorDiv` by
  1–1000, or an integer expression; with nulls where the data has them, and a lossy
  cast's nulls; none for one row of the whole frame, one time in ten; `MaintainOrder`
  three times in ten;
- `Sort` by one to three columns, ascending or descending, nulls first or last; then
  `Head` or `Slice`, offsets from −9000 to 4000 and lengths −1 to 5000;
- `Join` inner, left, semi and anti, the key kind drawn first among integer (Int32 and
  Int64, which meet at the wider), text and float with its NaN and ±0, so that each is
  joined on; on a key of the same name (`JoinOn`, coalesced) or two names
  (`JoinLeftOn`/`JoinRightOn`, both kept); a third of the right sides filtered;
  `JoinMaintainOrder(JoinOrderLeft)` half of the inner and left joins;
- comparisons, `And`, `Or`, `Not`, `IsNull`, `IsNotNull`; `Add`, `Sub`, `Mul`, `Div`,
  `FloorDiv` and `Mod` (by literals −7 to 10 and by columns), `Neg`, `Abs`; `When`;
  `FillNullWith`; `Cast` and `CastLossy`;
- `Sum`, `Mean`, `Min`, `Max`, `Count`, `Len` and `NUnique`, of a column or a small
  expression.

A literal compared with a column is a value the column's base table holds half the
time, so a filter keeps some rows. Each expression node carries its type, and
`ast.Check` rebuilds every type from the leaves. The types are ursus's own rules for
what qcheck generates: a literal has its Go value's type, two integers meet at the
wider and a float beside an integer at Float64, `Div` is a Float64, an integer's or a
Boolean's `Sum` an Int128, `Mean` a Float64, a count a Uint64, and a weak literal fill
keeps the column's type where it fits (feature-gaps §9, `fill.go`). Every self run
compares them with ursus's `CollectSchema` (a `schema` finding), and
`TestQueriesAreWellTypedAndUrsusAgrees` does over 300 seeds.

**Where ursus documents a refusal, the AST avoids it**, and the reference refused
none of the queries of any run:

| ursus's rule | the AST's choice |
| --- | --- |
| A strict `Cast` fails the query on the first value it cannot represent (`Cast`'s doc). | A strict cast only where no value can fail: a widening integer, an integer or a Boolean to Float64 or text, a float or a Date to text, a Date to Int32 or Int64, Int32 to a Date. Every other is `CastLossy`: text to a number, a Date or a Boolean, a float to an integer, an integer narrower or to a Date. A strict cast's refusal depends on how many rows a `Head` lets through, which differs by batch size and by limit pushdown, and would read as a mismatch of configurations. |
| A conditional's branches meet by `Promote`, which refuses text, a Boolean or a Date beside a number (§9). | Branches, fills by a column and arithmetic meet only where `model.Promote` does: a type with itself, two integers, an integer and a float. |
| Int128 arithmetic is exact or refused; `Neg` of an unsigned integer is refused (`expr.go`). | No arithmetic on an Int128 (an integer `Sum`) or a Uint64 (a count): they are compared with an Int64 literal, sorted and carried. |
| `Mean` of a Boolean is refused. | `Mean` of integers and floats only; a Boolean's `Sum` (an Int128) is generated. |
| A join whose suffixed name is taken is refused ("produces two columns named …"). | No generated name ends in `_right`, and a join whose schema `ast` cannot make is not drawn. |
| A join of keys that do not meet. | Keys are integer with integer, text with text, float with float. |
| A `Select` of literals alone is a row for each of the frame's rows (§9). | Drawn: it is no refusal, and each configuration answers it alike. |

Three more choices keep a query's answer one answer, which is the self check's
premise:

- `Head` and `Slice` only of rows in a promised order: a scan's, kept by the row-wise
  operations, `Sort`'s; and not after a group-by without `MaintainOrder`, or an inner
  or left join without `JoinMaintainOrder`, whose orders are not promised (`agg.go`,
  `join.go`). `MaintainOrder` promises an order only of input in one. A `Sort` of rows
  in no promised order sorts by the group-by's keys too, which makes it total.
- A float `Sum`, `Mean`, `Min` or `Max` only over rows in a promised order: over rows
  in none, a float sum's last bits and the sign of a zero `Min` would move with the
  order, which the one allowance (§4) does not cover.
- An inner or left join only where the base tables bound its matches under 10,000
  rows (`gen.MaxJoinRows`), NaN matching NaN and −0 +0 as ursus's join matches them:
  an 8193-row table joined to itself on three values would answer 22 million rows, and
  in batches of one row a row of a dozen columns costs about 37 KB of batch headers
  through four operators.

## 4. The self check

`self.Run` runs cases from a seed, case i's seed the seed plus i, until a count or a
duration. Each case:

1. **The schema:** ursus's `CollectSchema` of the query against the AST's types.
2. **The reference**, twice: one thread, batches of 8192 rows, `WithMemoryLimit(0)`,
   the default flags, each table one record. The two answers must agree to the bit and
   by position, whatever the query promises.
3. **Each operation's rows**, for anti-vacuity: a `Count` of every frame node and of
   its input, at the reference.
4. **Three variants** (`-variants`), rows of `model.Pairwise`, a covering array of
   21 rows in which every pair of levels of any two factors meets: threads {1, 4, 8}
   × batch sizes {1, 2, 3, 7, 64, 1024, 65536} × the optimizer on or off × 1 record
   or k (2 to 5, drawn per case, cut at points drawn from the table's seed, an empty
   record among them sometimes). Every variant runs `WithVerify`, so the optimizer
   asserts each rule kept the schema, and `WithMemoryLimit(0)`: under the default
   budget a group-by switches to serial at a batch scheduling picks
   (`WithMemoryLimit`'s doc), so it is never compared bit-exact twice (437's caveat),
   and under a limit of the caller's own every group-by is serial (`aggWorkers`), which
   would hide the threads. Each variant's answer is compared with the reference's; the
   first variant runs twice and must agree with itself to the bit.
5. **The canary**, every eighth case (below).

**The comparison** reads a frame's cells by type: a fixed-width value's bytes as
stored, text as it is, a Boolean's bit, any NaN as one NaN, so a float compares by
its bits and −0 is not +0. An answer whose order the query promises (`ast.Info`)
compares row by row; one in no promised order compares as a multiset, both sides
sorted by their cells. A refusal on one side and an answer on the other is a
mismatch; two refusals agree. A `Count` compares its number.

**The float-threads rule, the one allowance.** Step 437, which was to decide the
thread-count float bits (D8), was never built. Feature-gaps §1 records "a float
aggregate's last bits depend on the thread count … **not bit-exact**" as found, no §9
entry decides it, `WithThreads`' doc says results do not depend on it, and the code is
as 437 found it (`parallelsink.go`: batches dealt round-robin, partial states merged in
worker order). So it is undecided, and the rule takes the critic's error model, counted
each time it fires:

- A noisy column is a `Sum` or `Mean` of a Float64 in a group-by without
  `MaintainOrder` (which runs on every thread; `MaintainOrder` is serial), keyed or
  keyless. No expression reads one, no key or sort key is one, and a join's right side
  holds none (`ast.Schema` refuses each), so its noise never moves a row.
- `ast.Bounded` puts beside each its error bounds, `x.Abs().Sum()` and `x.Count()`,
  carried through every `Select` that carries the column.
- Between runs on different thread counts a noisy cell may differ, if |Δ| ≤
  2·n·eps·Σ|x| for a `Sum` and 2·eps·Σ|x| + 2 ulp for a `Mean` (eps 2^-52; each
  recursive sum is within (n−1)·u·Σ|x| of the true one, u = eps/2). A NaN among the
  values must be NaN on both sides; where Σ|x| is +Inf or past MaxFloat64/4, partial
  sums can overflow, and which infinity, or NaN, is the order's, so any answer is
  allowed. Anything else is a mismatch, and so is any difference between runs on one
  thread count: a batch size or the optimizer may not move a float's bits.
- **Stale:** the rule exists because a float sum on eight threads differs from one
  thread's. Every eighth case a **canary** sums 4,096 floats of five magnitudes, drawn
  from the case's seed, keyless, on eight threads in batches of seven rows, against the
  reference. A run with ten canaries or more and none differing fails `stale-rule`.
  The random queries alone are no measure of that: their noisy sums often cover one
  batch, one row a group, or quarters, and seeds 300,000 to 300,532 gave none that
  differed in 110 comparisons, where seeds from 1 gave 18 of 82. The canaries differ
  95–100% of the time (35 of 37, 36 of 38, 37 of 37, 50 of 50).

**Bisection:** a mismatch reruns the reference with each subset of the variant's
factors changed, single factors first, and names the fewest that reproduce it
(`caused by: batch`).

**Every run is inside `fuzzcheck.Run`** (`Limits{Wall: 1 min, Alloc: 1 GiB, API:
true}`), through a `fuzzcheck.TB` that records a failure and unwinds by a panic of its
own: ErrInternal, a panic in ursus's code or one escaping the API, an allocation past
the bound, a goroutine left a second after, are a `fuzzcheck` finding; a hang ends the
process after printing every goroutine, naming the case's seed and configuration. One
query runs before any is judged, so what ursus starts once per process is not taken
for a leak.

**Anti-vacuity:** each frame operation's nodes are counted: refused, fed rows (their
input had rows) and answered rows. A run where an operation seen 20 times over input
with rows answered rows in under a twentieth of them, or one refused all of 20 times,
fails `vacuous`. In the smoke run Filter answers rows in 125 of 257, inner joins 14 of
36, `Slice` 28 of 55.

**A finding** prints its kind, the seed, the configurations, the factors, the
differing row of both answers, the tables and the query as Go; `qcheck self -seed S
-cases 1` runs case S alone.

## 5. Teeth

`TestSelfTeeth` breaks answers through two test hooks, `corrupt` (an answer once read)
and `inject` (inside `fuzzcheck.Run`, after the query), over the smoke seed's first
cases; each break must be found, and each mismatch bisected to the factor it was put in.

| break | cases | found |
| --- | --- | --- |
| a cell's bytes changed on eight threads | 60 | **bites:** 11 mismatches, all `threads` |
| the first two rows swapped in batches of one row | 60 | **bites:** 9, all `batch` (a swap in an unordered answer or of equal rows is no difference) |
| the last row lost with the optimizer off | 60 | **bites:** 24, all `optimizer` |
| a `Count` one more from several records | 180 | **bites:** 9, all `records` |
| a refusal under `WithVerify` on one thread | 60 | **bites:** 27, all `verify` |
| the reference's second run changed | 60 | **bites:** 29 `rerun` |
| a noisy cell 1e-6 relative + 1e-3 off on several threads | 360 | **bites:** 9, all `threads` (within the bound where Σ\|x\| is large, as it should be) |
| a noisy cell one ulp off on one thread | 360 | **bites:** 5, all `batch`: the rule allows the threads alone |
| `ErrInternal` returned in batches of three | 60 | **bites:** 21 `fuzzcheck`, "an internal error" |
| a panic in batches of two | 60 | **bites:** 22 `fuzzcheck`, "a panic reached the caller" |

`TestAStaleRuleAndAVacuousOpFail` holds the run-wide checks: ten canaries none
differing fail `stale-rule`; an operation fed rows 20 times and answering none, and
one refused 20 times, fail `vacuous`; a canary that differed and an operation with
rows do not. `TestPairwiseCoversEveryPair` checks the covering array,
`TestBisectionTakesFactorsOneByOne` `Config.With` and `Diff`.

`TestSelfSmoke` is the run every ci.yml run affords: seed 20,261,114, 400 cases,
8.8 s on the laptop (4,091 runs, 1,200 comparisons), no finding, the rule firing in 4
of 39 comparisons and 50 of 50 canaries, every operation seen. The module's tests take
36 s in all, the teeth most of it.

## 6. Findings

Every finding was an allocation past fuzzcheck's gigabyte at `WithBatchSize(1)`, work
done for each batch as long as every batch or group an operator held. Each is reduced
by hand to one operator over a generated table, kept in the root package's
`tinybatches_test.go` as a differential (the answer in batches of one row, on one and
four threads, against the reference's; its allocation bounded at 128 MiB, read from
the process's count, not parallel), shown failing before the fix and passing after,
and fixed, with a CHANGELOG "Fixed" line.

| found by | what | before | after | fix |
| --- | --- | --- | --- | --- |
| seeds 11, 39, 135 | **A sort's gather** (`physical/extsort.go`): `gatherRows` made two arrays as long as the sort's kept input batches for every output batch; a sort keeps each input batch as a source | 8,191 rows: 2.3 GB, 400 ms | 22.6 MiB, 27 ms | number only the sources a gather touches (`sourceSlots`): an array where there are no more sources than picked rows, a map past that. Every caller of `gatherRows` goes through it: the sort, its spilled chunks, the as-of join, the temporal streams, the merge of sorted frames. `TestASortOfOneRowBatchesAllocatesLinearly` |
| seed 210 | **A slice from the end's accounting** (`physical/tail.go`): after each batch read, the tail recounted every batch it held in its memory account, a `Release` and a `Retain` each | the last 4,000 of 8,193 rows: 6 GB, 12 s; from before the start, all held: 8 GB, 13 s | 8–22 MiB, ≤30 ms | a batch added is retained as it comes, which is exact; a batch dropped is let go of only by a recount, since a view of an allocation a held batch shares cannot be let go of alone, and the recount comes once the batches dropped since the last reach an eighth of those held, and before the tail decides to spill. So it spills where it did; in between its account counts at most an eighth of the ring too much. `TestASliceFromTheEndOfOneRowBatchesAllocatesLinearly` (offsets −4000, −9000, −100) |
| seed 700,294 | **`NUnique`'s pair table** (`kernel/wordkeys.go`, `intkeys.go`): an accumulator reserves its group count before each batch, and `WordKeyTable.Reserve` grew its keys to that count exactly, so a group-by finding a new group each batch copied every pair each batch | a `NUnique` by 8,193 keys: 842 MB | 29.5 MiB | past their capacity the keys grow to n or twice what they held, as append grows; `IntKeyTable.Reserve` likewise. `TestANUniqueOfOneRowBatchesAllocatesLinearly` |

None changes an answer or a documented behaviour. Two change a reported figure,
within what the docs promise: the tail's account, and a run's `Peak`, can count an
eighth of its ring too much between recounts, and others reading the shared budget
see it; a key table's `NBytes`, its capacity, can be up to twice its keys where
`Reserve` grew it. Neither moves a spill of the tail's own, which recounts first.

Not findings: at `-alloc 128` and `-alloc 256`, three queries over text of 1–5 KB and
one join of 8,131 rows allocated 141–306 MiB in batches of one row, linear: 37 KB a row
of thirteen columns, batch headers and `Column.Rename`'s copies (`-alloc` is the CLI's
bound, 1 GiB by default). And two of qcheck's own bugs on the way, fixed: a `Select`
passing a noisy column through was refused by `ast.Check`, and `Bounded` named a
`Select`'s bounds before it had walked the group-by below it.

## 7. What 513 and 514 plug into

- **513, DuckDB:**
  - `qcheck/go.mod` gains duckdb-go/v2; `duck` holds the runner behind the `duckdb`
    tag with a `!duckdb` stub that reports it unsupported, as
    `bench/engines/go/duckdbengine` does; a cgo job beside the `qcheck` one.
  - `sqlgen` reads the same `ast.Query`, typed at every node with `model.Type`, and
    `model.Table`'s record, which DuckDB registers with a hidden `__rid`.
  - `compare` takes over the float tolerance: `ast.Bounded` already gives each float
    aggregate Σ|x| and n.
  - `cmd/qcheck` gains a `duck` subcommand beside `self`.
  - The refusals of §3's table are the translations' first rules: where the AST avoids
    a refusal, SQL never sees it.
- **514, the shrinker:**
  - A case is a seed (`gen.NewCase`), and a finding carries its seed, its
    configurations and the factors bisection named (`self.Finding`).
  - `shrink` shrinks the AST, whose constructors keep each step well-typed and whose
    `ast.Check` rejects one that is not.
  - `Query.Go` already prints a query as Go statements over ursus's API, which
    `TestAQueryPrintsAsGo` parses for 200 seeds: the emitted test's body.
- **469, spill and sources (H6's rest):** `model.Config` gains a memory limit and a
  source; the reference stays as it is.

## 8. CI

A `qcheck` job in ci.yml, pure Go (`CGO_ENABLED=0`), with the workflow's
`GOEXPERIMENT=simd GOAMD64=v3`, working in `qcheck/`: `go vet ./...`, `go test
-count=1 ./...` (the teeth and the smoke run), then `go run ./cmd/qcheck self -seed
<run number>000000 -duration 5m`, which prints its seed first. Not a required check;
the maintainer decides that.

## 9. Checks

- Local runs, 18 s at most each, `GOMAXPROCS=4 GOEXPERIMENT=simd GOAMD64=v3`, after
  the fixes: seeds 1,000 (856 cases), 100,000 (895), 200,000 (551), 400,000 (560),
  500,000, 600,000 (6 variants; 447, 316), 1,100,000 to 1,300,000 (`-alloc 256`; 849,
  844, 636), 1,500,000 (7 variants, 422), 2,000,000 to 4,000,000 (5 variants; 605, 424,
  543): no finding. Before them, seed 1's first 200 cases found the sort (three
  times) and seed 1's first 300 the tail; seed 700,000's 309 found `NUnique`.
- `cd qcheck && go vet ./... && go test ./...`: ok.
- After the last edit, with `GOFLAGS=-p=1 GOMAXPROCS=4 GOEXPERIMENT=simd GOAMD64=v3`:
  `go test ./internal/...`, `go test .` in full, `GOEXPERIMENT=simd go vet ./...`,
  `go run ./internal/gen/levels`, `go run ./internal/gen/api -check` (0 gone),
  `go run ./internal/gen/testinventory -check` (it skips a directory holding a
  `go.mod`, as it skips `bench/`, so `qcheck/` needed nothing; `levels` and `api` list
  `./...`, which stops there too) and `gofmt -l .`; the outcomes are in the PR.
