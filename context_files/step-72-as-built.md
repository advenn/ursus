# Step 72 — as built

**Literals keep their type, and a panic never kills the host.** This is `audit.md`
§11, items 3 and 4: O3, and recovering panics, with S3, A6, A7, I11, I12 and I14
also fixed at their causes.

Ten commits and this document.

Authoritative where it disagrees with the vision docs and [`design/`](./design/).

Each of these was asserted on its own exit code:

- `make test-all` exit 0, with **105** package-ok lines: 21 packages × 5 SIMD
  configurations. `internal/exec` has a test file now, which is why it is not 100.
- `make race` exit 0 (21).
- `make levels` and `go vet` clean in all three modules.
- PDS-H SF=0.1 exit 0, with **22/22** matching the duckdb reference.

No benchmarks. **The 32 golden plans did not move**: the new identity key is never
rendered. The suite is at **2547** tests.

---

## 1. Two measurements first

### Literals: six wrong answers

`Lit.String()` renders a strong literal's value and not its type. So `Lit(int8(100))`
and `Lit(int64(100))` both render `lit(100)`, as do `1` and `1.0`, and `float32(0.1)`
and `0.1`. Four map instances merged computations on that rendering.

`literal_identity_test.go` has one case per merge site, each answered by hand:

| case | today | by hand |
| --- | --- | --- |
| aggregate, `i8 + int8(100)` beside `i8 + int64(100)` | b = −111 | 401 |
| aggregate, `x*1` beside `x*1.0` | `ErrInternal` | 2 and 2.0 |
| aggregate, `f + float32(0.1)` beside `f + 0.1` | `ErrInternal` | a Float32 and a Float64 |
| `GroupByDynamic`, `+ int8(127)` beside `+ int64(127)` | b = −128 | 128 |
| window temporary, the same pair under `Max().Over(g)` | b is a's Int8, and `CollectSchema` says Int8 | 201, as Int64 |
| window partition, `Over(k * int8(2))` beside `Over(k * int64(2))` | b is partitioned by a's wrapped keys | 1, 10, 100 |

`TestLiteralRenderingsCollide` pins the premise: seven pairs render alike and are
different literals.

### Panics: nineteen of twenty wrong

**A panic in a worker goroutine kills the process that ran it**, so these cases
cannot run in the test binary. `panic_test.go` runs each in a **child process**: this
test binary again, told by `URSUS_PANIC_PROBE` to run one case and print a report.

- The child recovers anything that reaches the caller.
- It counts the goroutines still running a second after the call.
- The parent classifies what it gets back: a **crash**, a **hang**, a **caller
  panic**, or an answer to judge.

The plan said caller-goroutine panics would be measured in-process. I put every case
in the child instead: one harness then tells a worker's crash from a caller's panic.

| case | today |
| --- | --- |
| a panicking udf, through every driver | a crash at 4 threads; a caller panic at 1 |
| `Str().Slice(1, MaxInt64)` | a crash |
| `Rank(RankMethod(99))` | a crash |
| `Quantile(Interpolation(99))` | **silently linear**: [2 3 5] |
| `List(Int8)`, `List(Int16)` written by another writer | a caller panic: `data.NewFixed` refuses int32 values under Int8 |
| `WithCompression(Lz4)`, `(Lzo)` | a panic in arrow-go, after the query had run |
| the zero `Expr` as an operand, or aliased | a nil dereference |
| a row group that claims more rows than it holds | **a hang**, cut off by the child's timeout |
| every byte of a 573-byte file flipped, two ways | 17 caller panics among 1146 reads |

`Select(Expr{})` was the control: already refused. Both instruments are two-way
ratchets, `knownLiteralDefects` and `knownPanicDefects`, and **both are empty now**.
The panic ratchet lists each defect by the prefix of how it fails, so a case that
starts failing in a new way fails too. The engine-wide recover moved every prefix at
once, from a crash to an internal error, and the ratchet made that commit say so.

## 2. `expr.Identity`

`Identity(n)` is `String()` followed by a length-prefixed part for every node, in
pre-order:

- for every literal, its Go type, its DataType and its rendering;
- for every udf, the id `NewUDF` minted.

