# Step 505 — as built

**The last two large source files split by topic.** Step 493 split six files and left
`internal/expr/call.go` and `internal/kernel/unary.go` for a later pass, because open
pull requests edited them; none does now. Code moves between files within its package
and nothing else changes: no name, no signature, no comment but a pointer where a
section left its file.

## 1. What was there

`call.go`, 2,934 lines: the `CallFn` enum, thirteen constant blocks a family each, its
classifiers and names; the `Call` node and `ResolveCall`, which dispatches on the
family; then every family's output types, `ResolveHorizontal` alone 477 lines.
`unary.go`, 1,633 lines: `Unary` and its operator families in the first 455, then
`Cast` and everything a cast calls, the numeric conversions, the temporal rescaling,
formatting to text, the Enum and Categorical casts. Each had the enum's family blocks
or the kernel's `# ...` headings for dividers, so the cuts follow them.

## 2. The change

| original (lines) | pieces (lines) |
| --- | --- |
| `internal/expr/call.go` (2,934) | `call.go` 250: `Call`, `Field`, `CallOutputName`, `ResolveCall`, the literal-argument readers; `callfn.go` 647: `CallFn`, its constants, classifiers and names; `callhorizontal.go` 680: `CutBreaks`, `ResolveHorizontal` and its helpers, `OrderCmpType`; `callgen.go` 345: the general and maths families, `ReplaceParts`, `MembershipType`, `DecimalRounded`, `PhysicalOf`, `ReinterpretOf`; `callstr.go` 149: the string family, `GroupNames`; `calldt.go` 394: the temporal family, `GridRefusal`, `OffsetRefusal`, the zone and roll policies; `callnested.go` 517: the list, struct and arr families, `withFieldsOut` |
| `internal/kernel/unary.go` (1,633) | `unary.go` 457: `Unary`, the null and NaN predicates, `unaryArith`, `unaryI128`, `unaryInt`, `unaryFloat`, `unaryMath`, `trig`, the unsigned sign; `cast.go` 499: `Cast`, `castTo`, `durationNegates`, `timeInDay`, `normaliseTime`, `checkWidths`, `rescaleTemporal`, `readTicks`; `castnum.go` 375: the conversions among Bool, the integers, Int128 and the floats; `caststring.go` 185: `formatToString`, `readAnyInt`, `FloatText`, `AppendFloatText`; `castenum.go` 165: `castEnum`, `castCategorical`, `ToPhysical`, `EnumPhysical` |

Each new file starts with the package clause and the imports it needs; each original's
imports are pruned to what stayed. Names follow the packages' own: flat nouns
(`winfn.go`, `castdec.go`, parquet's `nested.go`). The `CallFn` enum left `call.go`
as `WinFnOp` left `window.go` in step 493: the original keeps the node and the
dispatcher.

## 3. The checks

- Lossless: for each original, the multiset of top-level declaration names before and
  after (the original plus its pieces) is identical, by `go/parser`: 310 for `call.go`
  (77 declarations, the constant blocks' names counted), 47 for `unary.go`. Every
  declaration of every piece, its doc comment included, is byte-identical to the
  original's text, and the only comments outside a declaration are the two pointers in
  `call.go` and the one in `unary.go`.
- `gofmt` (the toolchain's), `GOEXPERIMENT=simd go build ./...` and `go vet` on the
  two packages: clean.
- `go run ./internal/gen/levels`: ok. `go run ./internal/gen/api -check`: 0 gone.
- `go test ./internal/expr/... ./internal/kernel/... ./internal/physical/...` and the
  root package, under `GOFLAGS=-p=1 GOMAXPROCS=4 GOEXPERIMENT=simd GOAMD64=v3`: ok,
  the root in 253 s; the other widths and the race detector on the pull request's CI.
