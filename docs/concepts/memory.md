---
description: >-
  Out-of-core execution in ursus, the Go dataframe library: a memory budget its queries share,
  sort, group-by, join, unique and window functions that spill to disk, and errors that
  name the operator that cannot.
---

# Memory and spilling

A query in ursus runs under a memory budget. When the operators that hold data across
batches, a sort, a group-by, a join's build side, reach it, they write what they hold
to disk and carry on, and the answer is the one they give in memory. An operator that
cannot spill fails at the budget with an error naming itself, instead of growing until
the operating system kills the process.

## The default budget

The queries of a process share one budget, by default **half the memory the process
can have**: the smaller of its container's limit and the machine's memory on Linux,
and the machine's memory on macOS and Windows. Half, because ursus's buffers live on
the Go heap, which grows to about twice what is live before it collects.

Under that default, ursus also sets the Go runtime's soft memory limit near the same
ceiling, so the heap's slack stays under it. `GOMEMLIMIT`, or calling
`ursus.LeaveProcessMemoryLimit()` before the first query, keeps a program's own choice.

## Queries at once

The budget belongs to an `Engine`, and the queries run on it share it:
`DefaultEngine()` for a query given none. A query spills once the engine's queries
together hold more than the budget and it holds more than the others leave it, never
less than an even share; alone, it spills where it would on a budget of its own. It is
refused only when it alone holds more than the whole budget, so another query's
holding makes it spill sooner, not fail.

A service can make engines of its own, one for its requests and another for its
exports, say, each with its budget and spill directory, and read what their queries
hold for its metrics:

--8<-- "Example_memoryEngine.md"

Each query looks at the budget as it goes, so at a moment the engine's queries can
hold more than it, up to about one and a half times it with sixteen large queries at
once. *Changed in 0.8:* each query had a budget of its own, half the memory each.

A Parquet scan's pages, decompressed and read at once, count on its engine too, up to
about 100 MiB for every column of a wide table read on eight threads: the engine's
other queries spill sooner beside it, and its own query does not, as the bytes are
about to be consumed.

## A limit of your own

`WithMemoryLimit` sets the budget for one query, apart from its engine's, so it spills
where it would alone; `WithSpillDir` sets where its files go, and `WithMemoryStats`
reports what it held and how many files it wrote. A group-by
over 25,000 keys under 256 KiB:

--8<-- "Example_memorySpill.md"

The spill files are removed when the query ends, on success, on an error, and when a
loop over `CollectBatches` breaks early. `WithMemoryLimit(0)` turns the budget off.

## What spills

- **sort,** and a top-k with any k;
- **group-by,** including the values a median, a quantile or an `NUnique` holds,
  the pairs of a `CorrSpearman` and the counts of a `Mode`;
- **join,** both sides partitioned;
- **unique;**
- **window functions,** over many partitions or one huge one;
- **keyed `Rolling` and `GroupByDynamic`;**
- **tail and reverse.**

`JoinAsOf`, `MergeSorted` and `HStack` stream their input, as `Rolling` and
`GroupByDynamic` do, so they hold little to begin with. *Changed in 0.6.7:* a
`CorrSpearman` over a group larger than the limit spills its pairs, in a group-by, a
window or a temporal group, and a `Mode` its counts in a group-by or a temporal
group, where each failed.

## What does not

A few answers are as large as their input, and cannot be divided across files:
`Implode` holds every value of a group as its answer, for one. Each such operator
fails at the budget, and its error says what it holds, why it cannot spill, and what
the alternatives are:

--8<-- "Example_memoryRefusal.md"

`Collect` itself holds its whole answer, by definition. An answer larger than memory
goes through `CollectBatches`, or `WriteParquet` and `WriteCSV`, which stream it.

## A larger-than-memory query

A sort of more Parquet than fits in memory, written to a file as it comes out of the
sort, holding at most 512 MiB:

--8<-- "Example_memorySink.md"

Each path that spills is tested under a limit far below its input, against its answer
without one.
