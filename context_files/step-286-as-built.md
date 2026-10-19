# Step 286 — as built

**Tier 2 of `v0.6.6-scope.md`, the rest: `hist` and `cumulative_eval`.** This
finishes tier 2, and with it the scope's items. The light report is next.

## 1. What changed

**`Hist(opts...)`,** Polars' `hist`, ported from
`crates/polars-ops/src/chunked_array/hist.rs` at `py-1.44.1`
(`internal/kernel/listhist.go`):

- **Options:** `HistBins(edges...)`, `HistBinCount(n)`, `HistBreakpoint()` and
  `HistCategory()`, as Polars' `bins`, `bin_count`, `include_breakpoint` and
  `include_category`. Both bins and a count are refused, as Polars refuses them, and
  so are edges that do not increase.
- **A bin count, 10 by default:**
  - equal widths over the least value to the greatest, NaN aside, each edge
    `x·width + offset`, the last the greatest itself;
  - one value, or values all alike, are the bins over the value ± 0.5, and no value
    the bins over [0, 1];
  - counted as Polars counts: a value's bin by its scaled offset, corrected a bin
    either way against the edges, the least value in the first.
- **Edges given:** the values sorted and walked against them. The first bin holds
  its left edge; a value outside the edges, or NaN, is in no bin; fewer than two
  edges make no bin.
- **The answer:** each bin's count, a UInt32, or a Struct of its right edge, its
  interval and its count.
  - **The interval** is written in Polars' float display (`polarsFloat`, from
    polars-core's `fmt_float`): one decimal for a whole number below 999999, six
    decimals with the trailing zeros cut for a long shortest form, and Rust's
    scientific notation outside [1e-6, 999999]. So "[1.0, 3.333333]",
    "(1.2346e-7, 0.1]", "(1e7, 1.5000e300]".
  - Polars' category is a Categorical; here it is text, as `Cut`'s labels are.
- **A reshape,** `ReshapeHist`, a column of its own length in a `Select` and a list a
  group in an aggregation, by `list.hist` (`FnListHist`, the list block at 700).
  - The edges ride in the `Reshape`'s arguments as their bits, as `sample`'s fraction
    does.
  - In a `Select`, the values are imploded, counted, and exploded; no bin is no row.
- **The sweeps:** the names test counts 208, the sliced-list floor is 37 of 38, and
  the contract drives `list.hist` by a count and by edges with both includes.

**`CumulativeEval(body, opts...)`,** Polars' `cumulative_eval`:

- **The answer:** each row's answer is body over the values of its partition up to
  and including it, in the window's order. A row whose prefix holds fewer than
  `MinSamples` values, 1 by default, is null.
- **The body:**
  - it reads `Element()` and is lowered as `List().Eval` lowers an aggregate
    (`expr.LowerCumulativeEval`), to one value a list of the prefixes' column;
  - one that is not one value a prefix is refused, as Polars refuses non-scalar
    output, and so is one reading another column.
- **The op:** `WinCumulativeEval`, its lowered body in `WinParams.Body`, so its text
  is part of the window's identity.
- **The physical window evaluates it** (`internal/physical/cumeval.go`), as no kernel
  can evaluate an expression.
  - Prefixes overlap and a list column's offsets cannot share them, so they are built
    a run of rows at a time, about a million values at once, as a list column the
    body is evaluated over.
  - The time is a prefix a row, as Polars', and the memory a run's.
- **Under a memory limit,** a cumulative_eval over a partition larger than the limit
  is refused, as its rows each read their whole prefix. The refusal's hint, which
  said every window function streams, now names it.
- **Refused elsewhere:**
  - inside `List().Eval`, "not there yet";
  - in `GroupBy(...).Agg`, as every window is, where Polars gives a list a group.

## 2. Tests and teeth

Every answer is Polars 1.44.1's.

- **`TestHist`:**
  - 10 bins, 3, none, edges, one edge;
  - 3 and 4 bins with both includes; edges with both, with a breakpoint; on the edges;
  - integers, alike, no rows, one bin, a Float32;
  - NaN alone, once and beside values;
  - the last edge the greatest;
  - the labels over ten edges in each branch of the float display;
  - 0 to 19 in 10 bins; by group;
  - the name and type;
  - edges falling, both given, text and a date refused.
- **`TestCumulativeEval`:**
  - first less last squared, a sum, a mean from 2, a sum over g, a rank's last;
  - a null first, two ways;
  - count, len, integers' max less min, text's min;
  - a value an element and another column refused.
- **`TestCumulativeEvalPastAChunk`:**
  - 3,000 rows, several runs, against `CumSum`, over the frame and over a key;
  - the refusal under 16 KiB naming cumulative_eval.

| tooth | result |
| --- | --- |
| edges a step on | **bites** |
| the last edge computed | **bites**, once −5.7 to 3 was added |
| alike, ± 1 | **bites** |
| no correction a bin either way | **bites** |
| the least value in no bin | **bites** |
| the first edge open | **bites** |
| NaN in the first bin | **bites** |
| seven decimals | **bites** |
| Go's exponent | **bites** |
| the first bin open | **bites** |
| no bin a null row | **bites** |
| min samples ignored | **bites** |
| a prefix without its row | **bites** |
| a run's answers from its start | **bites** |
| a value an element taken | **bites** |

**One tooth was silent at first.** Computing the last edge as `k·width + least`, not
taking the greatest, matched every probe range. A search found −5.7 to 3 in 5 bins,
where it is 2.999999999999999.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
