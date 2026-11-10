"""The rollingquantile group: rolling median and quantile by rows and by time
(rollingquantile_test.go, step 227): a null in the window, a NaN that sorts as the
largest value, -0, min_samples, a centred window, nearest and linear interpolation, an
integer window, and windows by time where a null row still answers from its window.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

nan, negz = float("nan"), -0.0
c = pl.col
v, i = c("v"), c("i")
MIN_US = 60_000_000
T0 = 19723 * 86_400_000_000

RQ = pl.DataFrame({"v": pl.Series([3.0, None, 1, 4, nan, 2, 2, negz, 9, 5], dtype=pl.Float64)})
INTS = pl.DataFrame({"i": pl.Series([5, 1, None, 3, 3, 8], dtype=pl.Int32)})
BYT = pl.DataFrame({"t": pl.Series([T0 + m * MIN_US for m in (0, 1, 1, 3, 7, 8)], dtype=pl.Int64).cast(pl.Datetime("us")),
                    "v": pl.Series([4.0, 1, None, 9, 2, 6], dtype=pl.Float64)})


def sel(*exprs: pl.Expr):
    return lambda df: df.select(*exprs)


def cases() -> list[Case]:
    gen = Gen(5015)
    n = 160
    gen_v = gen.frame(gen.floats(n, lo=-50, hi=50, null_rate=0.15, edges=True, name="v"))
    mins = sorted(int(m) for m in gen.rng.integers(0, 300, n))
    gen_by = pl.DataFrame({"t": pl.Series([T0 + m * MIN_US for m in mins], dtype=pl.Int64).cast(pl.Datetime("us")),
                           "v": gen.floats(n, lo=-50, hi=50, null_rate=0.15, edges=False, name="v")})
    return [
        case("rows", RQ, sel(v.rolling_median(3).alias("m3"), v.rolling_median(3, min_samples=1).alias("m3_1"),
                             v.rolling_quantile(0.25, "nearest", 4, min_samples=2).alias("q25n"),
                             v.rolling_quantile(0.25, "linear", 4, min_samples=2).alias("q25l"),
                             v.rolling_median(4, min_samples=1, center=True).alias("mc"))),
        case("int32_window", INTS, sel(i.rolling_median(2, min_samples=1).alias("m"), i.rolling_quantile(0.5, "nearest", 2, min_samples=1).alias("q")),
             note="an Int32 window answers a Float64"),
        case("by_time_3m", BYT, sel(v.rolling_median_by("t", "3m").alias("m"), v.rolling_quantile_by("t", "3m", quantile=0.75, interpolation="higher").alias("q")),
             note="a null row still answers from its window"),
        # --- generated -----------------------------------------------------------------------------
        case("rows_gen", gen_v, sel(v.rolling_median(5).alias("m5"), v.rolling_median(5, min_samples=1).alias("m5_1"),
                                    v.rolling_quantile(0.25, "nearest", 6, min_samples=2).alias("q25n"),
                                    v.rolling_quantile(0.25, "linear", 6, min_samples=2).alias("q25l"),
                                    v.rolling_quantile(0.9, "lower", 4).alias("q9lo"), v.rolling_quantile(0.9, "higher", 4).alias("q9hi"),
                                    v.rolling_quantile(0.3, "midpoint", 7, min_samples=3, center=True).alias("q3mc"),
                                    v.rolling_median(6, min_samples=1, center=True).alias("mc")),
             float="rel:1e-12", note="a linear interpolation's formula order: an ulp apart"),
        case("by_time_gen", gen_by, sel(v.rolling_median_by("t", "10m").alias("m10"), v.rolling_quantile_by("t", "25m", quantile=0.75, interpolation="higher").alias("q75h"),
                                        v.rolling_quantile_by("t", "25m", quantile=0.2, interpolation="linear", min_samples=2).alias("q2l"),
                                        v.rolling_median_by("t", "15m", closed="both").alias("m15b")),
             float="rel:1e-12", note="a linear interpolation's formula order: an ulp apart"),
    ]
