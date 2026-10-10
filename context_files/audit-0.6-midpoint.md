# The 0.6 midpoint audit

**Taken on 2026-10-10, after step 165,** at the maintainer's request: what is done,
what is left, the numbers, what must improve, and what later versions should
build. Step 166 recorded it and fixed its small findings (§4.1, 4.2, 4.3, 4.7's
fan-outs and 4.9); the rest are placed in `v0.6-scope.md`.

**Sources:**

- git history, line counts and test counts;
- `bench/results/REPORT.md` and its raw timings;
- `audit.md`, `v0.6-scope.md`, and steps 156–165;
- `CHANGELOG.md` and `README.md`;
- two read-only surveys of the tree: every open item, and code health.

## 1. The numbers

**History and size:**

| | |
| --- | --- |
| commits / releases | 374 commits since 2026-09-04; v0.2.0, v0.3.0, v0.3.1, v0.4.0, v0.5.0; 9 commits since v0.5.0 |
| recorded steps | 165 as-built records |
| code / tests | 66.2k non-test Go lines, 67.7k test lines; 1,192 test functions; 26 packages |
| public API | about 1,177 exported functions and methods in the root package |
| gate | test-all 115 package runs, race 23, levels, vet ×3, bench engine tests, 22 PDS-H answers |

**Correctness:**

- Every step's PDS-H answers at SF=0.1 match DuckDB's.
- `audit.md`: 87 of 95 rows fixed, 2 partly (J8, S21), 6 open (O12, O13, J11, I23,
  I26, S26).

**Speed, from the last full report** (v0.5.0, 2026-10-09):

| suite | ursus | Polars | ratio |
| --- | --: | --: | --: |
| PDS-H SF=1 | 276 ms | 78 ms | 3.56× |
| PDS-H SF=0.1 | 29 ms | 9 ms | 3.09× (see 4.1) |
| h2o 10M, Parquet | 947 ms | 547 ms | 1.73× |
| h2o 10M, CSV | 2,195 ms | 1,116 ms | 1.97× |
| PDS-H SF=1, peak memory | 1.10 GB | 0.84 GB | 1.31× |

Step 157's same-session baseline: ursus 302 ms, Polars 97 ms, 3.12×. Polars' own
geomean read 70, 78 and 97 ms on three days.

**0.6 so far, each step against the one before it.** CPU per iteration from rusage,
SF=1. These are not cumulative, and the cumulative figure against v0.5.0 has not
been measured yet:

| step | change | measured |
| --- | --- | --- |
| 158 | a probe row that misses does no work | q17 −19%, q16 −12% |
| 159 | integer join keys never encoded | q17 −29%, q16 and q7 −15%, q12 −12%, q2 −11% |
| 160 | integer group-by keys | q18 −11%, q13 −5%; h2o 1M gb4/5/8 −19 to −21% |
| 161 | Semi and Anti joins build in parallel | q4 wall −45%, q22 wall −44%; q4 CPU +17% |
| 162 | join sides exchanged at runtime by actual rows | q12 −24% (wall −17%), q9 −7%, q14 −6% |
| 163 | the Parquet reader's columns take its slices | q19 −7%; allocation 1.67× → 1.21× |
| 164 | word-wise Kleene, conjunct filters, integer `IsIn` | q6 −23%, q15 −24%, q14 −14%, q7 −11% |
| 165 | implied predicates through joins | q19 −12%; its join is no longer a cost |

## 2. Done in 0.6 (steps 156–165)

**Hygiene:**

- H1: the record corrected;
- H2: the version stance written down: 0.x for good, breaks allowed.

**Speed:**

- items 0–5 done;
- item 7 half done: the miss path is built, column reuse is not;
- one item found and built on the way: the miss path.

**Not yet started:**

- item 6, runtime filters (designed, §5);
- item 7's other half;
- the two found items: `LIKE` as a regular expression, and `First`/`Last` per-group
  columns;
- features F1–F5, robustness R1–R5, the API breaks, the report.

## 3. Left for 0.6

| group | item | size |
| --- | --- | --- |
| speed | 6: runtime filters, one hop (q2); design below | M |
| speed | 7: probe columns reused when every row matched once | S |
| speed | `LIKE '%a%b%'` as substring searches, not a backtracking regexp (18% of q13) | S |
| speed | `First`/`Last` keep typed per-group state (0.8–1.3 s over 182k groups) | S–M |
| features | F1 `Corr`/`Cov`/`MinBy`/`MaxBy`, F2 `ReplaceTimeZone`, F3 `Rolling*By`, F4 small leftovers, F5 `WithFields` | M, M, M, S×5, S |
| robustness | R1 streaming `MergeSorted`/`JoinAsOf`, R2 the open audit rows, R3 a default budget off Linux, R4 a Polars-oracle suite, R5 memory test gaps | S–M each |
| API breaks | `DynamicOptions` refused fields; `WithOptFlags(plan.Flags)`; and those found in 4.4 | S–M |
| record | final report (Polars in one session, ursus against v0.5.0, `gb10`/`j5` under 3 GB); v0.6 docs | S |

## 4. Must be improved (this audit's findings)

1. **The record was wrong about SF=0.1** (fixed, step 166). The raw timings behind
   `REPORT.md` give 29.0 ms, 3.09× Polars, at step 154's run. `step-154-as-built.md`
   and the 0.5 documents built on it say 24 ms and 2.55×. Correct them, and the
   CHANGELOG and README where they repeat it.