`IdentityAll` is the list form, for partition keys. The key is never shown to
anyone, and that is what lets it carry an id that would make every golden plan depend
on allocation order.

**Why a string, and not the values compared with `==`:**

- `==` merges −0 with +0, and `x/−0.0` and `x/0.0` are −Inf and +Inf.
- It never merges NaN with NaN, which compute alike.
- A `[]byte` in a map key panics.

fmt prints the shortest string that reads back as the same float, so −0 prints "-0".
A rewritten tree would have needed a Node type of its own, in a package whose type
switches promise to be exhaustive.

**The four sites** are `resolve_window.go`, `physical/agg.go` (which serves both the
aggregate and the temporal group) and `physical/window.go`.

**Including the udf id can only split a merge.** So two same-named udfs no longer
merge even where `CheckUDFNames` has not run. The refusal stays, because the name is
all a person sees of a udf. Its hint said "the planner deduplicates by rendered
form", and it now says that.

**The three pin tests would have passed either way.** They merged udfs built as
composite literals, with id 0. They now pin both halves, what must merge and what
must not, with udfs from `NewUDF`.

"Three maps that dedup on `String()`" was stale in seven files. It says four dedup
maps now.

## 3. A panic is an error

`internal/uerr/panic.go`:

- **`PanicError`** holds the value, the site, the stack and the callers.
  - The site is the first frame below `runtime.gopanic` that is not the runtime's
    own, so an index out of range names the code that indexed.
  - It unwraps to a `runtime.Error`, but **never to an `*uerr.Error`**. `MustValues`
    panics with a KindType error, and the internal error it becomes must not answer
    `errors.Is(ErrType)`.
- **`FromPanic`** makes a panic in ursus's own code KindInternal. An internal
  `*Error`, ursus's own assertion, passes through unchanged.
- **`Attributed`** is for code ursus calls but did not write.
- **`Catch`** is deferred directly. **`Guard`** and **`GuardErr`** wrap a call.

**Recovered around the call, not the goroutine.** Each lane reports its error
in-band, and a recover around a whole worker would send on a lane its own deferred
close had already closed.

- `physical.Pull` is `Next` under `Catch`. The dispatchers of `parallelOp`,
  `parProbeOp` and `parallelSink` pull through it, and so do exec's `Collect`,
  `Batches` and `Count`.
- Each worker guards what it runs: `apply`, `startBatch`, `stepCurrent` and
  `Consume`.
- The CSV convert goroutines catch into their own error slot.
- **A parallel aggregate's workers drain without consuming** once anything has
  failed. They used to keep feeding every sink, including the one that had just
  panicked. The dispatcher's `context.Canceled`, which is only the failure's echo, is
  no longer joined to the error.

**Every public entry point recovers:** `Collect`, `Count`, `CollectSchema`,
`Explain`, `CollectInto`, `WriteParquet`, `WriteCSV`, `Record`, `ArrowSchema` and
`Rows`.

- **`CollectBatches` guards only `compile`.** Go forbids a range function to recover
  a panic from the loop body, so a panic in the caller's own loop stays the caller's.
  `TestALoopBodyPanicIsTheCallers` pins that.
- **The `Once` sites recover inside the function `Do` runs.** A panic that escaped
  it would leave the `Once` done, and every later call would get no schema and no
  error.
- **Constant folding declines** an expression whose kernel panics, as it declines
  one that fails.

**Whose panic it was decides the Kind:**

| panicked in | Kind |
| --- | --- |
| ursus | `ErrInternal`: "recovered a panic", the site, "this is a bug in ursus" |
| a `MapElements` or `MapBatches` function | **KindValue**, naming the udf, the row and the column, as its returned errors do. A flag set around the call tells it from the code around it. |
| arrow-go, reading a Parquet file | **KindIO**: the file is corrupt |
| `ScanArrow`'s factory | KindIO: it is the caller's code |

For a Parquet read, **the innermost frame in either arrow-go or ursus decides**. A
panic whose innermost such frame is ursus's own stays `ErrInternal`, and
`PanicError.Callers` is there for that.

## 4. The known causes

1. **`Str().Slice`** computed its end as `start+length` in int, which wrapped. It
   compares in int64 first, as `listSlice` always has.
