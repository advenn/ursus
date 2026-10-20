# Step 297 — as built

**The user guide's last four chapters** (`docs-site-scope.md` §2):

- missing data;
- Go values in and out;
- Go functions;
- CSV, Parquet and Arrow.

The guide has its twelve chapters now.

## 1. The chapters

**`guide/missing-data.md`:**

- `NullCount`;
- fills by a value, by a statistic and by `Coalesce`;
- fills from the neighbours, with a limit, and `Interpolate`;
- `DropNulls`;
- a frame's `FillNull`, which chooses its columns from the schema;
- how nulls behave in each operation.

**`guide/go-values.md`:**

- a frame from a slice of structs, a field at a time;
- the Go types `Values` takes, in a table;
- a `Series`: `Values`, `Map`, `Fold`, `All`, `Get`;
- the types `Column[T]` takes;
- `Rows[T]` with tags, `ursus:"-"` and pointer fields;
- `CollectBatches` into structs.

**`guide/go-functions.md`:**

- `MapElements`, typed by its function, as a key;
- nulls never reaching `fn`, counted with `sync/atomic`;
- `MapBatches` deciding what a null means;
- an error naming the function, the value and the row;
- the name in `Explain`;
- what the function must do.

**`guide/io.md`:**

- the CSV options in a table;
- a glob and the writer;
- Parquet's projection and row-group predicate in `Explain`;
- `ScanParquetFrom` as object storage;
- Arrow out without a copy and in with one.

**The examples:** 23 more, 128 in all, with `readings` the chapter's data and
`testdata/parts` two CSVs for the glob.

- They passed at `-cpu` 1, 2, 4 and 16, and under the race detector.
- The error example names "row 5 of its batch". That held at every CPU count, since
  the file is one batch.
- The Parquet example writes to memory, not to a path. The first draft wrote under
  `os.TempDir()`: `Explain` printed the path, which differs by system, and parallel
  runs would share the file.

## 2. Held to Polars

**Equal:** the fills by mean, forward, forward with a limit, backward;
`interpolate`; `coalesce`; `null_count`.

**Different, in `feature-gaps.md` §9:**

- **A fill of a whole-number float keeps an integer column's type.** `FillNullWith(2.0)`,
  and a frame's `FillNull(0.0)`, of an Int64 column give Int64, and only a fraction,
  `0.5`, widens it. Polars widens it for any float.
- **How it was found:** a tooth meant to show the frame's `FillNull` passing over
  columns that cannot be null came out silent. Filling a nullable integer column with
  `0.0` changed nothing, a probe found why, and both pages that said "a float fill
  widens" were wrong.

## 3. Found on the way

**A typed view of a `Date` or a `Datetime` is its integer.**

- `df.Column[time.Time]` and `df.At[time.Time]` refuse one, "no typed view for
  time.Time".
- `Column[int32]` gives a Date's days, and `Column[int64]` a Datetime's ticks.
- `Rows[T]` decodes either into a `time.Time`.
- It is in `feature-gaps.md` §1 for 0.6.7's V3, the eager `Series`. The Go values page
  says what each takes.

**A frame's `FillNull` chooses its columns from the schema,** not the data. A probe
first looked as though the output schema depended on whether a column held a null.
It did not: `Values` builds columns the schema says cannot hold one, and those are
left alone.

## 5. The 0.6.6 version

The eight chapters of steps 296 and 297 went to the `docs-0.6.6` branch as step 295's
four did (`a5bf069`).

- **Names:** every name they put in code type exists at v0.6.6. The check found none
  missing, once it counted struct fields and `iota` constants.
- **The examples pass against `git archive v0.6.6`,** but for the two that print
  "Zoë": v0.6.6 pads a cell by bytes, step 296's bug.
  - Their outputs on the branch are v0.6.6's, a column short, refilled from its own
    printer.
  - The strings page says so in a note: 0.6.7 aligns it.
- **Published:** a manual run published 0.6.6 as `latest` from the branch. Both
  versions' guides are live.

## 6. Tests and teeth

**The width test caught three lines** in this step's examples before a page was built:
`TestTheExamplesFitThePage` in the gate, as step 296 meant.

**Teeth:**

- **`MapElements` calling `fn` on a null** bites `Example_udfNulls`: its call count
  goes from 3 to 6.
- **A frame `FillNull` filling a column that cannot hold a null** is silent, an
  equivalence. A whole-number fill of a column with no nulls changes nothing, the
  finding of §2.

**Gate:** test-all 125 ok, race 25 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
