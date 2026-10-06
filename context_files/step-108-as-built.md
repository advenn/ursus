# Step 108 — as built

**A Null-typed column concatenates, writes, and reads back as Null.** This is the
first part of `v0.4-scope.md` item 23, a batch of false refusals: `audit.md` J12 and
I18.

## 1. Measured first

A probe against master reproduced each row item 23 named before any work, because
steps 70–88 had fixed many audit rows since the survey.

**Still open:**

| row | what it does |
| --- | --- |
| J7 | an as-of join on a Float, Int8 or UInt64 key plans, then fails at `Collect` with *"cannot be read as Int64"*; `MergeSorted` refuses Float64, Int8, UInt64 and String keys with a hint that the key "must be numeric or temporal" |
| J9 | a full join with `JoinCoalesce(true)` is refused, with a hint that ursus has no coalesce expression |
| J12 | `Concat` of a frame with a Null column, and any `Collect` that concatenates batches of one: *"concat is not implemented for Null"* |
| I18 | both writers refuse a Null column |
| S15 | the Duration × float hint hard-codes "2.5", whatever the factor was |
| S16 | `FillNan` on a String names `is_not_nan()`, its desugaring, not the method called |

**Already fixed:** S14 (UInt64 with an integer literal in `* // %`) and A9's hint.

This step is J12 and I18. The rest follow.

## 2. What was wrong

A Null column is what a typed null literal makes (`Null(dtype.Null)`), and what an
all-empty CSV column infers as.

- **`concatColumn`** had no case for it.
- **The Parquet and CSV writers** refused it.
- **The reader** mapped Parquet's NULL logical type to its physical type, so the
  column PyArrow writes for its own Null type came back as an Int32 that happened to
  hold no values.

## 3. The fix

- **Concatenating Null parts** gives one Null column of their total length, with no
  payload.
- **The Parquet writer** writes Parquet's NULL logical type on INT32, as PyArrow
  does: every definition level 0, no values.
- **The reader** maps that type to Null. A `nullCol` reads it by stepping over its
  rows with the chunk reader's `Skip` and counting them.
- **The CSV writer** writes the null value in every row.

**Checked outside the suite:** PyArrow reads ursus's file back as `null`. Polars
reads it as an all-null `i32`, which is its own mapping for the logical type.

## 4. Tests and teeth

**`TestANullColumnConcatenates`:** `Concat`, and `Collect` at a batch size of one.

**`TestANullColumnWritesAndReadsBack`:**

- **A Parquet round trip across row groups** of five rows, read in batches of four;
  the columns beside the Null one are unchanged.
- **`testdata/parquet/pyarrow_null_column.parquet`,** a 901-byte file PyArrow 25
  wrote, documented in that directory's README.
- **The CSV text,** exactly.

| tooth | result |
| --- | --- |
| the NULL logical type read as its physical type | **bites:** both Parquet reads. A first mutation failed to build, which does not count. |
| Null columns do not concatenate | **bites** |
| the CSV writer refuses Null again | **bites** |
| the null reader's row count not reset between batches | **bites:** the multi-row-group round trip |

**Gate:** test-all 105 ok, race 21 ok, levels, vet ×3 and the bench engine tests clean; PDS-H SF=0.1 22/22 against DuckDB.
