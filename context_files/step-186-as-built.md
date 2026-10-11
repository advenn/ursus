# Step 186 — as built

**The API breaks of `v0.6-scope.md`: internal types in the public API.** The midpoint
audit's §4.4 named them, and ursus stays 0.x so that APIs can break when that helps.

## 1. What was wrong

These took or returned a type from an internal package. A caller outside the module
cannot import that package, so cannot name the type, build a value of it, or
implement it:

- **`Scan(plan.Source)`:** `plan.Source` is an internal interface.
- **`FromPlan(plan.Node)` and `LazyFrame.Plan()`:** the logical plan. `FromPlan` was
  kept for "a future SQL frontend", and SQL is out by decision.
- **`DataFrame.Batch() *data.Batch`,** kept alive by `var _ = data.Batch{}`.
- **`WithOptFlags(plan.Flags)`:** no outside caller could build its argument, so the
  option was unusable outside the module.
- **`DataFrame.Column[T]`** returned `*data.Series[T]`, where `Series[T]` is a public
  alias of the same type.

Option types such as `WindowOption`, a function over an internal parameter, are a
different case. A caller only ever builds one through ursus's constructors, which is
what an option type is for, so they stay.

## 2. What changed

- **`Scan` becomes `scan`.** Its eight callers in the package are renamed, and the
  `LazyFrame` doc's example uses `ScanParquet`.
- **`FromPlan`, `Plan()` and `Batch()` are removed**, with the `data.Batch{}`
  keep-alive.
- **`export_test.go`** gives the module's root tests `Scan`, `FromPlan`, `Plan` and
  `Batch`, beside the hooks it already held. The first version of this step
  overwrote that file, losing `SetDefaultMemoryLimit` and the rest. The compiler
  said so, and the file was rebuilt from HEAD with the new functions added.
- **`OptFlags`** is a public struct with `plan.Flags`' nine fields, plus
  `DefaultOptFlags()` and `NoOptFlags()`, and `WithOptFlags` takes it. It is
  converted with `plan.Flags(f)` and `OptFlags(plan.DefaultFlags())`, conversions
  that compile only while the two structs' fields are identical, so the compiler
  keeps them in step.
- **`DataFrame.ColumnAt(i) *Column`** gives a column by position, through the public
  alias. `ursustest`, a public package of the module, used `Batch()` and now uses
  `ColumnAt`, `Height` and `Width`, as `internal/memcheck` does.
- **`DataFrame.Column[T]`** returns `*Series[T]`, the same type by its public name.
- **Tests:** 19 root test files moved from `plan.Flags` to `OptFlags`, and the
  `plan` imports they no longer needed went. `inventory_test.go` keeps `plan.Flags`,
  since it drives the internal optimizer itself. `ursustest`'s nested test builds its
  frame with `Frame`.

## 3. The guard

**`TestThePublicAPINamesNoInternalType`** (`apisurface_test.go`):

- it parses the package's source, in milliseconds. Type-checking it through
  `go/importer` took 7.7 s, its dependencies from source;
- for every exported function, method on an exported type, and exported struct
  field, it refuses a `pkg.Type` whose `pkg` is an internal package's import;
- an alias is ursus's own name and passes, as does an option type defined over an
  internal one;
- its first run found `Column[T]`'s `*data.Series[T]`.

| tooth | result |
| --- | --- |
| a batch exported again (`RawBatch() *data.Batch`) | **bites** |
| `plan.Flags` taken again (`WithPlanFlags(plan.Flags)`) | **bites** |

The CHANGELOG has its first Breaks section, each break with its replacement.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