2. **`Makefile`'s header said `GOEXPERIMENT=simd` is MANDATORY** (fixed, step 166).
   The package doc (`ursus.go:34`) says it is optional, and the Makefile's own scalar
   run proves it is. The header was stale.
3. **An unguarded goroutine** (fixed, step 166, by 4.7's shared fan-out). In
   `internal/physical/agg.go`, `foldPartitioned`'s routing `wg.Go` has no
   `uerr.GuardErr`, unlike its neighbour, so an internal panic there kills the
   process instead of becoming an error.
4. **Internal types in the public API.** These are unusable outside the module:
   - `Scan(plan.Source)`, `FromPlan(plan.Node)`, `LazyFrame.Plan()`,
     `WithOptFlags(plan.Flags)`;
   - `DataFrame.Batch() *data.Batch`, kept alive by `var _ = data.Batch{}`;
   - option types (`CSVOption`, `ParquetOption`, `WindowOption`...) whose underlying
     types are internal.

   Under "break when it helps" these go into 0.6's Breaks: unexport them, or give
   them public types.
5. **No default budget off Linux** (R3). On macOS and Windows nothing spills unless
   the caller sets `WithMemoryLimit`, and no GOMEMLIMIT is set.
6. **The CSV reader still copies every column** (`csv/parse.go` through `NewFixed` and
   `NewStringParts`). Step 163's owned constructors apply wherever its builders do not
   reuse their slices.
7. **Duplicated code worth folding:**
   - `groupKeys` and `builtKeys` are one int-or-bytes wrapper, written twice;
   - the integer-key path is written three times;
   - there are five per-bucket spill writers;
   - `foldPartitioned` and `eachConcurrently` were two worker fan-outs, which
     differed in panic guarding; step 166 made the second the only one.
8. **Unit coverage is thin in `internal/plan` (0.15 test lines per line) and
   `internal/expr` (0.22).** The root's end-to-end tests (5.5×) cover them
   indirectly. R4 and targeted rule tests would make a rule's failure name the rule.
9. **The documents disagreed with each other** (fixed, step 166). The README's and
   CHANGELOG's "Not done" lists differ: only the README names `RollingMap`, IPC and
   NDJSON; only the CHANGELOG names list set operations, `Sample`, `WithFields` and
   `List().Eval`. `dataframe-features.md`'s §14 roadmap is stale, still listing SQL
   and object stores for v0.3.
10. **One function is 391 lines:** projection pushdown, `(*pushState).pushdown` in
    `rule_projection.go`. It is the one place a split would aid review.
11. **The 0.6 target is unmeasured:** within 3× of Polars, and 20% faster than
    v0.5.0. Every figure above is one step against its parent.

## 5. Runtime filters (item 6): the design, step 167

- **Planner rule** (after `build_side`): for an inner or semi join with one integer
  key whose probe column has exactly the key's type, outside `NullsEqual`:
  - walk the probe side down, through Filter, plain Project renames, WithColumns
    that do not redefine the key, and the non-null-extended side of a join;
  - never into a Cache from outside it;
  - wrap the deepest node with a new plan node, `RuntimeFilter{Input, Key, Slot}`,
    and give the join the same slot;
  - skip it if that point is the join's own input, where it would gain nothing.
- **The join** publishes its integer key tables to the slot after freeze.
- **The physical operator** keeps every row until something is published, and also
  when the join exchanged its sides or keys its table by encoded bytes. Otherwise it
  drops the rows whose key is surely absent. A row is dropped only if it cannot
  join.
- **Tests:** answers against the rule turned off; placement in the plan; an inventory
  golden for the new node; a CACHE'd subtree like q2's; teeth.

## 6. For later versions (0.7 onward)

**Speed:** after 0.6, q19 is 61% Parquet reading. In order of expected return:

1. dictionary decoding into indices (M);
2. predicates evaluated inside the reader (L–XL);
3. columns decoded in parallel within a row group (M);
4. late materialisation of join output (L);
5. runtime filters chained through build sides, for q7 and q21 (L);
6. CSE by structure (M);
7. the parallel CSV splitter (M);
8. a String column that refers to its dictionary (XL).

**Out of core:** spill for `Rolling`, `GroupByDynamic` and as-of; streaming
`MergeSorted` and `JoinAsOf` if R1 slips.

**Features, the largest gaps against Polars:**

- **expression-level manipulation:** `sort`, `sort_by`, `head`/`tail`, `unique`,
  `gather`, `filter`, `replace`, `rle`, `sample`, `search_sorted`; and `DropNulls`,
  `Explode` and `Unnest` as expressions;
- **the list namespace:** `eval`, `filter`, set operations, `n_unique`, `arg_min`,
  `gather`, `to_struct`;
- **the struct namespace:** `with_fields`, `rename_fields`, `json_encode`; and `.arr`,
  `.bin`, `.cat`;
- **temporal:** `replace_time_zone`, `days_in_month`, business days, `combine`, the
  offsets;
- **rolling:** by time, median and quantile, `center`;
- **frame-level:** `pivot`, `transpose`, `partition_by`, `to_dummies`, `sample`; and
  selectors;
- **types:** Categorical (declared, reserved), Float16, an Array column; temporal
  mean, median and variance.

**I/O breadth, deferred from 0.6 by your choice:** Arrow IPC files, NDJSON, hive
partitioning, schema drift.

**Out by decision:** SQL and object stores.
