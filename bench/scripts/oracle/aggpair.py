"""The aggpair group: Corr, Cov, MinBy and MaxBy (aggpair_test.go, step 175). A
correlation or covariance is held to rel:1e-12: Polars' two passes against ursus's
merged one. Polars 2.1's cov of one pair is null, as ursus's is (1.44.1's was 0.0).

MaxBy over a NaN chooses the NaN's row here, as Max does (feature-gaps.md §9; MaxBy's
doc), where Polars skips it: min_by_max_by_nan is in oracleDifferences.

Not here: the brute force over 40,000 rows, the names and the plan, the refusals.
"""

from __future__ import annotations

from datetime import date

import polars as pl

from . import Case, case
from .gen import Gen

nan = float("nan")
c = pl.col
x, y, v, by = c("x"), c("y"), c("v"), c("by")

PAIR = pl.DataFrame({
    "g": ["a", "a", "a", "b", "b", "c"],
    "x": pl.Series([1.0, 2, 4, 1, None, 3], dtype=pl.Float64),
    "y": pl.Series([2.0, 4, 9, 5, 6, 7], dtype=pl.Float64),
})
TOL = dict(float="rel:1e-12", note="Polars' two passes against ursus's merged one: the last bits part")


def sel(*exprs: pl.Expr):
    return lambda df: df.select(*exprs)


def corr_cov(xs: list, ys: list) -> Case:
    return pl.DataFrame({"x": pl.Series(xs, dtype=pl.Float64), "y": pl.Series(ys, dtype=pl.Float64)})


def minmax(vals: list, bys: list, dtype: pl.DataType = pl.Float64) -> pl.DataFrame:
    return pl.DataFrame({"v": pl.Series(vals, dtype=pl.String), "by": pl.Series(bys, dtype=dtype)})


MINMAX = sel(v.min_by("by").alias("lo"), v.max_by("by").alias("hi"))


