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

-   :material-table-pivot:{ .lg .middle } **[Reshaping](reshaping.md)**

    ---

    Pivot and unpivot, explode and unnest, concatenating frames, and transpose.

-   :material-format-text:{ .lg .middle } **[Strings](strings.md)**

    ---

    Cleaning, matching with regular expressions, extracting, replacing, splitting,
    and parsing numbers and dates.

-   :material-code-brackets:{ .lg .middle } **[Lists and structs](lists-and-structs.md)**

    ---

    List columns and the questions to ask of them, expressions over their elements,
    sets, and struct fields.

-   :material-calendar-clock:{ .lg .middle } **[Dates and times](dates-and-times.md)**

    ---

    Parsing, calendar arithmetic, time zones, ranges, and groups by day, week or a
    rolling period.

-   :material-help-rhombus-outline:{ .lg .middle } **[Missing data](missing-data.md)**

    ---

    Counting nulls, filling them from a value or the neighbours, coalescing,
    interpolating, and dropping.

-   :material-language-go:{ .lg .middle } **[Go values in and out](go-values.md)**

    ---

    Frames from Go slices, typed columns as Series, rows into structs, and streaming
    an answer a batch at a time.

-   :material-function:{ .lg .middle } **[Go functions](go-functions.md)**

    ---

    Go functions inside a query, a value or a batch at a time, with nulls, errors and
    concurrency.

-   :material-file-table:{ .lg .middle } **[CSV, Parquet and Arrow](io.md)**

    ---

    Scans and their options, globs, object storage, pushdown into Parquet, sinks, and
    Arrow in and out.

</div>
