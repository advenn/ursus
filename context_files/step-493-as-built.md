# Step 493 — as built

**Six large source files split by topic.** The maintainer asked: "break big files."
Code moves between files within its package and nothing else changes: no name, no
signature, no comment but a one-line pointer where a section left its file.

## 1. What was there

Six files of 1,256 to 2,055 lines, each several topics under one name: a join's
breaker, build sink and probe operator in `join.go`; nine accumulators in the kernel's
`agg.go`; the Parquet source's options, schemas, readers and column-reader
construction in `parquet.go`. Each had section dividers already, so the cuts follow
them.

## 2. The change

| original (lines) | pieces (lines) |
| --- | --- |
| `internal/physical/join.go` (2,055) | `join.go` 496: `ProbeBuilder`, `joinBreaker`, the table, the spec, the helpers, `planJoin`; `joinsink.go` 537: the build sink; `joinprobe.go` 505: `joinProbeOp` and its loop; `joinmatch.go` 163: `enter`, `appendMatches`, the residual; `joinemit.go` 403: flush, `emit`, `gatherOut`, the merged key |
| `internal/kernel/agg.go` (2,007) | `agg.go` 409: `Accumulator`, `Extend`, `NewAccumulator`, the remap merge, the helpers, the accounting; `aggcount.go` 380: count, len, null_count, n_unique; `aggsum.go` 522: sum, mean; `aggminmax.go` 403: the extremum family; `aggrows.go` 334: first, last, implode |
| `internal/source/parquet/parquet.go` (1,943) | `parquet.go` 343: `Options`, `Evolution`, `Source`, `New`, `Open`; `files.go` 621: the `plan.Source` section, schemas, layouts, opening files; `reader.go` 497: the `source.BatchSource` readers; `colreader.go` 527: the encodings check and `newColReader` |
| `internal/physical/agg.go` (1,658) | `agg.go` 660: `hashAggSink`, `planAggregate`, `renameOp`, `Finish`, `residentResult`, the planning helpers; `aggconsume.go` 533: the Sink section through `consumeStrings`; `aggmerge.go` 491: `Merge`, the workers, the partitioned fold |
| `internal/source/parquet/column.go` (1,317) | `column.go` 552: the interfaces, `lazyValid`, `fixedCol`, the kept-rows path, `boolCol`; `bytearray.go` 228: `byteArrayCol`, `decimalFromBytes`; `nested.go` 559: the list and struct readers |
| `internal/expr/window.go` (1,256) | `window.go` 299: `OrderKey`, `MappingStrategy`, `Window`, `HasWindow`, `OrderedAggRefusal`; `winfn.go` 424: `WinFnOp`, `RankMethod`, `WinParams`, `WinFn`; `winresolve.go` 423: `ResolveWinFn` and the output types; `rebuild.go` 128: the generic `Rebuild` |

Each new file starts with the package clause and the imports it needs; each original's
imports are pruned to what stayed. Names follow the packages' own: flat nouns
(`joinbuild.go`, `aggmode.go`, `winsorted.go`). `internal/expr/call.go` and
`internal/kernel/unary.go` wait for a later pass; open pull requests edit them.

## 3. The checks

- Lossless: for each original, the multiset of top-level declaration names before and
  after (the original plus its pieces) is identical, by `go/parser`: 70 for `join.go`,
  103 for the kernel's `agg.go`, 78 for `parquet.go`, 39 for the physical `agg.go`,
  90 for `column.go`, 86 for `window.go`. Every moved range is byte-identical to its
  piece's body, and what stayed to the original's remainder.
- `gofmt` (the toolchain's), `GOEXPERIMENT=simd go build ./...` and `go vet` on the
  four packages: clean.
- `go run ./internal/gen/levels`: ok. `go run ./internal/gen/api -check`: 0 gone.
- `go test ./internal/...` and the root package, under `GOFLAGS=-p=1 GOMAXPROCS=4
  GOEXPERIMENT=simd GOAMD64=v3`: see the pull request.
