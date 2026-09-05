# ursus benchmark suite.
#
# Single entry point for every benchmark in this repo. Three tiers:
#
#   micro  ursus-only, public-API Go microbenchmarks (benchstat format)
#   h2o    h2o.ai db-benchmark: 10 groupby + 5 join queries, broad library field
#   pdsh   Polars Decision Support (TPC-H 22 queries), the headline comparison
#
# Everything is gated on `preflight`: this suite refuses to run on a machine
# that does not have the free memory the requested scale needs, because a run
# that swaps measures the swap, not the engine.
#
# GOEXPERIMENT=simd is exported below, same as the root Makefile. It is not
# required to BUILD ursus — the two files that import `simd` are behind
# //go:build goexperiment.simd and have scalar twins — but a benchmark should
# measure the vector path rather than the fallback, so it is on by default here.

export GOEXPERIMENT := simd
export GOAMD64      ?= v3

SHELL := /bin/bash

# ---------------------------------------------------------------- knobs -----

SF          ?= 1          # PDS-H scale factor. 1 ~= 1 GB. 10 is opt-in.
N           ?= 1e7        # h2o row count. 1e7 ~= 0.5 GB CSV.
SUITE       ?= pdsh       # pdsh | h2o
ENGINES     ?=            # comma list; empty = every enabled engine
QUERIES     ?=            # comma list; empty = every query in the suite
ITERATIONS  ?= 3          # timed iterations (one un-timed warm-up always runs)
IO          ?= parquet    # parquet | csv
THREADS     ?= $(shell nproc)
MEM_LIMIT   ?=            # e.g. 8G. Enforced via systemd-run --user --scope.
TIMEOUT     ?= 600        # per query, seconds
FORCE       ?= 0          # 1 skips the preflight hard-fail (you own the result)

UV      ?= uv
GO      ?= go
PY      := $(UV) run python
GOBIN   := $(CURDIR)/bin

DRIVER  := $(PY) -m driver.main
COMMON  := --suite $(SUITE) --scale $(SF) --rows $(N) --io $(IO) \
           --threads $(THREADS) --iterations $(ITERATIONS) --timeout $(TIMEOUT) \
           $(if $(ENGINES),--engines $(ENGINES)) \
           $(if $(QUERIES),--queries $(QUERIES)) \
           $(if $(MEM_LIMIT),--mem-limit $(MEM_LIMIT)) \
           $(if $(filter 1,$(FORCE)),--force)

.DEFAULT_GOAL := help

# ------------------------------------------------------------- lifecycle ----

.PHONY: help
help:
	@sed -n 's/^##//p' $(MAKEFILE_LIST) | column -t -s ':' | sed 's/^/  /'

## preflight        : resource + tooling gate; run this first
.PHONY: preflight
preflight:
	@$(DRIVER) preflight $(COMMON)

## setup            : create the uv venv and build the Go runners
.PHONY: setup
setup: setup-py setup-go

# --inexact so that syncing does not uninstall the optional engine groups.
# A plain `uv sync` removes anything outside the selected groups, so running
# `make bench` after `make setup-extras` silently deleted chdb and datafusion
# and turned 44 timed queries into `unsupported` rows. The failure is quiet:
# every runner reports "not installed", which reads exactly like an engine that
# was never enabled.
.PHONY: setup-py
setup-py:
	$(UV) sync --inexact

## setup-extras     : add the optional engines (datafusion, chdb)
.PHONY: setup-extras
setup-extras:
	$(UV) sync --inexact --group datafusion --group chdb

.PHONY: setup-go
setup-go:
	@mkdir -p $(GOBIN)
	cd engines/go && $(GO) build -o $(GOBIN)/runner ./cmd/runner

## setup-cgo        : additionally build the cgo engines (duckdb-go, chdb-go)
.PHONY: setup-cgo
setup-cgo:
	@mkdir -p $(GOBIN)
	cd engines/go && $(GO) build -tags duckdb,chdb -o $(GOBIN)/runner-cgo ./cmd/runner

# ------------------------------------------------------------------ data ----

## gen              : generate + normalise PDS-H data (SF=1 by default)
.PHONY: gen
gen: preflight
	@$(DRIVER) gen --suite pdsh --scale $(SF)

## gen-h2o          : generate h2o groupby + join data (N=1e7 by default)
.PHONY: gen-h2o
gen-h2o: preflight
	@$(DRIVER) gen --suite h2o --rows $(N)

## answers          : (re)build the duckdb reference results for the suite
.PHONY: answers
answers:
	@$(DRIVER) answers $(COMMON)

## sql              : regenerate engines/sql/pdsh/*.sql from duckdb's tpch extension
.PHONY: sql
sql:
	@$(PY) scripts/dump_tpch_sql.py

# ------------------------------------------------------------------- run ----

## bench            : preflight -> gen -> run -> validate -> report
.PHONY: bench
bench: preflight setup
	@$(DRIVER) gen $(COMMON)
	@$(DRIVER) answers $(COMMON)
	@$(DRIVER) run $(COMMON)
	@$(DRIVER) validate $(COMMON)
	@$(DRIVER) report

## run              : run only (assumes data and answers already exist)
#
# Depends on setup-go, and that is not tidiness. Without it `make run` happily
# times whatever binary happens to be in bin/ — which silently measured code from
# a previous session and reported h2o gb7 as 38s and FAILING VALIDATION when the
# working tree had it at 2.4s and correct. A benchmark that measures the wrong
# build is worse than no benchmark, because the number looks real.
.PHONY: run
run: setup-go
	@$(DRIVER) run $(COMMON)

## validate         : check every engine result against the duckdb reference
.PHONY: validate
validate:
	@$(DRIVER) validate $(COMMON)

## report           : rebuild REPORT.md, plots and dashboard.html from timings.csv
.PHONY: report
report:
	@$(DRIVER) report

# ----------------------------------------------------------------- micro ----

## micro            : ursus-only Go microbenchmarks (benchstat format)
.PHONY: micro
micro:
	cd micro && $(GO) test -run '^$$' -bench . -benchmem -count=$(or $(COUNT),10) . \
		| tee $(CURDIR)/results/micro.txt

## micro-baseline   : record the current micro numbers as the comparison baseline
.PHONY: micro-baseline
micro-baseline: micro
	@cp results/micro.txt results/micro-baseline.txt
	@echo "baseline saved: results/micro-baseline.txt"

## micro-diff       : benchstat the current micro run against the baseline
.PHONY: micro-diff
micro-diff:
	@command -v benchstat >/dev/null || { \
		echo "benchstat not installed: go install golang.org/x/perf/cmd/benchstat@latest"; exit 1; }
	benchstat results/micro-baseline.txt results/micro.txt

# ----------------------------------------------------------------- chores ---

## clean            : remove results, keep generated data
.PHONY: clean
clean:
	rm -rf results/raw results/plots results/REPORT.md results/dashboard.html \
	       results/timings.csv results/micro.txt

## clean-data       : remove generated datasets (they are big; regeneration is fast)
.PHONY: clean-data
clean-data:
	rm -rf data answers

## clean-all        : clean + clean-data + venv + built binaries
.PHONY: clean-all
clean-all: clean clean-data
	rm -rf .venv $(GOBIN)
