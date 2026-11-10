"""The window172 group: Interpolate's nearest method and rolling windows labelled at
their middle row (window172_test.go, step 172)."""

from __future__ import annotations

from datetime import date

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
v = c("v")
NEAREST = v.interpolate(method="nearest")
SIX = pl.DataFrame({"v": pl.Series([1.0, 2, 3, 4, 5, 6], dtype=pl.Float64)})
PARTS = pl.DataFrame({"g": ["a", "b", "a", "b", "a", "b", "a"], "v": pl.Series([1.0, 10, 2, 20, 3, 30, 4], dtype=pl.Float64)})


def ints(vals: list) -> pl.DataFrame:
    return pl.DataFrame({"v": pl.Series("v", vals, dtype=pl.Int64)})


def sel(*exprs: pl.Expr):
    return lambda df: df.select(*exprs)


def center(w: int):
    return sel(v.rolling_sum(w, center=True).alias("full"), v.rolling_sum(w, center=True, min_samples=1).alias("partial"))


def cases() -> list[Case]:
    gen = Gen(5011)
    n = 150
    gen_ints = gen.frame(gen.ints(n, pl.Int64, lo=-100, hi=100, null_rate=0.3, name="v"))
    gen_roll = gen.frame(gen.ints(n, pl.Int64, lo=0, hi=2, null_rate=0, edges=False, name="g"), gen.floats(n, lo=-10, hi=10, edges=False, name="v"))
    return [
        case("nearest_ints", ints([1, None, None, 7, None, 10, None, None, None, 20, None]), sel(NEAREST), note="an Int64 kept"),
        case("nearest_ints_short", ints([1, None, 4]), sel(NEAREST), note="nearer the later value on a tie"),
        case("nearest_ints_ends", ints([None, 2, None]), sel(NEAREST)),
        case("nearest_floats", pl.DataFrame({"v": pl.Series([1.5, None, None, None, 4.5], dtype=pl.Float64)}), sel(NEAREST)),
        case("nearest_dates", pl.DataFrame({"t": pl.Series([date(2024, 1, 1), None, date(2024, 1, 3)], dtype=pl.Date)}),
             sel(c("t").interpolate(method="nearest")), note="an instant keeps its type: nearer the later day on a tie"),
        case("rolling_center_2", SIX, center(2)),
        case("rolling_center_3", SIX, center(3)),
        case("rolling_center_4", SIX, center(4)),
        case("center_per_partition", PARTS, sel(v.rolling_max(3, center=True, min_samples=1).over("g").alias("centered"),
                                                v.rolling_max(3, min_samples=1).over("g").alias("trailing"))),
        case("linear_and_nearest", ints([1, None, None, 7]), sel(v.interpolate().alias("linear"), NEAREST.alias("nearest")),
             note="two window functions in one Select, not one shared"),
        # --- generated -----------------------------------------------------------------------------
        case("nearest_gen", gen_ints, sel(v.interpolate().alias("linear"), NEAREST.alias("nearest")), float="rel:1e-12",
             note="a linear interpolation's formula order: an ulp apart; the nearest value exact"),
        case("center_gen", gen_roll, sel(v.rolling_sum(3, center=True).alias("s3"), v.rolling_sum(4, center=True, min_samples=1).alias("s4p"),
                                         v.rolling_max(5, center=True, min_samples=2).over("g").alias("m5g"),
                                         v.rolling_mean(3, center=True, min_samples=1).over("g").alias("mean3g")),
             float="rel:1e-12", note="a sliding sum's residual is an ulp"),
    ]
