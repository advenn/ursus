"""The rollmore group: rolling_skew, rolling_kurtosis, rolling_rank, rolling_cov and
rolling_corr (rollmore_test.go, tier 2 of v0.6.6-scope.md). The moments and the
ranks are compared by their bits; the covariance and correlation within rel:1e-12,
as Polars' rolling means are its own and the cancellation in mean(ab) - mean(a)mean(b)
leaves last bits apart. The skew's x^1.5 is glibc's pow in Polars, which errs by up to
0.52 ulp, so a long or generated skew is held to rel:1e-15.
"""

from __future__ import annotations

import math
from datetime import date

import polars as pl

from . import Case, case
from .gen import Gen

nan, inf, negz = float("nan"), float("inf"), -0.0
x, i, y = pl.col("x"), pl.col("i"), pl.col("y")


def f64(vals: list, name: str = "x") -> pl.DataFrame:
    return pl.DataFrame({name: pl.Series(name, vals, dtype=pl.Float64)})


def typed(vals: list, dtype: pl.DataType, name: str = "x") -> pl.DataFrame:
    return pl.DataFrame({name: pl.Series(name, vals, dtype=dtype)})


def sel(e: pl.Expr):
    return lambda df: df.select(e)


ROLL = pl.DataFrame({
    "x": pl.Series([1.0, 4, 2, 8, None, 3, 3, 9, -1, 5], dtype=pl.Float64),
    "i": pl.Series([1, 4, 2, 8, None, 3, 3, 9, -1, 5], dtype=pl.Int64),
    "y": pl.Series([2.0, 1, 5, 3, 4, None, 6, 2, 0, 7], dtype=pl.Float64),
    "g": pl.Series([1, 1, 1, 1, 1, 2, 2, 2, 2, 2], dtype=pl.Int64),
})
BIG = f64([1e8 + 1, 1e8 + 4, 1e8 + 2, 1e8 + 8, 1e8 + 3, 1e8 + 3, 1e8 + 9])
ALIKE = f64([1.0, 0.1, 0.1, 0.1, 0.1, 0.1, 7.3, 7.3, 7.3, 7.3])
UP = math.nextafter(1e8, math.inf)
ULP = f64([1e8, UP, 1e8, UP, UP, 5.0])
FLAT = f64([2.0, 2, 2, 2, 5])
NANWIN = f64([1.0, nan, 2, 3, 4, 5])
TIES = typed([3, 3, 3, 1, 3, 2, 2], pl.Int64)
CENTRE = f64([5.0, 1, 4, 2, 3, 0])
DUP = f64([1.0, 1, 2, 3, 3, 4, 1])
NANS = f64([1.0, nan, 2, nan, 0.5])
ZEROS = f64([0.0, negz, 0, inf, -inf])
MIXED = pl.DataFrame({"x": pl.Series([1.0, 2, 4], dtype=pl.Float64), "y": pl.Series([3.0, 1, 2], dtype=pl.Float64)})


def long300() -> pl.DataFrame:
    """300 rows of sevenths, a jump of 1e6 every 25 and a null every 37th."""
    vals = []
    for k in range(300):
        v = (k * 7919 % 1000) / 7 + (1e6 if k % 50 < 25 else 0.0)
        vals.append(None if k % 37 == 5 else v)
    return f64(vals)


