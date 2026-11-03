# Step 421 — as built

**A cancelled query stops calling `rolling_map`'s Go function.** H8 of
`v0.7.2-scope.md` (found by its reading), the plan's 430.

## 1. What was wrong

`rollingMap`, the kernel behind `Expr.RollingMap`, called the caller's function under
`context.Background()`. The function's wrapper looks at its context before each
window, but that context never ended, so a query cancelled after its tenth window
went on calling the function for the rest: 199,998 calls over 200,000 rows. A slow
function, an HTTP call a window, held its query, and the request's goroutine, as long
as the whole column.

## 2. The change

The query's context goes through the window kernels: `kernel.WinCall` takes it, from
the four physical callers, the window sink, the streamed windows, their carried state
and `List().Eval`'s windows, and `winRolling`, `rollingCentered` and `rollingMap` pass
it on. Each was given a context it already had, but `carried`, which takes its
caller's.

## 3. Tests

- **`TestACancelledRollingMapStops`** (root): over 200,000 rows on one thread, a
  `RollingMap` whose tenth call cancels its query is called at most a thousand times,
  and the query gives `context.Canceled`. Before the change, 199,998 calls.

**Teeth: 1 of 1 bites:** `context.Background()` again calls it 199,998 times.

**Gate:** the kernel and physical packages' tests; the root package's rolling,
window, over, ewm, rank, cumulative, list, eval, spill and stream tests; vet. CI's
every width, the scalar build and the race detector on the pull request.
