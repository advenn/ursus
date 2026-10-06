# Step 96 — as built

**A rolling or dynamic group-by's expansion is bounded, and what it must hold is
charged to the budget.** This is `v0.4-scope.md` item 6.

## 1. What was wrong

Rolling and GroupByDynamic aggregate overlapping windows. A row belongs to every
window that reaches it, so `temporalSink.Finish` expanded them. It built:

1. one (window, row) pair per membership;
2. then a copy of every input column, gathered by those pairs;
3. then each aggregate's input, evaluated over the copy.

All of it was built at once, and none of it was charged. The budget saw the input
and the window grid. A 4,000-row rolling window over 4,000 rows is 8 million pairs.

**Measured:**

- **A rolling sum over those 4,000 rows grew the live heap by 244 MB**
  (`TestRollingExpansionIsBounded`).
- **Each of the following held 8 million values under a 16 MB `WithMemoryLimit`,
  with no error** (`TestRollingStateIsCharged`):
  - a rolling median;
  - a rolling implode;
  - a dynamic median whose period was far past its `every`.

Under a container's limit that is a kill, not a refusal: the exposure step 90
removed everywhere else.

## 2. The fix

- **Pairs are built and fed a chunk at a time,** one batch's worth: pairs for a
  chunk, the gathered copy, the aggregates fed, repeat. A window that straddles
  chunks is fed in order across `AddBatch` calls, so first, last and implode keep
  their order. The expansion is bounded by the batch size, not by how much the
  windows overlap.
- **What the aggregates keep is charged as it grows** (`chargeState`), together
  with the two per-window arrays `Finish` builds, each window's start and its
  bucket. Then the account is checked.
  - **A sum keeps** one number per window.
  - **A median or an implode keeps** one value per pair, and is now refused under
    the budget as it grows, naming `rolling` or `group_by_dynamic`.
- **`NBytes` is O(windows),** so it is read once per `max(chunk, windows)` pairs:
  O(1) per pair, with the charge at most about one pair per window behind.
- **It is faster too.** A 4,000-row rolling sum and mean (8M pairs) went from
  235–279 ms to 174–186 ms, and from 581 MB allocated per query to 277 MB. The
  chunk stays in cache, where the whole expansion did not. This is a
  micro-benchmark of four runs each, old file against new, not a suite.

## 3. One test recalibrated

**`TestGridAccountingIsQueryWide`** set a 600 KiB limit, under which one bucket's
grid fits (about 346 KiB) and three do not. One bucket now charges about 700 KiB:
the grid plus the per-window state that was always allocated and is now counted.
So the limit is 1 MiB.

The property is unchanged: one bucket fits, and three do not.

## 4. Tests and teeth

**`TestRollingExpansionIsBounded`:** the 4,000-row rolling sum grows the live heap by
under 48 MB (244 MB before).

The heap is sampled through `runtime/metrics` every millisecond, with
`SetGCPercent(10)` for the measurement, so what is counted is held rather than
not-yet-collected.

**`TestRollingStateIsCharged`:**

- **The three holistic cases are refused** under 16 MB, as `ErrResource` naming the
  operator.
- **Each is refused before the heap grows past 48 MB.** The whole state is 64 MB of
  values, so a charge made only once everything is built would fail this.
- **A control:** a sum under the same limit runs.

**`TestRollingAnswersDoNotDependOnTheChunk`:**

- **Three shapes:**
  - a rolling window;
  - a rolling window by key;
  - an overlapping dynamic window.
- **Nine aggregates,** order-dependent ones included.
- **Batch sizes 1, 7, 64 and 1000,** each compared with one batch of 2^20.

**`TestDynamicWindowStateIsCharged`:** 14,400 windows from two rows; the budget's
peak covers grid, start, bucket and count for each.

| tooth | result |
| --- | --- |
| the expansion built whole | **bites:** the bound, and the three refusals' heap growth |
| the state never charged | **bites:** the three refusals |
| the state charged only at the end | **bites:** the three refusals, on heap growth |
| a chunk drops its first pair | **bites:** the chunk-independence cases |
| the per-window arrays not charged | silent at first; **bites** `TestDynamicWindowStateIsCharged`, written for it |
| the aggregates' state left out of the charge | **bites:** `TestDynamicWindowStateIsCharged` |

**Gate:** test-all 105 ok, race 21 ok, levels, vet ×3 and the bench engine tests clean; PDS-H SF=0.1 22/22 against DuckDB.
