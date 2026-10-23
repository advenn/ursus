# Step 328 — as built

**`MapRows`, `MapColumns`, `Show` and `Cache`:** the rest of `polars-parity.md` §4,
Polars' `map_rows`, `map_columns`, `show` and `cache`, but `serialize`.

## 1. The API

| Method | Polars | What it answers |
| --- | --- | --- |
| `df.MapRows[T, R](ctx, fn)` | `map_rows` | `fn` of each row: a T a struct, read as `Rows` reads one, or a `[]any`, as `Row` gives one; a struct R a column a field, as `FromRows` builds them, and any other R one column, `map` |
| `df.MapColumns(ctx, names, fn)` | `map_columns` | each named column replaced by `fn` of it, under its name and in its place; an answer one value long repeated |
| `df.Show(n)` | `show` | the first n rows drawn, every one, where `String` draws ten; a negative n all but the last −n |
| `lf.Show(ctx, n)` | `LazyFrame.show` | the same, only the first n rows computed |
| `lf.Cache()` | `cache` | the query run once where it is used at several places, whatever it holds; used once, no cache |

**Where ursus differs:**

- **`MapRows` is typed.** T and R are Go types, so a frame of no rows maps to a frame
  of no rows of R's columns, where Polars cannot tell the answer's type; an
  interface R is refused. Polars names a tuple's columns `column_0` and on, where a
  struct's fields name its own.
- **`Show` gives the text to print,** as `Glimpse` does, where Polars prints it; it
  draws ursus's own table, as `String` does.
- **A cache used once is dropped,** as Polars drops one, since it would only fence a
  filter or a projection out.

## 2. How

- **`framemap.go`:** `MapRows` decodes the rows, calls `fn` on each in order, and
  builds the answer by `FromRows`, or, for one column, by `structColumns` over R as
  the one field of a struct, which is laid out as R is. `MapColumns` checks each
  answer's length and broadcasts one value by `kernel.Take`.
- **`frame.go`:** `String` and `Show` draw through one `table(rows, height)`.
- **`internal/plan`:** `LazyFrame.Cache` wraps the query in a `plan.Cache` of ID 0.
  `MarkShared(root, auto)` numbers an asked cache with more than one parent, whatever
  it holds and whether or not automatic sharing is on, and drops one with a single
  parent. `ShareSubplans` now decides only the automatic sharing: two asked caches
  must not both reach the physical plan as ID 0, where the second would replay the
  first.
- **`panic_test.go`:** the sweep of every `LazyFrame` entry point supplies an int,
  for `Show`'s limit.

## 3. Tests

**`TestFrameMaps`,** every answer Polars':

- `MapRows`:
  - a struct to a struct, and to one column;
  - a pointer's nil a null;
  - a `[]any` row;
  - no rows;
  - a failed row named;
  - an interface answer refused, and a row type that is no struct;
- `MapColumns`:
  - times ten;
  - two columns, cast and renamed, kept in name and place;
  - a sum broadcast;
  - a wrong length and an unknown name refused;
- `Show`:
  - 2, −1, and a query's 2, against `Head(2)`'s table;
  - 20 of 30 rows drawn whole;
  - 100 and −40;
- `Cache`, by a Go function's count of calls:
  - a reused Select running twice, cached once;
  - `CACHE #1` twice in the plan;
  - a cache used once, at the root and under a `Select`, gone from it;
  - two cached queries each used twice, with the rules on and with none, each its
    own rows and the function run once.

## 4. Performance

Against Polars 1.44.1, micro benchmarks, the median of 7 (of 3 for Polars' `map_rows`),
1M rows of an Int64 and a Float64:

| operation | ursus | Polars |
| --- | --- | --- |
| `MapRows`, a struct to a float | 29.3 ms | 169.5 ms, a tuple to a float |
| `MapRows`, a struct to a struct of two | 39.7 ms | 263.5 ms, a tuple to a tuple |
| `MapRows`, a `[]any` to a float | 43.8 ms | — |
| `MapColumns` of two, cast and doubled | 7.6 ms | 3.4 ms |

`MapRows` is four to seven times Polars': a Go function called a row, where Polars
calls Python. `MapColumns` itself only renames and builds a frame; the time is the
benchmark's function, a query a column where Polars' does Series arithmetic.
`Show` and `Cache` make no claim of speed: `Cache` runs a query once where it ran
twice.

## 5. Docs

- **Go functions:** "A row at a time: MapRows" and "A whole column: MapColumns".
- **Lazy and eager:** `Cache` under the rewrites, `Show` beside the frame's printing.
- **Coming from Polars:** two rows.
- **The CHANGELOG, `feature-gaps.md`, `polars-parity.md`.**

## 6. Teeth, all 13 biting

- `MapRows`' one column named otherwise;
- a `[]any` row's values out of order;
- a failed row's error dropped;
- an interface answer not refused;
- `MapColumns`' one-value answer refused;
- a mapped column renamed;
- an answer of any length;
- a negative limit as a count;
- `Show` drawing ten;
- a query's `Show` of one row;
- an asked cache never numbered;
- an asked cache used once kept: silent at first, the test's one use being the
  root, which has no parent; a cache under a `Select` was added;
- asked caches left to automatic sharing, which with it off let two reach the
  physical plan as ID 0.

**Gate:** test-all 130 ok, race 26 ok (the root package 526 s here), levels, vet ×3
and the bench engine tests clean; the 22 PDS-H answers match DuckDB.
