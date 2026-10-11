# Step 179 — as built

**R2 of `v0.6-scope.md`, its next three rows:** O13, I23 and O12 of `audit.md`.

## 1. O13: types that render alike. Measured reachable, and fixed

**Evidence.** Step 86 measured the plainest shape, two typed nulls, and each kept its
type. This step looked past nulls. `DataType.String` joined an Enum's categories with
", ", so `Enum("a, b")` and `Enum("a", "b")` both rendered as `Enum(a, b)`. A Cast
renders its target type, and aggregates and window functions are deduplicated on
their renderings. So over a column `s` = a, b:

| two expressions, one cast to each Enum | before |
| --- | --- |
| in one `GroupBy().Agg`, each with `First` | failed with `ErrInternal`: batch column "y" is Enum(a, b), schema says Enum(a, b) |
| in one `Select`, each `First().Over(...)` | y answered ∅ ∅, the first's answer; it is a a |
| in one `Select`, each `ForwardFill` | y answered ∅ ∅; it is a b |

A plain `Select` of the two casts was right, because the projection does not
deduplicate.

**The fix** is at the source. `DataType.String` and `Field.String` now quote, through
`dtype.QuoteName`, any category or field name that would make the rendering
ambiguous: one that is empty, has a space at either end, or holds `,` `:` `(` `)`
`"` `\` `!` or anything unprintable. Every ordinary name renders as before, so
`Enum(low, high, N/A, two words)` is unchanged. Since every deduplication key embeds
these renderings, this one change fixes each of them.

## 2. I23: statistics from an unknown writer. Fixed

**Evidence**, from arrow-go v18.7.0's source:

- `NewInt64StatisticsFromEncoded` and its siblings start from statistics whose
  `hasNullCount` is already true, and add nulls only when the file sets the field.
- So a missing `null_count` reads as zero, and `IsNull` pruning would skip a group
  holding nulls.
- `hasMinMax` is `IsSetMax() || IsSetMin()`, so a one-sided bound reads its other
  side as 0.
- The thrift metadata that knows the difference is internal to arrow-go.

Every writer the bench's environment can run writes both: Polars (`created_by`
"Polars"), pyarrow ("parquet-cpp-arrow version 25.0.1") and DuckDB ("DuckDB version
v1.5.5"), whose files are PDS-H's data. Each was read back with pyarrow.

**The fix** keys trust on the footer's `created_by`, as arrow-go already does for
writers with known statistics bugs (`HasCorrectStatistics`):

- `knownWriter` takes a list of prefixes: parquet-cpp (with -arrow), parquet-go
  (arrow-go, so ursus's own files), parquet-mr, parquet-rs, DuckDB and Polars;
- `fileLayout.fullStats` records it per file, and the pruner receives it;
- from any other writer, `IsNull` is never pruned on a null count, and an integer,
  unsigned or float range is never pruned on its bounds;
- strings' bounds still prune, since an absent bound there is already "" and no
  bound;
- the all-null shortcut is kept, because a missing count reads as 0 and so can never
  claim all-null.

## 3. O12: the optimizer and errors. Decided and written

**Evidence.** `rule_simplify.go`'s `boolIdentity` folded `x OR true` to true and
`x AND false` to false whatever x was. A strict cast in x that fails therefore failed
with the optimizer off and answered with it on.

Polars 1.44.1 raises for `(cast > 0) | True` in a `select`, optimized or not, and
for `& False` too. In a `filter` it returns every row. It is inconsistent, so the
choice follows ursus's own contract.

**The decision,** now written on `plan.Rule`:

- a rule never adds an error;
- a rule never removes an error from a value the answer reads;
- whether a value nothing reads is computed is not promised.

**The change:** `boolIdentity` folds those two identities only for an x that
`fallible` clears. `x AND true` and `x OR false` keep x, and its errors with it.

**Found:** projection pushdown never removes an unread computed column. A `Select`
whose output a later `Select` drops is still computed, and a failure in it fails the
query, where Polars drops it. It is recorded here and in the contract's text as what
happens today, and left as an optimization for later.

## 4. Tests and teeth

- **`TestTypesThatRenderedAlikeStayApart`:** the three shapes of §1.
- **`TestTypeRenderingQuotesOnlyWhatItMust`:** ordinary names bare, odd ones quoted,
  including inside a List.
- **`TestPruningTrustsOnlyAKnownWritersStatistics`** (`writerstats_test.go`):
  - four row groups, written with arrow-go under six `created_by` strings;
  - `IsNull`, a number's range and a string's range;
  - the same rows every time, and the groups skipped counted.
- **`TestAFoldedOperandsErrorIsKept`:**
  - `OR true`, `AND false` and `true OR` over a failing cast raise the cast's error,
    not an internal one;
  - an infallible operand is still folded away.

| tooth | result |
| --- | --- |
| an Enum's categories bare again | **bites:** both rendering tests |
| a field's name bare again | **bites:** the rendering test |
| `x OR true` folded whatever x | **bites:** `TestAFoldedOperandsErrorIsKept` |
| every writer known | **bites:** `IsNull` and the number range, other writers |
| another writer's null count trusted | **bites:** `IsNull`, other writers |
| another writer's number bounds trusted | **bites:** the number range, other writers |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
