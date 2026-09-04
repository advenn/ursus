# ursus — build targets.
#
# GOEXPERIMENT=simd is MANDATORY, not optional: package `simd` does not compile
# without it ("build constraints exclude all Go files in .../src/simd"). Every
# target below exports it. If you invoke `go` by hand, export it too, or set it
# in your shell profile and in your IDE's run configuration.
#
# GOAMD64=v3 only affects ordinary scalar codegen (AVX/FMA/BMI baseline). It has
# no effect on package `simd`, which dispatches on vector width at runtime.

export GOEXPERIMENT := simd
export GOAMD64      ?= v3

GO      ?= go
PKGS    ?= ./...
COUNT   ?= 1

.PHONY: all
all: vet test

.PHONY: build
build:
	$(GO) build $(PKGS)

.PHONY: vet
vet:
	$(GO) vet $(PKGS)

.PHONY: test
test:
	$(GO) test -count=$(COUNT) $(PKGS)

.PHONY: race
race:
	$(GO) test -race -count=$(COUNT) $(PKGS)

# The width matrix. `simd=0` forces software emulation (where ToArch() returns an
# unnameable internal bridge type) and `simd=128` gives 2 float64 lanes instead of
# 8, so sub-byte bitmap writes straddle bytes. These two legs are what catch
# hardcoded vector-width assumptions; without them the width bugs ship.
.PHONY: test-widths
test-widths:
	@for w in 512 256 128 0; do \
		echo "=== GODEBUG=simd=$$w ==="; \
		GODEBUG=simd=$$w $(GO) test -count=$(COUNT) $(PKGS) || exit 1; \
	done

# Same matrix, plus a build with the experiment off, proving the scalar-only
# path still compiles and passes. This is the full pre-merge gate.
.PHONY: test-all
test-all: test-widths
	@echo "=== GOEXPERIMENT off (scalar only) ==="
	GOEXPERIMENT= $(GO) test -count=$(COUNT) $(PKGS)

.PHONY: bench
bench:
	$(GO) test -run '^$$' -bench . -benchmem $(PKGS)

# The cross-engine benchmark suite lives in bench/ and has its own Makefile.
# `make -C bench help` lists its targets; this is just a shortcut so the entry
# point is discoverable from the root.
#
# Note this does NOT replace `make bench` above, which is still the in-repo
# microbenchmarks. bench/ compares ursus against polars, duckdb, pandas,
# datafusion, chdb, duckdb-go, arrow-go, gota and qframe on TPC-H and the
# h2o.ai db-benchmark.
.PHONY: bench-suite
bench-suite:
	$(MAKE) -C bench bench

# The Phase 0 risk gate: proves the two empirical claims the arrow-go bet rests on
# (zero-copy kernel parity, and that un-Released buffers are reclaimed by the GC).
.PHONY: riskgate
riskgate:
	$(GO) test -run 'TestRiskGate' -v ./internal/arrowx/
	$(GO) test -run '^$$' -bench 'BenchmarkRiskGate' -benchmem ./internal/arrowx/

# Import-level invariant check: a package may only import strictly lower levels.
.PHONY: levels
levels:
	$(GO) run ./internal/gen/levels

# Use `go fmt`, never bare `gofmt`.
#
# `go fmt` shells out to the gofmt belonging to the toolchain go.mod selects.
# A bare `gofmt` is whatever is first on PATH — under goenv that is the pinned
# global version, which here is 1.26.2. A 1.26 gofmt cannot parse generic methods
# and fails with
#
#	method must have no type parameters
#
# which reads exactly like a compiler error and will send you hunting for a
# language-version problem that does not exist.
.PHONY: fmt
fmt:
	$(GO) fmt $(PKGS)

.PHONY: clean
clean:
	$(GO) clean -cache -testcache
