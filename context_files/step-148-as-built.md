# Step 148 — as built

**The first half of item 6 of `v0.5-scope.md`: audit A8.** `Diff`, `PctChange` and
`FillNull(FillMean)` now work inside `.Over(g)`. On the way, any window body built
around an aggregate or an ordered function works too, as in
`(x - x.Mean()).Over(g)`.

## 1. Evidence first

**A8 (audit.md):** `Diff`, `PctChange` and `FillNull(Mean)` inside `.Over(g)` were
refused as "a window inside a window", where Polars computes each per group. Each is
sugar that builds a window the user never wrote:

- `Diff(n)` is `w - w.Shift(n)`, and `Shift` is an ordered function with no `Over`
  of its own, which `extractWindows` treats as a window over the frame;
- `PctChange` is built the same way;
- `FillNull(FillMean)` is `Coalesce(x, x.Mean().Over())`.

**Reading the window path found a wider refusal.** The physical window accepts a
body that is exactly one aggregate or one ordered function, and nothing else:

- `(x - x.Mean()).Over(g)`, the commonest window in Polars code, was refused as
  "not a window function";
- so was `x.Sum().Cast(Float64).Over(g)`.

That refusal came only from the physical planner, so Explain printed a plan that
Collect would not run.

## 2. What changed

### `distribute` (`internal/plan/resolve_window.go`)

**What it does.** A window whose body is not one aggregate or one ordered function
hands its partition, order and mapping down to the body's windowed parts:

- each aggregate and each ordered function in the body is windowed alone, by the
  outer keys;
- each window a sugar built takes the outer keys in place of its own;
- the outer window is then gone.

So `(x - x.Mean()).Over(g)` becomes `x - x.Mean().Over(g)`, and `x.Diff(1).Over(g)`
becomes `w - w.Shift(1).Over(g)`.

**Why it is the same answer.** Under the default mapping, a window's body is computed
per partition and each row gets its own partition's value. An expression that is
elementwise around its windowed parts is therefore the same expression over those
parts, each windowed by the same keys. Polars computes it so.

**It declines, and the window stays as it was:**

- **A body that is one aggregate or ordered function:** the ordinary window. If its
  own operand holds a window, as in `x.Diff(1).CumSum().Over(g)`, one window would
  have to be computed below another, and it is still refused.
- **A window the user wrote inside the body:** its scope is the user's. Still refused
  as a window inside a window.
- **A body with nothing windowed in it,** as in `Col("x").Add(1).Over(g)`:
  `extractWindows` now refuses it as "not a window function" while the query is
  resolved, where only the physical planner did. The physical refusal stays as a
  backstop.
- **A mapping other than the default:** the default is the only one that maps back
  to rows.

**An ordered window over an aggregate is refused by `distribute` itself.** It is
`OrderedAggRefusal`, as `Window.Field` refuses the same window written by hand. A
window built after resolution would first be typed inside an optimizer rule, where
its refusal read as ursus's own failure; a test caught exactly that.

### `Window.Inherits` (`internal/expr/window.go`, `fill.go`)

`FillNull(FillMin/Max/Mean)` builds its window through `inheritedOver`, which marks
it. A marked window takes an enclosing `Over`'s keys; without one, it is the frame's,
as before.

**Why a mark.** A window the user wrote with `.Over()` looks the same, and must keep
its own scope.

**`String()` renders it,** as `over(; inherited)`. Every field that changes how a
window is treated must render, for the dedup maps.

## 3. Tests

**`window_distribute_test.go`:**

- **`TestTheSugarInsideOver`,** over a frame whose groups interleave:
  - `Diff` and `PctChange` per group, and `Diff` in an order given by
    `OverWith`;
  - `x - x.Shift(1)` written out;
  - a demeaned column;
  - a running share, `CumSum / Sum`;
  - an aggregate under a cast.
- **`TestFillNullByAGroupsAggregate`:** `FillMean`, `FillMin` and `FillMax` per group,
  and `FillMean` without `Over`, the frame's.
- **`TestWindowsTheUserScopedStayRefused`,** each refused while the query is resolved:
  - a user's window inside another;
  - a window in an ordered function's operand;
  - a body with nothing windowed;
  - an order over an aggregate.
- **`TestTheSugarInsideOverAgreesWithItsSpelling`:** `Diff(2)` and `PctChange(1)` over
  2,000 rows in seven interleaved groups equal their expansions with each `Shift`
  windowed by hand.

## 4. Teeth

| tooth | result |
| --- | --- |
| no distribution | **bites:** the sugar and fill tests |
| a user's window is inherited too | **bites:** its refusal case |
| the order is not handed down | **bites:** `Diff` in an order |
| an inherited window keeps its own keys | **bites:** the fill test |
| the fill sugar is not marked | **bites:** the fill test |
| a body with nothing windowed is left to the physical planner | **bites:** its refusal case, through Explain |
| an ordered aggregate is built, and refused later | **bites:** its refusal case, as an internal error |
| `inherited` not rendered | silent |

**The last is defensive.** Windows are deduplicated after distribution has given
every inherited window its final keys. An undistributed inherited window computes
what a window of the same keys computes, so no map entry they shared could give a
wrong answer today. The rendering keeps it that way if a later pass deduplicates
earlier.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean.
