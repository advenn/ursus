# Step 98 — as built

**The non-nullable check runs in production.** This is `v0.4-scope.md` item 8, and
`audit.md` I24. The scope said: measure its cost with a micro-benchmark only, then
decide.

## 1. What was open

`data.CheckNonNullable` refuses a batch whose non-nullable field holds a null. It
was off unless a test binary's `init` turned it on. So a null in a column declared
non-nullable was:

- `ErrInternal` in the suite;
- silent in production.

The optimizer acts on the declaration: `boolIdentity` folds `x AND false` to
`false` exactly when `x` is non-nullable. A lie there is a potentially unsound
rewrite. I1's nullability case and I13 both reached the check, and were loud only
because tests ran them.

The stated reason for the toggle was cost. The other four checks are O(columns), and
this one may pay a popcount per column, because a scanned or concatenated column
carries a materialised bitmap even when nothing is null.

## 2. The measurement

**`BenchmarkNonNullableCheck`** (in `internal/data`): `NewBatch` over ten non-nullable
Int64 columns of 8192 rows, each with a materialised all-set bitmap. That is the
case the O(1) `IsAllSet` path misses, and the benchmark refuses to run if the path
is hit. Five runs each:

| | ns per batch |
| --- | --- |
| check off | 101.6–105.9 |
| check on | 854.9–874.7 |

**About 76 ns per non-nullable column per batch,** next to the microseconds any
kernel takes to read the same batch. A column with no stored bitmap costs nothing.
Most columns read from files are nullable, and are not checked at all.

## 3. The decision

**On.** `var CheckNonNullable = true`. It stays a package var so the benchmark can
measure it off.

- `internal/data`'s test init no longer sets it, so
  `TestNonNullableCheckIsOnByDefault` reads the real default. The other packages'
  inits still set it, which is now redundant and harmless.
- `TestNonNullableCheckIsOffByDefault` checked only that the toggle works when off.
  It is renamed `TestNonNullableCheckCanBeTurnedOff`.
- `CheckTimeRange`, the sixth check, stays test-only. It is O(rows) per Time column,
  not a popcount, and is not this item.

**Found on the way:** `NewBatch`'s doc comment ran straight into
`CheckNonNullable`'s with no blank line, so godoc attached it to the variable and
`NewBatch` was undocumented. It is moved onto the function.

## 4. Teeth

| tooth | result |
| --- | --- |
| off by default again | **bites:** `TestNonNullableCheckIsOnByDefault`, and the two tests that relied on the init |
| `NewBatch` skips the check | **bites** |

**Gate:** test-all 105 ok, race 21 ok, levels, vet ×3 and the bench engine tests clean; PDS-H SF=0.1 22/22 against DuckDB. Run once over steps 98 and 99 together.
