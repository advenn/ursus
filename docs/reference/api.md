---
description: >-
  The API reference of ursus, the Go dataframe library, generated from its source on
  pkg.go.dev, with runnable examples beside the methods they document.
---

# API reference

The reference for every package, type and method is generated from the source on
pkg.go.dev, for this version of ursus:

--8<-- "api-link.md"

Its doc comments say why a thing is the way it is, not only what it does, and the
runnable examples beside the methods are run by `go test`, as this site's are.

The packages:

| Package | What it holds |
|---|---|
| [`ursus`](https://pkg.go.dev/github.com/advenn/ursus) | frames, expressions, scans and sinks: everything a query needs |
| [`ursus/dtype`](https://pkg.go.dev/github.com/advenn/ursus/dtype) | the type system, re-exported by `ursus` as `ursus.Int64`, `ursus.Schema` and the rest |
| [`ursus/ursustest`](https://pkg.go.dev/github.com/advenn/ursus/ursustest) | assertions for tests: frames equal, and golden plans |
| [`ursus/i128`](https://pkg.go.dev/github.com/advenn/ursus/i128) | the 128-bit integer behind `Int128` and `Decimal` |