2. **`RankMethod` and `Interpolation`** gain `Valid()`. `ResolveWinFn` and
   `Agg.Field` refuse an undeclared value with KindValue, on every route: a window,
   and `FromPlan`. Each kernel names every declared value, with an internal error for
   anything else. Rank's average arm had been the default, writing into a Float64
   buffer that a Uint32 output never allocated.
3. **`List(Int8)` and `List(Int16)`** elements narrow as a flat column's do.
4. **LZ4 and LZO**: `NewWriter` asks `compress.GetCodec` first and refuses with
   KindUnsupported, naming `Lz4Raw`. An `Lz4Raw` round trip is the control. Row groups
   start through `AppendBufferedRowGroupChecked`.
5. **The zero `Expr`**: `node()` is nil-safe and returns a fresh `expr.Err`. It is
   fresh because plan layers annotate an error in place. All 41 direct reads of the
   field go through it, and `TestOnlyNodeReadsTheField` keeps it that way.
6. **A corrupt Parquet file**:
   - A row group that claims more rows than its column chunks hold is KindIO. It
     looped forever, holding the reader's lock.
   - The loop checks `ctx`.
   - A panic in arrow-go is recovered in four places: in `openFile`, which closes the
     file; in the schema `Once`; in `Open`; and in `Next`, where the error is
     sticky.

   **`Open` was found by the sweep.** After the engine-wide recover, three flipped
   bytes were still `ErrInternal`. `Open` reads the first row group eagerly, outside
   `Next`, so only the entry point had caught those three.

## 5. The instruments

- **The literal sweep** reads the `Literal` union's terms from `expr.go` with
  `go/parser`, and fails on a term without samples. It asserts equal identities
  **exactly** when two literals share a Go type, a DataType and their bits: 67
  literals and 484 ordered pairs that render alike. The floor is 400. **The commit
  message says 188 literals and "over 50 pairs". I wrote that number without
  measuring it; it is 67 and 484.**
- **The panic harness**: 28 cases.
  - A **raw kernel udf**, built without the public wrapper, is how a bug in an ursus
    kernel looks to the engine. It goes through every driver, and stays
    `ErrInternal` after the udf attribution.
  - `export_test.go` adds `ExprOf`, the only way to put a hand-built node into a
    public query.
- **Internal fakes** panic in each dispatcher and worker, with a leak check, plus a
  sink that counts what it is fed after a panic. Each serial loop in exec gets the
  same test.
- **`TestEveryGoroutineIsCovered`** names all six `go` statements in the engine, each
  with its test. A new one fails until it says what recovers it.
- **`TestEveryEntryPointRecovers`** reflects over every `*LazyFrame` method that runs
  a query, and runs each on a plan that panics as it resolves: an `Alias` with no
  child, which no constructor builds. Each must return a **recovered** panic, not
  merely an internal error.
- **`TestTheZeroExprIsAnErrorEverywhere`** calls all 136 methods of `Expr` and its
  namespaces on the zero `Expr`.
- **The corrupt sweep** must also see at least one KindIO error that came from a
  recovered panic. Otherwise it would no longer test the recovers at all.

## 6. Teeth

Every patch was checked to have applied, and every one ran against a green baseline.
Four first attempts did not count and were re-aimed:

- three left an unused import and did not compile;
- one patch matched twice.

| reintroduce | fails |
| --- | --- |
| `Identity` is `String()` only | the literal sweep, the expr tests, the pin tests, the by-hand cases |
| no DataType part | the sweep (a Duration against an int64) |
| no udf part | the three pin tests, the expr test — and nothing end to end, where `CheckUDFNames` refuses first |
| each site back on the rendering | its by-hand cases and its pin test |
| `Unwrap` exposes an `*Error` / no pass-through / runtime frames kept | the three uerr tests |
| `Pull` without `Catch` | the exec test, and the public one-thread cases |
| each dispatcher calling `Next` directly | its fake's test, which crashes the binary |
| each worker unguarded | its fake's test, or the public join case |
| the sink keeps consuming / reports its own cancel | the counting sink's two assertions |
| the CSV convert or its `Once` unrecovered | `TestConvertAllRecoversAPanic`, `TestSchemaPanicIsNotForgotten` |
| the Arrow `Once` unrecovered | the `ScanArrow` case |
| each entry point uncaught | the entry-point sweep |
| a recover around `yield` | the loop-body test |
| udf attribution removed / the row not tracked | the eight udf cases / the one-thread case |
| each known cause reverted | its case, and `TestStrSliceToTheEnd`, `TestAnUndeclaredMethodIsRefused` |
| `Next` or `Open` unrecovered / `corrupt` always internal | the corrupt sweep |
| `openFile` **and** the schema `Once` unrecovered | the corrupt sweep |
| `node()` not nil-safe / one direct read | the zero-`Expr` sweep and cases / the field guard |

