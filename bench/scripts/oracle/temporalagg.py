"""The temporalagg group: the mean, median and quantiles of Dates, Datetimes, Times and
Durations (temporalagg_test.go, steps 173 and 300): a Date's mean is a Datetime(us), a
Datetime's keeps its unit, truncated toward zero, a Time's and a Duration's keep their
types.

Not here: var and std of an instant, refused here and null in Polars (the Go test
keeps the refusal), and Parquet's UUID and JSON columns, a file written by Go.
"""

from __future__ import annotations

from datetime import date, datetime, time, timedelta

import polars as pl

from . import Case, case
from .dtmore import sel
from .gen import Gen

c = pl.col


def dates(vals: list[date | None]) -> pl.DataFrame:
    return pl.DataFrame({"d": pl.Series("d", vals, dtype=pl.Date)})


def instants(*vals: datetime) -> pl.DataFrame:
    return pl.DataFrame({"t": pl.Series("t", list(vals), dtype=pl.Datetime("us"))})


Q = pl.DataFrame({
    "d": pl.Series([date(2026, 1, 1), date(2026, 1, 4), None, date(2026, 1, 8)], dtype=pl.Date),
    "t": pl.Series([datetime(2026, 1, 1, 6), datetime(2026, 1, 1, 7, 30), None, datetime(2026, 1, 3, 6)], dtype=pl.Datetime("us")),
    "u": pl.Series([timedelta(hours=1), timedelta(hours=2), None, timedelta(minutes=250)], dtype=pl.Duration("ns")),
    "tm": pl.Series([time(1), time(2, 30), None, time(9)], dtype=pl.Time),
})


def cases() -> list[Case]:
    g = Gen(4987)
    n = 200
    gen = g.frame(g.dates(n, name="d"), g.datetimes(n, "us", lo=2020, hi=2025, name="t"),
                  g.datetimes(n, "ms", "Asia/Tokyo", lo=2020, hi=2025, name="tz"), g.durations(n, "ns", name="u"), g.times(n, name="tm"))
    quantiles = [(0.5, "linear"), (0.25, "linear"), (0.5, "nearest"), (0.9, "lower"), (0.9, "higher"), (0.3, "midpoint")]
    return [
        case("date_mean", dates([date(2024, 1, 1), date(2024, 1, 4), None]), sel(c("d").mean())),
        case("date_median_even", dates([date(2024, 1, 1), date(2024, 1, 2)]), sel(c("d").median())),
        case("date_mean_before_1970", dates([date(1969, 12, 31), date(1969, 12, 30)]), sel(c("d").mean())),
        case("datetime_mean", instants(datetime(2024, 1, 1), datetime(2024, 1, 1, 0, 1), datetime(2024, 1, 1, 0, 3)), sel(c("t").mean())),
        case("datetime_median", instants(datetime(2024, 1, 1), datetime(2024, 1, 1, 0, 1), datetime(2024, 1, 1, 0, 3)), sel(c("t").median())),
        case("datetime_mean_truncated", instants(datetime(2024, 1, 1, 0, 0, 0, 1), datetime(2024, 1, 1, 0, 0, 0, 2)), sel(c("t").mean())),
        case("datetime_median_even_truncated", instants(datetime(2024, 1, 1, 0, 0, 0, 1), datetime(2024, 1, 1, 0, 0, 0, 2)),
             sel(c("t").median())),
        case("datetime_mean_before_1970", instants(datetime(1969, 12, 31, 23, 59, 59, 999999), datetime(1969, 12, 31, 23, 59, 59, 999998)),
             sel(c("t").mean()), note="toward zero"),
        case("datetime_median_before_1970", instants(datetime(1969, 12, 31, 23, 59, 59, 999999), datetime(1969, 12, 31, 23, 59, 59, 999998)),
             sel(c("t").median())),
        case("time_mean_median", pl.DataFrame({"u": pl.Series([time(1), time(3), time(4)], dtype=pl.Time)}),
             sel(c("u").mean().alias("mean"), c("u").median().alias("median"))),
        case("duration_median", pl.DataFrame({"du": pl.Series([timedelta(seconds=1), timedelta(seconds=4), None], dtype=pl.Duration("ns"))}),
             sel(c("du").median())),
        case("grouped_mean", pl.DataFrame({"g": ["a", "a", "b"],
                                           "t": pl.Series([datetime(2024, 1, 1), datetime(2024, 1, 1, 0, 2), datetime(2024, 5, 5)],
                                                          dtype=pl.Datetime("us"))}),
             lambda df: df.group_by("g").agg(c("t").mean().alias("m")), order="set"),
        *[case(f"quantile_{q}_{ip}", Q, sel(pl.all().quantile(q, ip))) for q, ip in quantiles],
        # --- generated ---------------------------------------------------------------------
        case("mean_median_gen", gen, sel(
            c("d").mean().alias("d_mean"), c("d").median().alias("d_median"), c("t").mean().alias("t_mean"), c("t").median().alias("t_median"),
            c("tz").mean().alias("tz_mean"), c("tz").median().alias("tz_median"), c("u").mean().alias("u_mean"), c("u").median().alias("u_median"),
            c("tm").mean().alias("tm_mean"), c("tm").median().alias("tm_median"))),
        *[case(f"quantile_gen_{q}_{ip}", gen, sel(pl.all().quantile(q, ip))) for q, ip in quantiles],
    ]
