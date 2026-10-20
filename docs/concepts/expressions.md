---
description: >-
  Expressions in ursus, the Go dataframe library: one expression computed per row, per
  group, over the whole frame or over a window, and the namespaces for text, dates,
  lists and structs.
---

# Expressions

An expression says how to compute a column: `Col("qty").Mul(Col("price"))`. It holds
no data, and it is computed only where a query puts it. The same expression gives a
column per row in `Select` and `WithColumns`, one value per group in `Agg`, and keeps
rows in `Filter`. These are Polars' contexts, and ursus follows them.

## The same expression, three ways

--8<-- "Example_exprContexts.md"

- in a `Select` of row-wise expressions, `revenue` is a value per row;
- its `Sum()` is an **aggregate**: in a `Select` it is one value for the frame;
- in `GroupBy(...).Agg`, the same aggregate is one value per group.

`GroupBy()` with no keys also aggregates the whole frame, as one group.

## Windows: an aggregate beside the rows

`Over` computes an expression within each group of rows and gives the answer back on
every row of the group, so the frame keeps its height. A sum over a region beside each
order, and each order's rank within its region:

--8<-- "Example_exprOver.md"

Window functions include the cumulative ones (`CumSum`, `CumMax`, ...), `Shift`,
`Diff`, `Rank`, `RollingMean` and the other rolling windows by rows or by time, and the
exponentially weighted `EwmMean` and its kin.

## Many columns at once

`Col` takes several names, and `All()` and `Exclude(...)` select by what to leave
out. An expression over several columns is one expression per column, and `Suffix`
or `Prefix` names them:

--8<-- "Example_exprMany.md"

## Namespaces

Methods for one kind of column sit behind a namespace:

| Namespace | For | For example |
|---|---|---|
| `Str()` | text | `Contains`, `Replace`, `Split`, `ToUpper`, `Strptime`, `Slice` |
| `Dt()` | dates and times | `Year`, `Weekday`, `Truncate`, `OffsetBy`, `ConvertTimeZone` |
| `List()` | lists | `Len`, `Get`, `Contains`, `Sum`, `Eval`, `Sort`, `Unique` |
| `Struct()` | structs | `Field`, `WithFields`, `RenameFields`, `JsonEncode` |
| `Bin()` | binary | `Contains`, `StartsWith`, `Encode`, `Size` |
| `Cat()` | enums | `GetCategories`, `LenBytes`, `Slice` |

--8<-- "Example_exprNamespaces.md"

## Conditions

`When(cond).Then(a).Otherwise(b)` is a conditional column, and `When` may follow
`Then` for further conditions, the first true one winning:

```go
ursus.When(ursus.Col("qty").Ge(10)).Then("large").
    When(ursus.Col("qty").Ge(5)).Then("medium").
    Otherwise("small")
```

Comparisons give a `Bool` column, combined with `And`, `Or` and `Not` under SQL's
three-valued logic: `null.And(false)` is false, and `null.And(true)` is null.

## Literals

A Go value is a literal wherever an expression goes: `Col("qty").Gt(3)`,
`Then("bulk")`, `FillNullWith(0.0)`. `Lit(v)` makes a literal expression of its own,
and a Go slice is a `List` literal.

**A literal has its Go value's type.** A Go integer constant is an `int`, an `Int64`,
and a float constant a `Float64`, so `Col("a").Add(3)` of an `Int32` column is an
`Int64` column, and `Col("f").Mul(2.5)` of a `Float32` one a `Float64`. Write
`Add(int32(3))` to stay at `Int32`. Polars' literals take the column's type instead.
The fills are the exception: `FillNullWith`, `FillNan` and their kin meet the column,
so `FillNullWith(0)` of an `Int32` column stays `Int32`, and only a fill with a
fraction widens an integer column.

## Names

An expression is named after the leftmost column it reads: `Col("qty").Mul(Col("price"))`
is named `qty`. `Alias` names it, and goes last, since an alias inside an expression
does not name the whole.
