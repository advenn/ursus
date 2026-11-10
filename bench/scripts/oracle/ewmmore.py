"""The ewmmore group: ewm_sum, ewm_mean_by and ewm_sum_by (ewmmore_test.go, tier 2 of
v0.6.6-scope.md). The ewm family is held to rel:1e-12 as one (step 496): a build that
fuses a*b+c to an FMA differs by an ulp from one that does not; a Float32's to
rel:1e-6, its own ulp being 6e-8.

The fingerprint cases are the 300 rows TestEwmFingerprints held to the sum of Polars'
bits, recorded whole now.
"""

from __future__ import annotations

from datetime import datetime

import polars as pl

from . import Case, case
from .gen import Gen

nan, inf = float("nan"), float("inf")
c = pl.col
x, i, t = c("x"), c("i"), c("t")
DAY_US = 86_400_000_000

EWM = pl.DataFrame({
    "x": pl.Series([1.0, 2, None, 4, 8, None, None, 3], dtype=pl.Float64),
    "i": pl.Series([1, 2, None, 4, 8, None, None, 3], dtype=pl.Int64),
    "g": pl.Series([1, 1, 1, 1, 2, 2, 2, 2], dtype=pl.Int64),
    "t": pl.Series([d * DAY_US for d in (18262, 18263, 18265, 18265, 18268, 18269, 18271, 18272)], dtype=pl.Int64).cast(pl.Datetime("us")),
})
TOL = dict(float="rel:1e-12", note="the ewm family: an FMA fused or not differs by an ulp between builds (step 496)")
TOL32 = dict(float="rel:1e-6", note="a Float32's ewm, within its own ulps (6e-8), the FMA reason as for a Float64")


def days(*ds: int) -> pl.Series:
    return pl.Series("t", [(18262 + d) * DAY_US for d in ds], dtype=pl.Int64).cast(pl.Datetime("us"))


def xt(xs: list, ts: pl.Series) -> pl.DataFrame:
    return pl.DataFrame({"x": pl.Series("x", xs, dtype=pl.Float64), "t": ts})


def sel(*exprs: pl.Expr):
    return lambda df: df.select(*exprs)


def fingerprints() -> pl.DataFrame:
    """300 rows of sevenths, a null every 37th, at times 1 to 17 hours and a few minutes apart."""
    xs, ts, at = [], [], 0
    for k in range(300):
        xs.append(None if k % 37 == 5 else (k * 7919 % 1000) / 7 - 50)
        at += (k * 31 % 17 + 1) * 3_600_000_000 + (k % 7) * 60_000_000
        ts.append(at)
    return pl.DataFrame({"x": pl.Series(xs, dtype=pl.Float64), "t": pl.Series(ts, dtype=pl.Int64).cast(pl.Datetime("us"))})


