# Step 187 — as built

**The last API break of `v0.6-scope.md`: `DynamicOptions`' `Label`, `StartBy` and
`IncludeBoundaries`,** which were declared only to be refused, each naming itself.
The scope said to build them or remove them. They are built, as Polars'
`group_by_dynamic` answers them.

## 1. Evidence first: what Polars does

Asked of Polars 1.44.1 in the bench's environment:

| option | Polars' answer |
| --- | --- |
| `label="left"` | each window by its start, the default |
| `label="right"` | by its end, start plus period: with a period of 2h every 1h, 02:00 for [00:00, 02:00) |
| `label="datapoint"` | by the instant of its first row |
| `include_boundaries=True` | `_lower_boundary` and `_upper_boundary` before the index |
| `start_by="datapoint"` | the grid starts at the first row, the offset ignored |
| `start_by="wednesday"`, every 1w | the weekday at or before the grid's own start |
| `start_by="friday"`, every 2w | 2023-12-22: Jan 4 truncates by the fortnight to Monday Dec 25, and steps back to the Friday before |
| a weekday with every 1d, 2d, 3d or 5h | 5h from Friday at 20:00, 3d from Friday on a Tuesday: no rule its docs state, which say a weekday takes effect only on weeks |
| a label or start not in its list | refused |

ursus keeps a dynamic group's empty windows, where Polars drops them, so the tests
compare only the windows that hold a row. A `datapoint` label has no row to read in
an empty window, so such a window is labelled by its start.

## 2. What changed

- **`plan.TemporalGroup`** gains `WindowLabel`, `StartBy` and `Boundaries`. Its
  `Label()` method, the Explain line, already took that name.
  - **Typing:** checks the label and the start against their lists, and refuses a
    weekday on a grid not in weeks.
  - **Schema:** the two boundary columns go before the index.
  - **Explain** renders each option that is not the default.
- **`temporalSink`:**
  - `window` gains `end`;
  - `gridStart` begins the grid as `StartBy` says;
  - `Finish` labels each window by its start, end or first row, and writes the
    boundaries;
  - the sink's own schema and the projection above it both carry the two columns.
- **`GroupByDynamic`** passes the three options, lowercased, and
  `refusedDynamicFields` is gone. `TestRefusedDynamicOptionsAreRefused`, which pinned
  the refusal, is replaced by a note pointing to the new tests.

## 3. Tests and teeth

- **`TestDynamicOptionsAnswerAsPolars`**, fourteen cases:
  - each label;
  - boundaries, with a period of 2h every 1h labelled right;
  - starting at the first row, hourly, weekly and daily, with the offset ignored;
  - weeks from Monday, Wednesday and Sunday, and fortnights from Friday;
  - a weekday with an offset.
- **`TestDynamicOptionsRefuseWhatPolarsRefuses`:** an unknown label and start, and a
  weekday on 5h and on 7d. "7d" is seven days, not a week, since step 184.

| tooth | result |
| --- | --- |
| right labelled by its start | **bites:** three cases |
| datapoint labelled by its start | **bites:** the datapoint label |
| the boundaries swapped | **bites:** two cases |
| a datapoint start on the grid | **bites:** four cases |
| a weekday stepped back from the first row | **bites:** the fortnight from Friday |
| a weekday allowed on any grid | **bites:** the refusals |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
