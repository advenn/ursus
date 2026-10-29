---
description: >-
  Running ursus, the Go dataframe library, inside a Go service: queries built once and
  shared, results streamed to an http.ResponseWriter, cancellation by the request's
  context, errors as HTTP statuses, a shared memory budget and threads, metrics from
  a query hook, embedded data, and what a large query leaves in memory.
---

# ursus in a service

A service runs many small queries at once, each for a request that may go away, in a
process whose memory a container bounds. This chapter is what ursus does for it, and
what it asks of it. *New in 0.8*, most of it.

## A query built once, run by every request

A `LazyFrame` is never changed: every method gives a new one. A query is built once,
as the service starts, and each request adds what is its own, a filter by the
parameters it was given, and runs it. Any number of goroutines may run one at once.

The answer goes to the client as it is made: `WriteCSV`, `WriteNDJSON`, `WriteJSON`,
`WriteParquet` and `WriteIPC` write to any `io.Writer`, and an `http.ResponseWriter`
is one.

--8<-- "Example_serviceHandler.md"

A `DataFrame` is never changed either, so reference data loaded once, a frame of
prices or of regions, may be read from every request, joined to its query with
`df.Lazy()`.

A writer sends rows as they come, so the status goes with the first byte: a query
that fails after it can only cut the body short. Where the status has to say whether
the query worked, `Collect` it first and write the frame. `CollectBatches` gives the
answer a batch at a time, for an encoding of the service's own, and holds no more
than the batch:

--8<-- "Example_serviceBatches.md"

## A request that goes away

A query runs under the context it is given. Give it the request's, `r.Context()`: a
client that disconnects, or a deadline that passes, stops the query within some tens
of milliseconds, a sort or a join's build among it, and the call returns the
context's error, `context.Canceled` or `context.DeadlineExceeded`, never a part of
the answer as the whole.

--8<-- "Example_serviceDeadline.md"

## Errors a client can be told

A failure of ursus's own is an `*ursus.Error`, which `errors.As` reads: its `Kind`,
the operation, the message, the plan node, and the hints under it, a near column's
name or a knob to turn. The kinds are what a service answers by: a column or a value
the request named wrongly, or a limit the query met.

--8<-- "func.statusOf.md"

--8<-- "Example_serviceBadColumn.md"

`errors.Is(err, ursus.ErrSchema)` and the other sentinels match as they did. A panic
in a query is an error too, `KindInternal`, naming the line that panicked, so no
query takes the process down; a panic in a [Go function](go-functions.md) a query
calls, `MapElements`' or `MapBatches`', is a `KindValue` error naming it.

## Memory and threads, shared

The queries of a process share one memory budget, half the memory the process may
use, and its threads, `GOMAXPROCS`: an `Engine`'s, `DefaultEngine()` unless a query
is given another with `WithEngine`. A query spills to disk once the engine's queries
together hold more than the budget and it more than what the others leave it, never
less than an even share; it is refused only when it alone holds more than the whole
budget. A query that reads a batch or less runs on one thread; a larger one takes its
share of the engine's threads, divided among those running on more than one.
[Memory and spilling](../concepts/memory.md) says the rest.

A service makes an engine of its own where it wants its own figures: a budget for
its requests and another for its nightly exports, say. Its hook hears of every query
as it ends, with the query's context, its duration, rows, peak memory, spills and
error, for the service's metrics; ursus imports no logging or tracing package.

--8<-- "Example_serviceEngine.md"

`Stats()` gives what an engine's queries hold now, their peak, how many run and how
many files they have spilled, for a gauge.

## Data the service carries

Every reader takes an `fs.FS` too: `ScanCSVFS`, `ScanParquetFS`, `ScanNDJSONFS`,
`ScanJSONFS`, `ScanIPCFS` and `ScanIPCStreamFS`. So the data a service embeds in its
binary reads as a file does:

```go
//go:embed testdata/sales.csv
var data embed.FS
```

--8<-- "Example_serviceEmbed.md"

A pattern is a file's name, a directory's, read whole, or a glob, `**` among it. Data
in an object store is read through the `From` readers' openers, `ScanParquetFrom`
and its kin, a ranged read for each column chunk a query needs: see
[from object storage](io.md#from-object-storage).

## What a large query leaves behind

A query holds what it needs and lets it go as it ends, but the Go runtime returns the
memory to the operating system slowly. A group-by of five million keys over ten
million rows, 153 MB of input, maps 2.1 GB at its peak; once it has answered, the
live heap is the input's 153 MB again, and the process's RSS stays at 1.85 GB until
the runtime returns the pages, or 181 MB at once after `debug.FreeOSMemory()`. A
service measured by its RSS sees its largest query's peak for a while after it.

The budget is half of what the process may use because the Go heap grows to about
twice what is live before it collects. So that the heap's slack stays under the
ceiling too, the first query run under the default budget sets the Go runtime's soft
memory limit to nine tenths of it, process-wide (`SetProcessMemoryLimit`). A service
that sets `GOMEMLIMIT`, or calls `ursus.LeaveProcessMemoryLimit()` before its first
query, keeps its own; an engine given `WithEngineMemoryLimit` leaves the runtime's
limit alone.