def cases() -> list[Case]:
    g = Gen(4962)
    gen_moments = g.frame(g.floats(200, lo=-100, hi=100, edges=False))
    gen_ranks = g.frame(g.ints(200, pl.Int64, lo=-20, hi=20), g.floats(200, lo=-5, hi=5, edges=False, name="f"))
    gen_pairs = g.frame(g.floats(200, lo=-100, hi=100, edges=False, name="x"),
                        g.floats(200, lo=-100, hi=100, edges=False, name="y"),
                        g.ints(200, pl.Int64, lo=0, hi=2, null_rate=0, edges=False, name="g"))
    return [
        # --- skew and kurtosis ------------------------------------------------------
        case("skew_4", ROLL, sel(x.rolling_skew(4, bias=True))),
        case("skew_4_unbiased", ROLL, sel(x.rolling_skew(4, bias=False))),
        case("skew_4_min2", ROLL, sel(x.rolling_skew(4, bias=True, min_samples=2))),
        case("kurt_4", ROLL, sel(x.rolling_kurtosis(4, fisher=True, bias=True))),
        case("kurt_4_unbiased", ROLL, sel(x.rolling_kurtosis(4, fisher=True, bias=False))),
        case("kurt_4_pearson", ROLL, sel(x.rolling_kurtosis(4, fisher=False, bias=True))),
        case("kurt_4_min1", ROLL, sel(x.rolling_kurtosis(4, fisher=True, bias=True, min_samples=1))),
        case("skew_3_ints", ROLL, sel(i.rolling_skew(3, bias=True))),
        case("skew_3_center", ROLL, sel(x.rolling_skew(3, bias=True, center=True))),
        case("skew_3_over_g", ROLL, sel(x.rolling_skew(3, bias=True).over("g"))),
        case("skew_3_flat_nan", FLAT, sel(x.rolling_skew(3, bias=True)), note="alike values: NaN"),
        case("kurt_3_flat_nan", FLAT, sel(x.rolling_kurtosis(3, fisher=True, bias=True))),
        case("skew_3_nan_in_window", NANWIN, sel(x.rolling_skew(3, bias=True))),
        case("kurt_3_nan_in_window", NANWIN, sel(x.rolling_kurtosis(3, fisher=True, bias=True))),
        case("skew_4_near_1e8", BIG, sel(x.rolling_skew(4, bias=True)), note="the merged moments' rounding shows, and is Polars'"),
        case("kurt_4_near_1e8", BIG, sel(x.rolling_kurtosis(4, fisher=True, bias=True))),
        case("skew_4_near_1e8_center", BIG, sel(x.rolling_skew(4, bias=True, center=True))),
        case("skew_1_one_row", f64([1.0, 2.0]), sel(x.rolling_skew(1, bias=True))),
        case("skew_3_alike_after", ALIKE, sel(x.rolling_skew(3, bias=True)),
             note="alike after another value: the spread is zero only to the mean's precision, which Polars takes as zero"),
        case("kurt_3_alike_after", ALIKE, sel(x.rolling_kurtosis(3, fisher=True, bias=True))),
        case("skew_2_ulp_apart", ULP, sel(x.rolling_skew(2, bias=True)), note="an ulp apart: NaN as Polars'"),
        case("skew_3_ulp_apart", ULP, sel(x.rolling_skew(3, bias=True))),
        case("kurt_3_ulp_apart", ULP, sel(x.rolling_kurtosis(3, fisher=True, bias=True))),
        case("skew_4_alike_after", ALIKE, sel(x.rolling_skew(4, bias=True))),
        case("moments_300", long300(), lambda df: df.select(
            x.rolling_skew(5, bias=True).alias("sk5"), x.rolling_kurtosis(7, fisher=True, bias=True).alias("ku7"),
            x.rolling_kurtosis(7, fisher=True, bias=False, min_samples=4).alias("ku7u4"),
            x.rolling_skew(9, bias=True, center=True, min_samples=3).alias("sk9c3")), float="rel:1e-15",
             note="glibc's pow for x^1.5 is 0.503 ulp off at row 201 of sk5, pow15's 0.497: one ulp apart"),
        case("skew_3_f32", ROLL, sel(i.cast(pl.Float32).rolling_skew(3, bias=True)), note="a Float32's is a Float32"),
        case("skew_3_bool_refused", typed([True, False, True], pl.Boolean), sel(x.rolling_skew(3, bias=True)),
             contains="not defined"),
        case("moments_gen", gen_moments, lambda df: df.select(
            x.rolling_skew(5, bias=True).alias("sk5"), x.rolling_skew(6, bias=False, min_samples=3).alias("sk6u3"),
            x.rolling_kurtosis(6, fisher=True, bias=True).alias("ku6"),
            x.rolling_kurtosis(7, fisher=False, bias=False, center=True, min_samples=4).alias("ku7pc4")),
             float="rel:1e-15", note="glibc's pow against pow15 for the skew's x^1.5"),
        # --- rank -------------------------------------------------------------------
        case("rank_min_4", ROLL, sel(x.rolling_rank(4, "min"))),
        case("rank_max_4", ROLL, sel(x.rolling_rank(4, "max"))),
        case("rank_dense_4", ROLL, sel(x.rolling_rank(4, "dense"))),
        case("rank_min_3_ints", ROLL, sel(i.rolling_rank(3, "min"))),
        case("rank_min_3_ties", TIES, sel(x.rolling_rank(3, "min"))),
        case("rank_max_3_ties", TIES, sel(x.rolling_rank(3, "max"))),
        case("rank_dense_3_ties", TIES, sel(x.rolling_rank(3, "dense"))),
        case("rank_dense_4_dup", DUP, sel(x.rolling_rank(4, "dense")), note="a tie below the value: dense counts it once"),
        case("rank_min_4_dup", DUP, sel(x.rolling_rank(4, "min")), note="min counts it twice"),
        case("rank_min_4_min1", ROLL, sel(x.rolling_rank(4, "min", min_samples=1))),
        case("rank_min_3_nan", NANS, sel(x.rolling_rank(3, "min")), note="NaN above every number"),
        case("rank_max_3_nan", NANS, sel(x.rolling_rank(3, "max"))),
        case("rank_min_2_zeros", ZEROS, sel(x.rolling_rank(2, "min")), note="-0 with 0"),
        case("rank_max_2_zeros", ZEROS, sel(x.rolling_rank(2, "max"))),
        case("rank_min_2_f32", typed([1.0, 2.0], pl.Float32), sel(x.rolling_rank(2, "min"))),
        case("rank_min_3_center", CENTRE, sel(x.rolling_rank(3, "min", center=True))),
        case("rank_min_4_center_min1", CENTRE, sel(x.rolling_rank(4, "min", center=True, min_samples=1))),
        case("rank_min_3_center_null_after", f64([1.0, 2, None, 4, 0.5]),
             sel(x.rolling_rank(3, "min", center=True, min_samples=1)),
             note="Polars answers null at row 1, whose window's last row is null; ursus's own is 2 (oracleDifferences)"),
        case("rank_avg_4", ROLL, sel(x.rolling_rank(4, "average"))),
        case("rank_avg_3_ints", ROLL, sel(i.rolling_rank(3, "average"))),
        case("rank_avg_3_ties", TIES, sel(x.rolling_rank(3, "average"))),
        case("rank_avg_2_dates", typed([date(2020, 1, 3), date(2020, 1, 1), date(2020, 1, 2)], pl.Date),
             sel(x.rolling_rank(2, "average"))),
        case("rank_avg_2_bools", typed([True, False, True], pl.Boolean), sel(x.rolling_rank(2, "average"))),
        case("rank_avg_2_u64", typed([(1 << 64) - 1, 1 << 63, 5], pl.UInt64), sel(x.rolling_rank(2, "average")),
             note="UInt64 past 2^63"),
        case("rank_avg_4_center", CENTRE, sel(x.rolling_rank(4, "average", center=True))),
        case("rank_min_3_min1_over_g", ROLL, sel(x.rolling_rank(3, "min", min_samples=1).over("g"))),
        case("rank_gen", gen_ranks, lambda df: df.select(
            x.rolling_rank(5, "min").alias("min5"), x.rolling_rank(5, "max").alias("max5"),
            x.rolling_rank(5, "dense").alias("dense5"), x.rolling_rank(5, "average").alias("avg5"),
            x.rolling_rank(6, "min", center=True, min_samples=2).alias("min6c2"),
            pl.col("f").rolling_rank(4, "average", min_samples=1).alias("favg4"))),
        # --- cov and corr -----------------------------------------------------------
        case("cov_4", ROLL, sel(pl.rolling_cov("x", "y", window_size=4)), float="rel:1e-12",
             note="Polars' rolling means are its own: last bits apart"),
        case("corr_4", ROLL, sel(pl.rolling_corr("x", "y", window_size=4)), float="rel:1e-12"),
        case("cov_4_min2_ddof0", ROLL, sel(pl.rolling_cov("x", "y", window_size=4, min_samples=2, ddof=0)),
             float="rel:1e-12"),
        case("cov_3_ints", ROLL, sel(pl.rolling_cov("i", "i", window_size=3)), float="rel:1e-12"),
        case("corr_3_int_y", ROLL, sel(pl.rolling_corr("i", "y", window_size=3)), float="rel:1e-12"),
        case("cov_3_f32", ROLL, sel(pl.rolling_cov(x.cast(pl.Float32), y.cast(pl.Float32), window_size=3)),
             float="rel:1e-6", note="a Float32's covariance is a Float32"),
        case("cov_2_f32_f64", MIXED, sel(pl.rolling_cov(x.cast(pl.Float32), y, window_size=2)), float="rel:1e-12",
             note="a Float32 and a Float64: a Float64"),
        case("corr_3_f32", MIXED, sel(pl.rolling_corr(x.cast(pl.Float32), y.cast(pl.Float32), window_size=3)),
             float="rel:1e-6"),
        case("corr_2_one_side_flat", pl.DataFrame({"x": [1.0, 1.0, 1.0], "y": [3.0, 1.0, 2.0]}),
             sel(pl.rolling_corr("x", "y", window_size=2)), float="rel:1e-12", note="one side alike: NaN"),
        case("cov_3_min2_over_g", ROLL, sel(pl.rolling_cov("x", "y", window_size=3, min_samples=2).over("g")),
             float="rel:1e-12"),
        case("pairs_gen", gen_pairs, lambda df: df.select(
            pl.rolling_cov("x", "y", window_size=6).alias("cov6"),
            pl.rolling_cov("x", "y", window_size=5, min_samples=3, ddof=0).alias("cov5d0"),
            pl.rolling_corr("x", "y", window_size=6).alias("corr6"),
            pl.rolling_corr("x", "y", window_size=8, min_samples=4).over("g").alias("corr8g")),
             float="rel:1e-12", note="Polars' rolling means are its own"),
    ]