def cases() -> list[Case]:
    gen = Gen(5003)
    n = 160
    keys = gen.ints(n, pl.Int64, lo=0, hi=4, null_rate=0, edges=False, name="g")
    gen_pairs = gen.frame(keys, gen.floats(n, lo=-100, hi=100, edges=False, name="x"), gen.floats(n, lo=-100, hi=100, edges=False, name="y"))
    gen_by = gen.frame(keys, gen.ints(n, pl.Int64, lo=-1000, hi=1000, name="v"), gen.floats(n, lo=-10, hi=10, edges=False, ties=True, name="by"))
    return [
        # --- corr and cov -----------------------------------------------------------------------
        case("corr_cov_by_g", PAIR, lambda df: df.group_by("g").agg(
            pl.corr("x", "y").alias("corr"), pl.cov("x", "y", ddof=1).alias("cov1"), pl.cov("x", "y", ddof=0).alias("cov0")).sort("g"),
             **TOL),
        case("corr_cov_two_rows", corr_cov([1, 2], [2, 4]), sel(pl.corr("x", "y").alias("corr"), pl.cov("x", "y", ddof=1).alias("cov1")), **TOL),
        case("corr_cov_null_dropped_pairwise", corr_cov([1, None, 3], [1, 5, 2]),
             sel(pl.corr("x", "y").alias("corr"), pl.cov("x", "y", ddof=1).alias("cov1")), **TOL),
        case("corr_cov_nan", corr_cov([1, nan, 3], [1, 2, 3]), sel(pl.corr("x", "y").alias("corr"), pl.cov("x", "y", ddof=1).alias("cov1"))),
        case("corr_cov_every_x_null", corr_cov([None, None], [1, 2]), sel(pl.corr("x", "y").alias("corr"), pl.cov("x", "y", ddof=1).alias("cov1"))),
        case("corr_cov_x_flat", corr_cov([1, 1, 1], [1, 2, 3]), sel(pl.corr("x", "y").alias("corr"), pl.cov("x", "y", ddof=1).alias("cov1"))),
        case("corr_cov_near_equal_large", corr_cov([1e9 + 1, 1e9 + 2, 1e9 + 4], [2, 4, 9]),
             sel(pl.corr("x", "y").alias("corr"), pl.cov("x", "y", ddof=1).alias("cov1")), **TOL),
        case("corr_cov_ints_self", pl.DataFrame({"i": pl.Series([1, 2, 3, 4, 5, 6], dtype=pl.Int64)}),
             sel(pl.corr("i", "i").alias("corr"), pl.cov("i", "i", ddof=1).alias("cov")), **TOL),
        # --- min_by and max_by ------------------------------------------------------------------
        case("min_by_max_by_tie_earliest", minmax(["a", "b", "c", "d"], [1, 1, 3, 3]), MINMAX),
        case("min_by_max_by_null_by_skipped", minmax(["a", "b", "c"], [None, 2, 1]), MINMAX),
        case("min_by_max_by_null_by_last", minmax(["a", "b", "c"], [2, 1, None]), MINMAX),
        case("min_by_max_by_every_by_null", minmax(["a", "b"], [None, None]), MINMAX),
        case("min_by_max_by_null_value", minmax([None, "b", "c"], [0, 2, 1]), MINMAX),
        case("min_by_max_by_nan", minmax(["a", "b", "c"], [1, nan, 3]), MINMAX,
             note="Polars' max_by skips the NaN, c; ursus's NaN is the greatest, b (§9, MaxBy's doc): oracleDifferences"),
        case("min_by_max_by_only_nans", minmax(["a", "b"], [nan, nan]), MINMAX),
        case("min_by_max_by_string", minmax(["a", "b", "c"], ["y", "x", "z"], pl.String), MINMAX),
        case("min_by_max_by_integer", minmax(["a", "b", "c"], [5, 2, 9], pl.Int64), MINMAX),
        case("min_by_max_by_date", minmax(["a", "b", "c"], [date(1970, 1, 6), date(1970, 1, 3), date(1970, 1, 10)], pl.Date), MINMAX),
        case("min_by_max_by_boolean", minmax(["a", "b", "c"], [True, False, True], pl.Boolean), MINMAX),
        case("min_by_max_by_decimal", minmax(["a", "b", "c"], [1.5, -2.25, 9]).with_columns(by.cast(pl.Decimal(10, 2))), MINMAX),
        case("min_by_max_by_by_g", pl.DataFrame({"g": pl.Series([1, 1, 2, 2], dtype=pl.Int64), "v": pl.Series([10, 20, 30, 40], dtype=pl.Int64),
                                                 "by": pl.Series([2.0, 1, None, None], dtype=pl.Float64)}),
             lambda df: df.group_by("g").agg(v.min_by("by").alias("lo"), v.max_by(by * 2.0).alias("hi")).sort("g"),
             note="the value keeps its type; a group of null bys is null"),
        case("min_by_over_g", pl.DataFrame({"g": pl.Series([1, 1, 2, 2], dtype=pl.Int64), "v": pl.Series([10, 20, 30, 40], dtype=pl.Int64),
                                            "by": pl.Series([2.0, 1, None, None], dtype=pl.Float64)}),
             sel(v.min_by("by").over("g"))),
        case("min_by_expanded", pl.DataFrame({"a": [1.0, 2, 4], "b": [2.0, 4, 9], "k": [3.0, 1, 2]}), sel(pl.col("a", "b").min_by("k")),
             note="a selection of several columns expands to one aggregate each"),
        # --- generated ----------------------------------------------------------------------------
        case("corr_cov_gen", gen_pairs, lambda df: df.group_by("g").agg(
            pl.corr("x", "y").alias("corr"), pl.cov("x", "y", ddof=1).alias("cov1"), pl.cov("x", "y", ddof=0).alias("cov0")).sort("g"), **TOL),
        case("min_by_max_by_gen", gen_by, lambda df: df.group_by("g").agg(v.min_by("by").alias("lo"), v.max_by("by").alias("hi")).sort("g"),
             note="ties at a half go to the earliest row"),
        case("min_by_max_by_over_gen", gen_by, sel(v.min_by("by").over("g").alias("lo"), v.max_by("by").over("g").alias("hi"))),
    ]
