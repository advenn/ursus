# ursus — build targets.
#
# GOEXPERIMENT=simd switches on the SIMD kernels, and every target below exports it.
# It is optional: without it, package `simd` is excluded by its build constraints
# and each kernel falls back to its scalar twin, which test-all runs the whole suite
# against. This said it was mandatory, from before the scalar fallbacks existed; the
# package doc (ursus.go) and test-all's own scalar run have said otherwise since.
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

# The interval parser is the only place a user's text becomes arithmetic, and
# FuzzEvery is the module's only fuzz target. `make test` already runs its SEED
# corpus — go test does that for every fuzz target — which is the part that has to
# be deterministic. This generates, so it is opt-in and out of the pre-merge gate.
FUZZTIME ?= 60s
.PHONY: fuzz
fuzz:
	$(GO) test -run '^$$' -fuzz 'FuzzEvery' -fuzztime $(FUZZTIME) ./dtype/

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
