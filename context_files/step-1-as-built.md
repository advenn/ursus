# Step 1 — as built

What actually exists, what was corrected against the original design, and what was
learned by building it. This is the authoritative document where it disagrees with
[`ursus-api.md`](./ursus-api.md), which remains the longer-range vision.

**Status:** the walking skeleton runs end to end.

```go
df, err := ursus.Frame(cols...).
    Filter(ursus.Col("price").Gt(5)).
    Select(ursus.Col("id"), ursus.Col("price")).
    Collect(ctx)
```

121 tests green under `GOEXPERIMENT=simd` × `GODEBUG=simd={512,256,128,0}`, with the
experiment off, and under `-race`.

---

## 1. What was built

| Package | Level | Contents |
| --- | --- | --- |
| `internal/uerr` | L0 | Error type, kinds, plan-node attribution, Damerau-Levenshtein near-match suggestions |
| `internal/arrowx` | L0 | The only package importing arrow-go: 64-byte-aligned allocator, ownership model |
| `internal/bitmap` | L5 | `View` / `Builder`, bit offsets made unforgeable, `Words` iterator, set ops |
| `dtype` | L10 | **Public.** Comparable interned `DataType`, `TypeID`, `TimeUnit`, `Field`, `Schema`, promotion lattice, cast legality |
| `internal/data` | L20 | `Column` (contiguous), `Series[T]` (typed view), `Batch`, `StringAccessor` |
| `internal/expr` | L20 | Sealed `expr.Node`, node set, matchers, expansion, type resolution |
| `internal/kernel` | L30 | Five kernel classes, scalar set, SIMD comparison family, `take`/`filter`/`concat`, dispatcher |
| `internal/source` | L30 | Runtime scan contract: `ScanSpec`, `BatchSource`, `Openable` |
| `internal/plan` | L40 | `plan.Node`, Scan/Filter/Project/Limit, resolve pass, optimizer, projection pushdown, Explain |
| `internal/source/memsrc` | L45 | In-memory source honouring projection pushdown |
| `internal/physical` | L50 | Evaluator, `Operator` / `BatchOp` / `stage`, operators, physical planner |
| `internal/exec` | L60 | Serial driver, `Collect` / `Batches` / `Count` |
| `ursus` | L70 | `Expr`, `Literal`/`Operand`, `LazyFrame`, `DataFrame`, `Values`, `Frame`, struct decoding |
| `ursustest` | L80 | `AssertFrameEqual`, `AssertSchemaEqual`, `AssertSeriesEqual`, `AssertPlan` |
| `internal/gen/levels` | L0 | CI-enforced import-level checker |

Import levels are enforced by `make levels`, wired into CI. A package may import
strictly lower levels only.

---

## 2. Corrections to the original design

Every one of these was a defect in `ursus-api.md` / `dataframe-features.md`.
Several were hard compile errors.

| # | Was | Now |
| --- | --- | --- |
| D2 | `DataType` documented comparable but had `Fields []Field` — non-comparable, cannot be a map key | 16-byte struct, unexported fields, nested payload behind an **interned `*typeExt`**. `==` and `map[DataType]T` both work; nested types ship |
| D3 | `Literal` union had `~int64 \| time.Duration` (overlapping type sets) and `[]byte` without `~` | `time.Duration` dropped from the union — `~int64` already admits it and a type switch recovers the name. Every integer width enumerated, since inference does no implicit widening |
| D4 | Four duplicate declarations: `Field`, `Coalesce`, `ScanFunc`, `Time` | Expression `Field` deleted; `StructOf`/`TimeOf`/`DateOf` convention applied consistently |
| D5 | `LazyFrame.Schema()` took no context but must read a Parquet footer | **`CollectSchema(ctx)`**; `Explain(ctx, ...)` too |
| D10 | `LazyFrame` mutation semantics unstated | **Persistent.** Every builder returns a fresh frame over an immutable plan; tested |
| D11 | `exprNode` named three times, never defined | Sealed `expr.Node` with `Field(*Schema) (Field, error)` + `Children()`. **The core deliverable of the step** |
| D12 | Expr→plan→physical boundary never specified | Expansion is a plan-time, schema-driven rewrite in `plan.Resolve`, running before type-checking and before any optimizer rule |
| D13 | Kernels contradicted the null semantics; `null + 1 == null` was inexpressible | Five kernel classes; **Total kernels are not given the validity bitmaps at all**, so a kernel author cannot get propagation wrong |
| D13(2) | `Masked` recipe zero-fills, wrong for Min/Max/Product | Documented: aggregates must use `IfElse(mask, identity)`. (Aggregates are v0.2; the rule is recorded in `kernel.go`) |
| D14 | Scalar twin behind `//go:build !goexperiment.simd` — only one impl links, so the mandated differential test was impossible | Scalar compiles **unconditionally**; only the ~8-line dispatch var block is build-tagged |
| D14b | Movemask idiom assumed 8 lanes = 1 byte | `bitmap.Builder.AppendBits(word, n)` owns the packing; correct at every width. Regression-tested at chunk sizes 1–64 |
| D18 | `Filter` null semantics unstated, `Remove` ambiguous | `Filter` keeps a row iff the predicate is **valid AND true**; `Remove(p...)` is `NOT(p1 OR p2 ...)`. Both documented and tested |
| D20 | Scalar lifting inconsistent across signatures | `Operand` applied uniformly, including multi-argument (`IsBetween[L, H Operand]`) |
| D22 | `plan.Node` called unexported but is exported from its package | Acknowledged; `ursus.FromPlan` added so a future SQL frontend can hand back a frame without a cycle |
| — | `take`/`filter`/`concat` compaction kernels | Named in **no** design document, needed by every operator. Implemented |
| — | The expression evaluator | Never even named. Implemented as `physical.Eval`, with its contract written down and tested |

