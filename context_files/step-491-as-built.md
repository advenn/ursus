# Step 491 — as built

**`Name().StripPrefix` and `StripSuffix`, Polars' `name.strip_prefix` and
`strip_suffix`.** The fourth of the five features step 487 chose against Polars 2.1.
`Name().Map` spelled them, in a closure.

## 1. The change

- **`NameExpr.StripPrefix(p string) Expr` and `NameExpr.StripSuffix(s string) Expr`:**
  each an `expr.Rename` of the output name, as `ToLower` and `Replace` are, by
  `strings.TrimPrefix` and `TrimSuffix`: the affix taken off once, where the name has
  it, compared as bytes, as Polars compares it. A plan shows
  `name.strip_prefix("a_")`.
- Nothing new below the root package: a rename is planned as every other is, so an
  alias before the strip is what it strips and one after it wins, a group-by's key
  and aggregate are renamed alike, and two outputs of one name are refused.

## 2. Polars 2.1.0's answers

Over columns `a_x`, `a_`, `b_x`, `a_a_y`, `xa_`:

| expression | names |
| --- | --- |
| `all().name.strip_suffix("_x")` | `a`, `a_`, `b`, `a_a_y`, `xa_` |
| `all().name.strip_prefix("")`, `strip_suffix("")` | unchanged |
| `col("a_").name.strip_prefix("a_")`, `strip_suffix("a_")` | `""` |
| `(col("b_x") * 2).alias("a_q").name.strip_prefix("a_")` | `q` |
| `col("a_x").name.strip_prefix("a_").alias("z")` | `z` |
| `col("a_a_y")`, stripped of `a_` twice | `y` |
| stripped of `a_`, then `name.prefix("p_")` | `p_x` |
| `name.prefix("a_")`, then stripped of `a_` | `a_x` |
| `col("a_x").sum().name.strip_prefix("a_")` | `x` |
| `ä_x`, `äx` stripped of `ä` | `_x`, `x` |
| `group_by(col("a_x").name.strip_prefix("a_")).agg(col("b_x").name.strip_suffix("_x"))` | `x`, `b` |
| `a_x` and `b_x` both stripped to `x` | refused: "projections contained duplicate output name 'x'" |

ursus answers each as Polars does, `""` included.

## 3. Tests

`name_strip_test.go`, `TestNameStrip`: the table above, and the oracle: over 200
generated names of five runes that share bytes (`a`, `_`, `b`, `ä`, and `ä` with a
combining acute) and 40 generated affixes, `StripPrefix` undoes `Prefix` and
`StripSuffix` undoes `Suffix` for every name, and each leaves a name without the
affix as it is.

## 4. Teeth: 3 of 3 bite

| mutation | `TestNameStrip` |
| --- | --- |
| `StripPrefix` takes every occurrence, `strings.ReplaceAll` | **fails:** a prefix, then stripped, is `x`, Polars `a_x` |
| `StripPrefix` takes the prefix while the name begins with it | **fails:** the same row, `x` |
| `StripSuffix` takes the affix off the start | **fails:** `b_x` stripped of `_x` stays `b_x`, Polars `b` |

**Gate:** `./internal/...`, `./dtype/...`, `./i128/...` and the root package in full,
and vet.