def cases() -> list[Case]:
    gen = Gen(5010)
    n = 200
    ticks = sorted(int(v) for v in gen.rng.integers(0, 40 * DAY_US, n))
    gen_ewm = pl.DataFrame({"x": gen.floats(n, lo=-100, hi=100, edges=False, name="x"),
                            "t": pl.Series(ticks, dtype=pl.Int64).cast(pl.Datetime("us")),
                            "g": gen.ints(n, pl.Int64, lo=0, hi=2, null_rate=0, edges=False, name="g")})
    fp = fingerprints()
    x32 = x.cast(pl.Float32)
    return [
        # --- ewm_sum ------------------------------------------------------------------------------
        case("sum_alpha_05", EWM, sel(x.ewm_sum(alpha=0.5)), **TOL),
        case("sum_nulls_ignored", EWM, sel(x.ewm_sum(alpha=0.5, ignore_nulls=True)), **TOL),
        case("sum_min_samples_2", EWM, sel(x.ewm_sum(alpha=0.5, min_samples=2)), **TOL),
        case("sum_span_3", EWM, sel(x.ewm_sum(span=3)), **TOL),
        case("sum_ints_com_1", EWM, sel(i.ewm_sum(com=1)), **TOL),
        case("sum_over_g", EWM, sel(x.ewm_sum(alpha=0.5).over("g")), **TOL),
        case("sum_nan_inf", xt([1, nan, 2, inf], days(0, 1, 2, 3)), sel(x.ewm_sum(alpha=0.5)), **TOL),
        case("sum_leading_nulls", xt([None, None, 1, 2], days(0, 1, 2, 3)), sel(x.ewm_sum(alpha=0.5)), **TOL),
        case("sum_leading_nulls_ignored", xt([None, None, 1, 2], days(0, 1, 2, 3)), sel(x.ewm_sum(alpha=0.5, ignore_nulls=True)), **TOL),
        case("sum_f32_alpha_03", EWM, sel(x32.ewm_sum(alpha=0.3)), **TOL32),
        # --- ewm_mean_by and ewm_sum_by ---------------------------------------------------------------
        case("mean_by_2d", EWM, sel(x.ewm_mean_by("t", half_life="2d")), **TOL),
        case("mean_by_ints_1d", EWM, sel(i.ewm_mean_by("t", half_life="1d")), **TOL),
        case("mean_by_over_g", EWM, sel(x.ewm_mean_by("t", half_life="2d").over("g")), **TOL),
        case("mean_by_date_1d", EWM, sel(x.ewm_mean_by(t.dt.date(), half_life="1d")), **TOL),
        case("mean_by_date_36h", EWM, sel(x.ewm_mean_by(t.dt.date(), half_life="36h")), **TOL),
        case("mean_by_ms_1500us", EWM, sel(x.ewm_mean_by(t.cast(pl.Datetime("ms")), half_life="1500us")), float="rel:1e-12",
             note="a half-life finer than the unit, truncated to 1ms"),
        case("mean_by_ns_31h17m3s", EWM, sel(x.ewm_mean_by(t.cast(pl.Datetime("ns")), half_life="31h17m3s")), **TOL),
        case("mean_by_zoned_24h", EWM, sel(x.ewm_mean_by(t.dt.replace_time_zone("Europe/London"), half_life="24h")), **TOL),
        case("mean_by_half_life_0", EWM, sel(x.ewm_mean_by("t", half_life="0d")), **TOL),
        case("mean_by_unsorted", xt([1, 2, 3], days(2, 0, 1)), sel(x.ewm_mean_by("t", half_life="1d")), **TOL),
        case("mean_by_null_time", pl.DataFrame({"x": [1.0, 2, 3], "t": pl.Series([0, None, DAY_US], dtype=pl.Int64).cast(pl.Datetime("us"))})
             .with_columns(t + pl.duration(days=18262)), sel(x.ewm_mean_by("t", half_life="1d")), **TOL),
        case("mean_by_nan", xt([1, nan, 3], days(0, 1, 3)), sel(x.ewm_mean_by("t", half_life="2d")), **TOL),
        case("mean_by_one_instant", xt([1, 5, 3], days(0, 1, 1)), sel(x.ewm_mean_by("t", half_life="1d")), float="rel:1e-12",
             note="two at one instant: the second weighs nothing, and equals the answer"),
        case("mean_by_constant", xt([123.456, 123.456, 123.456], days(0, 1, 3)), sel(x.ewm_mean_by("t", half_life="31h17m3s")), float="rel:1e-12",
             note="a value equal to the last answer is the answer: weighed, d*x + (1-d)*x would be 123.45599999999999"),
        case("sum_by_2d", EWM, sel(x.ewm_sum_by("t", half_life="2d")), **TOL),
        case("mean_by_f32_36h", EWM, sel(x32.ewm_mean_by("t", half_life="36h")), **TOL32),
        case("sum_by_f32_36h", EWM, sel(x32.ewm_sum_by("t", half_life="36h")), **TOL32),
        # --- the fingerprints ---------------------------------------------------------------------------
        case("fingerprints_300", fp, sel(x.ewm_mean_by("t", half_life="5h").alias("mean_5h"), x.ewm_sum_by("t", half_life="31h17m3s").alias("sum_31h"),
                                         x.ewm_sum(alpha=0.1, ignore_nulls=True).alias("sum_01_ignore")), **TOL),
        case("fingerprints_300_f32", fp, sel(x32.ewm_mean_by("t", half_life="5h").alias("mean_5h"), x32.ewm_sum_by("t", half_life="7h").alias("sum_7h"),
                                             x32.ewm_sum(alpha=0.3).alias("sum_03")), **TOL32),
        # --- generated --------------------------------------------------------------------------------------
        case("sum_gen", gen_ewm, sel(x.ewm_sum(alpha=0.2).alias("a02"), x.ewm_sum(span=5, ignore_nulls=True, min_samples=3).alias("s5i3"),
                                     x.ewm_sum(com=2).alias("c2"), x.ewm_sum(half_life=3).alias("h3"), x.ewm_sum(alpha=0.4).over("g").alias("a04g")), **TOL),
        case("by_gen", gen_ewm, sel(x.ewm_mean_by("t", half_life="12h").alias("m12h"), x.ewm_sum_by("t", half_life="3d").alias("s3d"),
                                    x.ewm_mean_by("t", half_life="1d").over("g").alias("m1dg")), **TOL),
    ]