---

## 3. Things learned by building, that no document predicted

### 3.1 `simd.LoadXxxPart` lies under emulation — a real Go 1.27 bug

`LoadFloat64sPart` is documented to return "the number of elements loaded".

| Mode | 10-element slice, 2 lanes | |
| --- | --- | --- |
| Hardware (`GODEBUG=simd=128`) | returns **2** | ✅ matches the doc |
| Emulated (`GODEBUG=simd=0`) | returns **10**, loads 2 | ❌ contradicts the doc |

A kernel written to the documented idiom `i += n` therefore **skips elements and
silently produces wrong answers under emulation**. It is invisible on this
development machine because 8 float64 lanes swallow every test slice shorter than 9.

`v.Len()` and `StorePart`'s return value are correct in both modes. ursus derives
its loop step from those and discards `LoadPart`'s count — see `kernel.step`.

**This was caught by the `GODEBUG=simd=0` CI leg and by nothing else.** It is the
concrete justification for the width matrix.

### 3.2 `gofmt` on PATH cannot parse generic methods

Under goenv, bare `gofmt` resolves to the globally pinned Go (1.26.2 here), not the
toolchain `go.mod` selects. It fails with

```
method must have no type parameters
```

which reads exactly like a compiler error and sends you hunting for a
language-version problem that does not exist. **Always `go fmt`, never `gofmt`.**
Noted in the Makefile.

### 3.3 The arrow-go bet is empirically sound

The Phase 0 risk gate (`make riskgate`) measured all three load-bearing claims:

- **64-byte alignment** holds for every size class, including the sub-512-byte ones
  where a bare `make([]byte, n)` is routinely misaligned.
- **Un-`Release`d buffers are reclaimed.** 4000 MiB allocated and dropped without a
  single `Release` → **+20 KB** heap delta. Arrow's refcounting is advisory under the
  Go allocator, so ursus hides it completely: no public type has a `Release` method.
- **Zero-copy reads cost nothing.** 67.4 µs (native slice) vs 66.8 µs
  (`Float64Values`) vs 65.6 µs (`arrow.GetValues[T]`) — Arrow accessors are
  marginally *faster*, 0 allocs.

### 3.4 A width-only type check is a type-punning hole

`data.Values[T]` originally validated T against the column's **bit width**. Int64
and Float64 are both 64 bits, so `Values[float64]` succeeded on an Int64 column and
returned the integer bit patterns reinterpreted as floats. The id column `1,3,4,6`
rendered as `5e-324, 1.5e-323, 2e-323, 3e-323` — denormals plausible enough to be
mistaken for a real computation, and reachable through the public
`df.Column[float64]("id")`.

The check is now on the exact physical `TypeID`. Reading a Datetime column as
`[]int64` still works — that is the intended punning, by which one integer kernel
serves Date, Datetime and Duration — and nothing else does. Regression-tested in
`internal/data/data_test.go`.

Found by looking at rendered output, not by a test. Worth remembering: the test
suite was fully green while this was live, because every assertion happened to read
columns at their correct type.

### 3.5 Transpositions need Damerau-Levenshtein

Plain Levenshtein charges a transposition two edits, so with the distance budget a
short column name can afford (1), `"pirce"` produced no suggestion for `"price"` —
the most common typo there is. Switched to optimal string alignment.

---

## 4. Deliberately not built

Parquet and CSV sources, sinks, joins, group-by/aggregation, sort, window
functions, streaming and spilling, morsel parallelism, string/temporal namespaces,
selectors, SQL, plan serialization, the generated eager façade, `Categorical`.

Each has a named seam:

- **Group-by / joins / sort** — new `plan.Node` types; `TransformUp`, `Expressions`
  and projection pushdown's conservative default already handle unknown nodes
  correctly, so they are correct-but-unoptimized the day they land.
- **Morsel parallelism** — `Filter` and `Project` are already `BatchOp`s. The v0.2
  scheduler consumes the same values; only `internal/exec`'s loop changes. Zero
  kernels, zero operators, zero evaluator touched.
- **Parquet** — implement `plan.Source` + `source.Openable`. `memsrc` is the
  50-line reference implementation.
- **More SIMD families** — every one has the same shape as the comparison family;
  the width-safe movemask and the differential harness are already there.

---

## 5. Honest gaps

- **Kernel coverage is 9.1%.** The differential test covers float64 only, which is
  what "one proof family" means — but the integer, string and boolean kernel paths
  are exercised only incidentally by the end-to-end tests. Widening this is the
  first thing to do in step 2.
- **`Column.Slice` copies for fixed-width columns.** It is O(1) for Bool and String
  but allocates for numerics, because `Column` carries no buffer offset. Nothing on
  the current hot path uses it (`Filter` goes through `Take`, which copies anyway),
  so adding an offset field would put a `+ c.offset` in every kernel's inner loop to
  buy nothing today.
- **`Cast` routes numeric conversions through float64.** Correct for every pair
  ursus can currently construct, and the Uint64/Int64 promotion that would break it
  is rejected upstream — but it is a placeholder, not a real cast matrix.
- **Predicate pushdown is not implemented.** `plan.Scan.Predicate` and
  `plan.Caps.Predicate` exist and are threaded through; the rule does not.
- **Projection pushdown is the only optimizer rule.** The framework (`Rule`,
  run-once vs fixed-point, `Flags`, schema-preservation verification) is built to
  take more.

---

## 6. Verification

```bash
make test-all   # GOEXPERIMENT=simd × GODEBUG=simd={512,256,128,0}, plus experiment-off
make race
make riskgate   # the two arrow-go claims
make levels     # import-level invariants
go test ./... -update   # rewrite golden plan files
```
