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
	$(GO) test -race -timeout 40m -count=$(COUNT) $(PKGS)

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

# Fuzzing (step 508). `make test` already runs every fuzz target's SEED corpus, as go
# test does, which is the part that has to be deterministic. This generates inputs,
# so it is opt-in and out of the pre-merge gate; CI's fuzz workflow runs each target
# for 30 s. go test fuzzes one target of one package at a time:
#
#	make fuzz FUZZ=FuzzScanCSV PKG=./internal/readfuzz FUZZTIME=20s
#
# A failing input is written to $(PKG)/testdata/fuzz/$(FUZZ)/, where go test runs it
# as a seed from then on. The defaults fuzz the interval parser, the first target.
# `make fuzz-list` names every target, its package first.
FUZZ     ?= FuzzEvery
PKG      ?= ./dtype/
FUZZTIME ?= 60s
# Each new input is minimised for at most this long, as fuzz.yml does (step 516).
FUZZMIN  ?= 5s
.PHONY: fuzz fuzz-list
fuzz:
	$(GO) test -run '^$$' -fuzz '^$(FUZZ)$$' -fuzztime $(FUZZTIME) -fuzzminimizetime $(FUZZMIN) $(PKG)

fuzz-list:
	$(GO) run ./internal/gen/fuzzlist

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

# The public API against the last tag's (step 401): a feature api/<newest tag>.txt
# lists that this tree no longer has fails the check: the API is stable (step 494). A
# tag's list is written at the commit tagged, as it is cut: make api-tag TAG=v0.8.0.
.PHONY: api api-tag
api:
	$(GO) run ./internal/gen/api -check

api-tag:
	@test -n "$(TAG)" || { echo "usage: make api-tag TAG=vX.Y.Z"; exit 2; }
	$(GO) run ./internal/gen/api -tag $(TAG) > api/$(TAG).txt

# The test suite's hand-typed expected values (step 504): no test function may hold
# more than internal/gen/testinventory/baseline.txt allows, or any if it is not listed;
# CI's build job runs the same check. A test's expected data is a recorded oracle's
# (make oracle), a Go oracle's or a differential's. testinventory-write rewrites the
# baseline, after a count has fallen.
.PHONY: testinventory testinventory-write
testinventory:
	$(GO) run ./internal/gen/testinventory -check

testinventory-write:
	$(GO) run ./internal/gen/testinventory -write

# The recorded oracle (step 496): Polars 2.1.0's answers to the questions
# bench/scripts/oracle asks, written to testdata/oracle/<group>.json, which TestOracle
# in the root package compares ursus against (go test -run TestOracle .). Needs a
# Python with bench/scripts/oracle/requirements.txt installed; point ORACLE_PYTHON at
# it, and GROUPS at the groups to record (all, unset). CI's oracle workflow runs the
# same command and fails on a recording that changes.
ORACLE_PYTHON ?= python3
.PHONY: oracle
oracle:
	$(ORACLE_PYTHON) -m bench.scripts.oracle $(GROUPS)

# The documentation site (context_files/docs-site-scope.md). `docs` writes the
# fragments the pages include from docs/examples; `docs-serve` previews the site at
# http://127.0.0.1:8000, and `docs-build` builds it into site/, refusing a broken
# link or a missing fragment. Both need docs/requirements.txt's tools:
# pip install -r docs/requirements.txt.
DOCS_REF ?= master
MKDOCS   ?= mkdocs

.PHONY: docs
docs:
	$(GO) run ./internal/gen/docsite -ref $(DOCS_REF)

.PHONY: docs-serve
docs-serve: docs
	$(MKDOCS) serve

.PHONY: docs-build
docs-build: docs
	$(MKDOCS) build --strict

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
