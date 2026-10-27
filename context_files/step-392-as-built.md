# Step 392 — as built

**The 0.8 scope.** No code changed.

[`v0.8-scope.md`](./v0.8-scope.md) takes the theme the maintainer set on 2026-10-27,
ursus inside a Go service, once 0.7's performance work had run out of cheap gains,
and orders it by what a service feels: safety in a shared process first, then small
queries' latency and many queries' throughput, then the API, then the docs. 0.7 is
closed first by its report and tag, at the maintainer's word.

- **Timed,** with a probe of four queries, a filter, a group-by, a join to a 100-row
  dimension and a sort's head, over 1,000, 10,000 and 100,000 rows, on all threads and
  one, against Polars 1.44.1: under 10,000 rows all eight threads take one and a half
  to two and a half times one thread's time; ursus is faster than Polars at 1,000
  rows, and its join 3.2 times Polars' at 10,000.
- **Many at once:** the 10,000-row group-by from 1 to 64 goroutines, 2.6 times one
  at 64 on eight cores; at 64, 4.9 GB allocated a second and 18% of the CPU the
  collector's; up to 769 goroutines, each Collect starting its own.
- **Cancelled:** a group-by over ten million rows returns within 3 ms of its cancel, a
  sort 330 to 380 ms after, its kernels taking no context; no goroutine left.
- **Memory:** a budget is a query's, half the machine each; the first query sets the
  process's Go limit; a group-by of five million keys maps 2.1 GB for 153 MB of input
  and leaves 1.85 GB of RSS until the runtime returns it.
- **Surveyed** by reading, by two agents and checked line by line: each query's
  goroutines and their recovery, `wg.Go` escaping `TestEveryGoroutineIsCovered` in six
  helpers; process-wide state and init, 1.8 ms at start; where the context is
  checked; the inputs and outputs, the API's size and breaks, the errors, hooks and
  docs a service would use.

The probe lived in the scratchpad; the scope moves it into the repository with its
first item.
