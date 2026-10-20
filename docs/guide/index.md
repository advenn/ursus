---
description: >-
  The ursus user guide: how to select, filter, aggregate, window and join data with
  the Go dataframe library, chapter by chapter, every example run by the test suite.
---

# User guide

Each chapter takes one kind of work and shows it in code that runs: every block is a Go
example the test suite runs, and the output under it is what it printed. The
examples read the quick start's [`sales.csv`](../getting-started/quickstart.md), eight
orders, its dates read as dates, unless a chapter says otherwise:

--8<-- "func.sales.md"

<div class="grid cards" markdown>

-   :material-filter-variant:{ .lg .middle } **[Selecting and filtering](selecting.md)**

    ---

    Columns by name, pattern or type; rows by predicates; sorting, top-k, unique rows
    and slices.

-   :material-sigma:{ .lg .middle } **[Aggregation and group-by](aggregation.md)**

    ---

    Aggregates per group and over the whole frame, conditional aggregates, lists per
    group, and summaries.

-   :material-chart-timeline-variant:{ .lg .middle } **[Window functions](windows.md)**

    ---

    An aggregate beside every row, cumulative and shifted values, ranks, rolling
    windows by rows and by time, and exponential averages.

-   :material-set-merge:{ .lg .middle } **[Joins](joins.md)**

    ---

    Every join kind, keys, suffixes and validation, as-of joins, joins on a condition,
    and filters by another frame.

</div>