**Silent, fifteen of them, and recorded rather than tested around:**

- **No Go-type part in the identity.** For every literal the API builds, the DataType
  determines the Go type.
- **No length prefixes.** They are a defence against an ambiguous DataType rendering
  (O13), and nothing reaches one.
- **The probe workers' `stepCurrent` guard.** A udf key panics in `startBatch`, whose
  tooth bites. A udf is never moved into a join residual, so no public query reaches
  a panic while stepping. The case I added to reach it evaluated in a filter above
  the join, and I removed it.
- **`CollectInto`'s own `Catch`**, because `Collect` catches first.
- **`Record`'s and `Rows`' `Catch`**, where no panic is known.
- **The folding guard**, because no public constant expression reaches a panicking
  kernel.
- **The `inFn` split**, because nothing makes the code around a udf panic.
- **The rank kernel's default**, because resolution refuses first. The quantile
  kernel's is the same shape, and was not run.
- **`AppendBufferedRowGroupChecked`**, because `GetCodec` refuses first.
- **The `ctx` check in the Parquet loop**, because the row-count guard fires first.
- **`openFile`'s recover alone, and the schema `Once`'s alone** — two teeth. Each
  covers the other, and removing both bites: 10 of the sweep's panics are in the
  footer, and `openFile` catches them.
- **`corrupt` always KindIO.** No panic in ursus's own code is reachable in a Parquet
  read.
- **The sticky reader error**, because every driver stops pulling after an error.

## 7. Behaviour changes

- **A panic is an error.**
  - In ursus's own code: `ErrInternal`, with the site.
  - In a udf: KindValue, naming the udf and the row.
  - In a corrupt Parquet file, or in `ScanArrow`'s factory: KindIO.
- `Str().Slice` with a length past the end slices to the end.
- An undeclared `RankMethod` or `Interpolation` is refused with KindValue.
  `Interpolation(99)` used to be silently linear.
- LZ4 and LZO are refused when the writer is made, before the query runs.
- Parquet lists of Int8 and Int16 read.
- A Parquet row group that holds fewer rows than it declares is KindIO, where it
  used to hang.
- The zero `Expr`, anywhere, is KindValue.
- `CheckUDFNames`' hint is reworded.
- A parallel aggregate's failure no longer carries "context canceled".

## 8. Still open

- **Recorded at step 72** in `audit.md`. Each was run, except S22 and S24, which
  would need the memory they exhaust.
  - **O13**: Enum and struct types render ambiguously.
  - **O14**: a udf calling `runtime.Goexit` truncates the rows silently. `Count` was
    2 of 5.
  - **I25**: a panic in `ScanArrow`'s reader is `ErrInternal`.
  - **I26**: `Rows` decodes no List.
  - **S22**: `Pad*` and `ZFill` with a huge width are a fatal out-of-memory.
  - **S23**: a panicking `MapName` is `ErrInternal`.
  - **S24**: `NewString`'s offsets wrap past 2 GiB.
  - **A14**: `Closed(99)` acts as `ClosedLeft`.
  - **A15**: a udf's error names its row within the batch.
- **510 of the 1146 flipped-byte reads returned a result.** A flipped value byte reads
  as another value. Parquet's page checksums are optional, and ursus neither writes
  nor verifies them.
- **§11 continues with the float64 go-between** (S1, S2 and S4, and `strict`
  threaded through), then the join promotion paths.
- **Step 70 and 71's leftovers:**
  - P1, W1, O4, O5, O6, O8b and O9;
  - I23 and I24;
  - the rest of §5.
- README debt.
