# Step 318 — as built

**V2, rows out:** Polars' `rows_by_key(named=True)`, `to_dicts` and
`iter_rows(named=True)` (`v0.6.7-scope.md` §6).

## 1. The API

```go
func (df *DataFrame) RowsByKey[K comparable, T any](key string) (map[K][]T, error)
func (df *DataFrame) ToMaps() ([]map[string]any, error)
func (lf *LazyFrame) IterRows[T any](ctx context.Context, opts ...CollectOption) iter.Seq2[T, error]
```

**`RowsByKey`:**

- the rows decoded into `T`, as `Rows` decodes them, grouped by the key column's
  value, a key's rows in the frame's order;
- the key is read into `K` by the same setter a field of `K`'s type would use, so an
  Int64 key reads into an `int` or an `int64`;
- a null key is K's zero, as every setter reads a null.

Polars' `include_key` is `T`'s fields here, and its `unique=True` is not given:
`RowsByKey` always groups.

**`ToMaps`:**

- each row a `map[string]any`;
- an integer or a float its own Go type, text and an Enum a string, a Binary a
  `[]byte`, a Boolean a bool;
- a Date or a Datetime a `time.Time` in its zone, a Duration a `time.Duration`, and a
  Time the `time.Duration` since midnight;
- a Decimal its text, through the cast;
- a List a `[]any` and a Struct a `map[string]any`, each read through its own;
- a null nil.

Each column's reader is chosen once, so a row costs a map and a call a column.

**`IterRows`:**

- `CollectBatches`, each batch decoded into `T`, the rows yielded;
- a break stops the query;
- an error is yielded once, with `T`'s zero, and ends the walk.

## 2. Speed

100,000 rows of an int64, a float64 and a string, medians of seven:

| | ursus | Polars |
| --- | --- | --- |
| `ToMaps` / `to_dicts` | 30 ms | 72 ms |
| `IterRows` / `iter_rows(named=True)` | 5 ms | 46 ms |
| `RowsByKey` / `rows_by_key(named=True)` | 11 ms | 85 ms |

## 3. Tests

**`TestRowsOut`:**

- `RowsByKey` by a String key with a null, by an Int64 key read as an `int`, an unknown
  key refused, a float key read as a time refused, and a null key whose slot holds 7
  keyed as 0;
- `ToMaps` of the frame, nil for the null; and of a List, a Struct, a time, a Duration
  in ns and one in µs;
- `IterRows` at batches of one; a break after two rows; an unknown column's error
  yielded once.

## 4. Docs

- **Go values:** a paragraph on the three.
- **Coming from Polars:** a row.
- **The CHANGELOG:** the features.
- **`feature-gaps.md`:** the Go values row, closed.

## 5. Teeth, all 8 biting

- a null a value in `ToMaps`;
- a break ignored;
- ticks read as nanoseconds;
- a list's first element each time;
- a struct's fields misnamed;
- `RowsByKey`'s rows out of order;
- a null key read from its slot, which a setter reading the raw values instead of the
  typed view's shows.

**Two teeth were silent at first:**

- **The null key.** `RowsByKey`'s own check of a null key was redundant: every setter
  reads a null as its zero through the typed view. The check is gone, and the tooth
  moved to the setter.
- **The list's elements.** The test's list repeated one value, `[2, 2]`, so "the first
  element each time" answered it. The list is `[2, 20]` now.

Three first failed the build, each leaving a variable unused, and were rewritten.

**Gate:** test-all 125 ok, race 25 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB. The root package took 680 s under the race
detector here, and 760 s of CI's 20-minute limit at step 317.
