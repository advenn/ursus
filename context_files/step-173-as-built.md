# Step 173 — as built

**F4 of `v0.6-scope.md`, its second half:**

- the mean and median of instants and durations;
- Parquet's UUID and JSON columns;
- the Decimal operators `//`, `%` and `**`, closed with a reason.

## 1. Evidence first: what Polars does

Asked of Polars 1.44.1 in the bench's environment, offline:

| question | Polars' answer |
| --- | --- |
| a Date's mean, of 2024-01-01 and 01-04 | 2024-01-02 12:00, a Datetime(us), the fraction of a day kept |
| a Date's median, of 01-01 and 01-02 | 2024-01-01 12:00, Datetime(us) |
| a Datetime's mean and median | the same type; 1 µs and 2 µs give 1 µs, truncated |
| before 1970, −1 µs and −2 µs | −1 µs, mean and median: toward zero, not floored |
| a Time's mean and median | a Time |
| a Duration's median | a Duration |
| Decimal `//`, `%`, `**` | refused: "floor_div", "remainder" and "pow" are not supported for a Decimal |
| a Parquet UUID, FIXED_LEN_BYTE_ARRAY(16) | Binary, the sixteen bytes |
| a Parquet JSON, BYTE_ARRAY | String, the text |

## 2. What changed

**The mean of an instant** (`expr/agg.go`, `kernel/agg.go`):

- **Datetime and Time** bind as the Duration mean already did: an exact Int128 sum,
  divided once and truncated toward zero, their own type out. A float mean would
  round as Polars' does past 2⁵³; this does not.
- **A Date's mean** is a Datetime(us). Its days become microseconds as they are
  added, which a day's 86,400,000,000 times any int32 of days fits in 128 bits.

**The median of an instant or a duration** (`kernel/aggstat.go`):

- `quantileAcc` gains the answer's type and a scale: a Date's days are scaled to
  microseconds;
- a temporal median's float answer is truncated toward zero into its own type, a
  Date's into Datetime(us);
- `Quantile`, `Var` and `Std` of a temporal column are still refused.

**Parquet:** a JSON column reads as String, and a 16-byte UUID column as Binary,
through the readers that already read STRING and a fixed-length binary.

**The Decimal operators are closed, not built.** Polars refuses `//`, `%` and `**`
for a Decimal, and ursus already refuses them, naming why. Building them would
answer where the reference engine refuses, so the refusal stands.

## 3. Tests

- **`TestTemporalMeanAndMedianAnswerAsPolars`:** nine cases against Polars' answers:
  - a Date's mean, even median, and mean before 1970;
  - a Datetime's mean and median, both truncated, and both before 1970;
  - a Time's mean and median, and a Duration's median, each kept in its type;
  - means per group.
- **`TestTemporalVarStdAndQuantileStillRefuse`.**
- **`TestParquetUUIDAndJSONRead`:** a file written with both logical types reads as
  Binary and String, with a null JSON value null.
- **`TestTemporalStatisticsAreRefusedWithACast`,** an audit test that pinned the
  refusal of a Datetime's median and mean, keeps its quantile and std cases and drops
  those two, pointing here.

## 4. Teeth

| tooth | result |
| --- | --- |
| a Date's mean in days, not microseconds | **bites:** the Date mean cases |
| a Date's median in days | **bites:** the even Date median |
| a temporal median floored, not truncated | **bites:** the median before 1970 |
| JSON refused | **bites:** `TestParquetUUIDAndJSONRead` |
| UUID refused | **bites:** the same |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
