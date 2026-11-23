---
description: >-
  Install ursus, the Go dataframe library, with go get. It needs Go 1.27 for generic
  methods; SIMD kernels are optional and pure Go throughout, with no cgo.
---

# Installation

```sh
go get github.com/advenn/ursus
```

That is the whole install. ursus is pure Go, with three direct dependencies, Apache
Arrow's Go library, klauspost's compression and `golang.org/x/text`: no cgo, no C or
C++ toolchain, no Python runtime and no sidecar process. It cross-compiles like any
other Go module.

## Requirements

**Go 1.27 or newer.** Two of its changes are load-bearing:

- **generic methods** are why `df.Column[int64]("qty")`, `df.Rows[Sale]()` and
  `Series[T].Map[U]` exist at all;
- a generic method still cannot satisfy an interface, so every public type is a
  concrete struct, and the polymorphism is kept in unexported interfaces.

## Your first program

Make a module and add ursus to it:

```sh
mkdir hello && cd hello
go mod init hello
go get github.com/advenn/ursus
```

Put this in `main.go` and `go run .`:

--8<-- "Example_install.main.md"

## SIMD kernels

ursus's compute kernels have SIMD versions for amd64, behind
Go's `simd` experiment. They are **optional**: switch them on when you build,

```sh
GOEXPERIMENT=simd go build ./...
```

and without the experiment, or on another architecture, every kernel falls back to its
scalar twin. That fallback is not a promise but a test: CI runs the whole suite with the
experiment off on every push, as it runs it at four vector widths with it on. The flag
buys speed, not correctness.

`GOAMD64=v3` lets the Go compiler use AVX2, FMA and BMI in ordinary code too. ursus's
own builds and benchmarks use it; any amd64 machine from the last decade has them.

## Platforms

ursus runs wherever Go does. Its memory budget, on by default at half the memory the
machine or the container allows and shared by a process's queries, reads that
allowance on Linux (cgroups included), macOS and Windows.

## Versions

ursus is on [ZeroVer](https://0ver.org), at 0.x, and a stable 1.0 is not expected
yet. Its public API is stable all the same, from v0.8.0: a release adds, and never
renames, removes or reshapes what an earlier release had, and CI fails a change that
takes a feature of the last release's away. Up to v0.7.2, Go's rule for v0 applied,
and a release could rename, remove or reshape an API. The
[changelog](../reference/changelog.md) says, for each release, what a user of the one
before can trip on, with what to do instead. Pin a version in your `go.mod`, and read
the changelog before you move.

These docs are versioned too: the selector at the top of the page switches between
releases, and `dev` is the development branch.
