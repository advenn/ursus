"""The aggwindow_byhand group: audit.md §7's aggregation and window cases
(aggwindow_byhand_test.go): ShiftFill's fill meeting the column at a common type, a
quantile at an exact rank, PctChange in float, a variance that overflows to +Inf, Sum
and CumSum of a Boolean, Product's +0, and a WithColumns whose later windows read its
earlier columns, which Polars spells as chained with_columns.

Not here: A4, an integer mean past 2^53, where Polars sums in float and rounds
(9007199254740992.0 of [2^53+1, 2^53+2]) and ursus divides the exact Int128 sum; the Go
test holds it to math/big. Spelled around: an integer Sum is an Int128 here, so a
Boolean's sum and cum_sum are cast to Int64 on both sides and the chained windows'
sums on ursus's; Product is a Float64 here (feature-gaps.md §9), so Polars' is cast.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

inf = float("inf")
c = pl.col
v, x, g, w = c("v"), c("x"), c("g"), c("w")


def typed(vals: list, dtype: pl.DataType, name: str = "v") -> pl.DataFrame:
    return pl.DataFrame({name: pl.Series(name, vals, dtype=dtype)})


def f64(*vals: float) -> pl.DataFrame:
    return typed(list(vals), pl.Float64)


def sel(*exprs: pl.Expr):
    return lambda df: df.select(*exprs)


GX = pl.DataFrame({"g": ["a", "a", "b"], "x": pl.Series([1, 2, 3], dtype=pl.Int64)})
GWX = pl.DataFrame({"g": ["a", "a", "b"], "w": pl.Series([0, 0, 0], dtype=pl.Int64), "x": pl.Series([1, 2, 3], dtype=pl.Int64)})
BOOLS = typed([True, False, True, None], pl.Boolean)
GBOOLS = pl.DataFrame({"g": pl.Series([1, 2, 1, 2], dtype=pl.Int64), "v": [True, True, True, False]})
ZERO = typed([3, 0, -5], pl.Int64)


def cases() -> list[Case]:
    gen = Gen(5001)
    n = 150
    gen_shift = gen.frame(gen.ints(n, pl.Int64, lo=-100, hi=100, name="v"), gen.ints(n, pl.UInt8, name="u"))
    gen_groups = gen.frame(gen.ints(n, pl.Int64, lo=0, hi=3, null_rate=0, edges=False, name="g"),
                           gen.floats(n, lo=-50, hi=50, edges=False, name="v"))
    gen_bools = gen.frame(gen.ints(n, pl.Int64, lo=0, hi=3, null_rate=0, edges=False, name="g"), gen.bools(n, name="v"))
    gen_chain = gen.frame(gen.ints(n, pl.Int64, lo=0, hi=4, null_rate=0, edges=False, name="g"),
                          gen.ints(n, pl.Int64, lo=-30, hi=30, name="x"))
    return [
        # --- A1: ShiftFill's fill meets the column at a common type -------------------
        case("shift_fill_float", typed([1, 2, 3], pl.Int64), sel(v.shift(1, fill_value=1.5)), note="an Int64 column, a float fill: Float64"),
        case("shift_fill_int32", typed([1, 2, 3], pl.Int32), sel(v.shift(1, fill_value=0)), note="an Int32 stays one"),
        case("shift_fill_int64", typed([1, 2, 3], pl.Int64), sel(v.shift(1, fill_value=pl.lit(-1, dtype=pl.Int64)))),
        # --- A2: a quantile at an exact rank is that value; nothing overflows ------------
        case("quantile_0_neg_inf", f64(-inf, 1, 2), sel(v.quantile(0, "linear"))),
        case("quantile_1_inf", f64(1, 2, inf), sel(v.quantile(1, "linear"))),
        case("median_extremes", f64(-1.7e308, 1.7e308), sel(v.median()),
             note="Polars 2.1 overflows to +Inf, lo + (hi - lo)/2; 0 here, held to big.Float in the Go test: a finding, skipped"),
        case("midpoint_huge", f64(1e308, 1.5e308), sel(v.quantile(0.5, "midpoint")),
             note="Polars 2.1 overflows to +Inf, (lo + hi)/2; 1.25e308 here, held to big.Float: a finding, skipped"),
        case("midpoint_alike_huge", f64(1.7e308, 1.7e308), sel(v.quantile(0.5, "midpoint"))),
        case("median_alike_huge", f64(1.7e308, 1.7e308), sel(v.median())),
        # --- A3: PctChange computes in float, not at the column's width -----------------
        case("pct_change_u8", typed([3, 1, 255, 0], pl.UInt8), sel(v.pct_change(1))),
        case("pct_change_i8", typed([100, -100, 50], pl.Int8), sel(v.pct_change(1))),
        # --- A5: a variance that overflows is +Inf, never negative ----------------------
        case("var_overflow", f64(1e308, -1e308), sel(v.var(ddof=0))),
        case("std_overflow", f64(1e308, -1e308), sel(v.std(ddof=0))),
        case("var_alike_huge", f64(1e308, 1e308), sel(v.var(ddof=0)),
             note="Polars 2.1's sum of squares overflows and its variance is NaN; 0 here, held to big.Float: a finding, skipped"),
        # --- A10: Sum of a Bool counts its trues ---------------------------------------
        case("bool_sum", BOOLS, sel(v.sum().cast(pl.Int64)), note="an integer Sum is an Int128 here, Polars' a UInt32: both cast to Int64"),
        case("bool_sum_by_g", GBOOLS, lambda df: df.group_by("g").agg(v.sum().cast(pl.Int64)).sort("g"),
             note="both sides cast to Int64"),
        case("bool_cum_sum", typed([True, None, True, False], pl.Boolean), sel(v.cum_sum().cast(pl.Int64)),
             note="both sides cast to Int64"),
        # --- A13: Product's zero is +0 ---------------------------------------------------
        case("product_zero", ZERO, sel(v.product().cast(pl.Float64)),
             note="Product is a Float64 here (§9), so Polars' Int64 is cast; the sign bit is the question"),
        case("cum_prod_zero", ZERO, sel(v.cum_prod().cast(pl.Float64)), note="Polars' Int64 cast; +0 after the zero"),
        # --- W1: a window reads the columns defined before it in its WithColumns ----------
        case("window_over_window", GX, lambda df: df.with_columns(x.cum_sum().over("g").alias("a"))
             .with_columns(c("a").max().over("g").alias("m")),
             note="one WithColumns here; Polars reads a column of the same with_columns nowhere, so two"),
        case("replaced_column_place", GWX, lambda df: df.with_columns((x * 10).alias("w"))
             .with_columns(w.sum().over("g").alias("s"))),
        case("two_splits", GX, lambda df: df.with_columns((x * 10).alias("w")).with_columns(w.sum().over("g").alias("s"))
             .with_columns((c("s") + w).alias("t")).with_columns(c("t").max().over("g").alias("m"))
             .with_columns((c("m") - c("t")).alias("d"))),
        case("earlier_column_outside", GX, lambda df: df.with_columns((x * 10).alias("w"))
             .with_columns((w + x.sum().over("g")).alias("s"))),
        # --- generated ---------------------------------------------------------------------
        case("shift_pct_gen", gen_shift, sel(v.shift(1, fill_value=1.5).alias("f15"), v.shift(2, fill_value=pl.lit(-1, dtype=pl.Int64)).alias("m1"),
                                             v.shift(-1, fill_value=0).alias("back"), v.pct_change(1).alias("pct"),
                                             c("u").pct_change(1).alias("upct"), c("u").shift(1, fill_value=7).alias("u7"))),
        case("quantile_var_gen", gen_groups, lambda df: df.group_by("g").agg(
            v.quantile(0, "linear").alias("q0"), v.quantile(1, "linear").alias("q1"), v.median().alias("med"),
            v.quantile(0.5, "midpoint").alias("mid"), v.quantile(0.3, "nearest").alias("q3n"),
            v.var(ddof=0).alias("var0"), v.std(ddof=0).alias("std0"), v.var(ddof=1).alias("var1")).sort("g"),
             float="rel:1e-12", note="a variance's merge order (feature-gaps.md §1)"),
        case("bool_sum_gen", gen_bools, lambda df: df.with_columns(v.cum_sum().cast(pl.Int64).alias("cs"))
             .group_by("g").agg(v.sum().cast(pl.Int64).alias("s"), c("cs").last().alias("cs_last")).sort("g"),
             note="both sides cast to Int64"),
        case("window_chain_gen", gen_chain, lambda df: df.with_columns(x.cum_sum().over("g").alias("a"))
             .with_columns(c("a").max().over("g").alias("m"), (x * 10).alias("w"))
             .with_columns((c("w") + x.sum().over("g")).alias("s")),
             note="ursus's Int128 sums cast to Int64 in the spelling"),
    ]
