"""The dtexact group: the .dt totals and epoch on spans and dates past a nanosecond
count (dtexact_test.go, step 231): a Date's epoch beyond 2262, the year 2200's at each
unit Polars has, and ninety minutes' and three centuries' totals.

Polars has no Datetime or Duration of seconds, so Datetime(s) and Duration(s), the
file's own subject, stay in the Go test, held to Go's time package and to integer
arithmetic; here are the same questions at the units both have.
"""

from __future__ import annotations

from datetime import date, datetime

import polars as pl

from . import Case, case
from .dtmore import sel
from .gen import Gen

c = pl.col

FAR = pl.DataFrame({"d": pl.Series([date(2300, 6, 1), date(2262, 4, 12), date(2000, 6, 1), date(1969, 12, 31), date(1700, 1, 1)], dtype=pl.Date)})
Y2200 = pl.DataFrame({"d": pl.Series([datetime(2200, 3, 4, 5, 6, 7)], dtype=pl.Datetime("ms")).dt.replace_time_zone("UTC")})
SECS = 9_467_280_000  # 109575 days, three centuries


def span(n: int, unit: str) -> pl.DataFrame:
    return pl.DataFrame({"n": pl.Series([n], dtype=pl.Int64).cast(pl.Duration(unit))})


def totals(e: pl.Expr) -> list[pl.Expr]:
    return [e.dt.total_days().alias("days"), e.dt.total_hours().alias("hours"), e.dt.total_minutes().alias("minutes"),
            e.dt.total_seconds().alias("seconds")]


def cases() -> list[Case]:
    g = Gen(4990)
    gen_du = g.frame(g.durations(200, "ms", lo_days=-120_000, hi_days=120_000, name="n"))
    gen_dates = g.frame(g.dates(200, date(1, 1, 1), date(9999, 12, 31)))
    gen_dts = g.frame(g.datetimes(200, "ms", lo=1600, hi=2500, name="t"), g.datetimes(200, "us", "Asia/Tokyo", lo=1900, hi=2100, name="tz"))
    return [
        case("epoch_of_far_dates", FAR, sel(c("d").dt.epoch("s")), note="a Date's epoch past 2262 wrapped once (step 231)"),
        case("epoch_2200_ms_us", Y2200, sel(c("d").dt.epoch("s").alias("ms"), c("d").cast(pl.Datetime("us")).dt.epoch("s").alias("us"))),
        case("totals_90min_ms", span(5_400_000, "ms"), sel(*totals(c("n"))[:3])),
        case("totals_90min_us", span(5_400_000_000, "us"), sel(*totals(c("n"))[:3])),
        case("totals_90min_ns", span(5_400_000_000_000, "ns"), sel(*totals(c("n"))[:3])),
        case("totals_three_centuries_ms", pl.DataFrame({"n": pl.Series([SECS * 1000, 86_400_000, 0, -SECS * 1000], dtype=pl.Int64)
                                                        .cast(pl.Duration("ms"))}), sel(*totals(c("n")))),
        # --- generated ---------------------------------------------------------------------
        case("totals_gen", gen_du, sel(*totals(c("n")))),
        case("epoch_dates_gen", gen_dates, sel(c("d").dt.epoch("s").alias("s"), c("d").dt.epoch("d").alias("days"), c("d").dt.epoch("ms").alias("ms"))),
        case("epoch_datetimes_gen", gen_dts, sel(
            c("t").dt.epoch("s").alias("s"), c("t").dt.epoch("ms").alias("ms"), c("t").dt.epoch("d").alias("days"),
            c("tz").dt.epoch("s").alias("tz_s"), c("tz").dt.epoch("us").alias("tz_us"))),
    ]
