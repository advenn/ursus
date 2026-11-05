"""The daterange group: date_range, datetime_range and time_range, and their per-row
forms (daterange_test.go, G2 of v0.6.6-scope.md, step 264).

A range of literal ends is named `literal` by Polars, and here after its generator;
the range of a frame's column keeps the column's name. Each case of literals aliases its
answer `r` on both sides, so the recording holds the values. Not here: the refusals,
which the Go test keeps, and the batch sizes, a differential there.
"""

from __future__ import annotations

from datetime import date, datetime, time

import numpy as np
import polars as pl

from . import Case, case
from .dtmore import sel
from .gen import Gen

ONE = pl.DataFrame({"a": pl.Series([1, 2, 3], dtype=pl.Int64)})
LISTS = pl.DataFrame({"s": pl.Series([date(2024, 1, 1), None], dtype=pl.Date),
                      "e": pl.Series([date(2024, 1, 3), date(2024, 1, 2)], dtype=pl.Date)})


def ny(v: datetime) -> pl.Expr:
    return pl.lit(v).dt.replace_time_zone("America/New_York")


def r(e: pl.Expr):
    return sel(e.alias("r"))


def cases() -> list[Case]:
    g = Gen(4984)
    n = 60
    starts = g.dates(n, date(2024, 1, 1), date(2025, 6, 30), null_rate=0.1, edges=False, name="s")
    spans = g.rng.integers(-5, 61, n)
    ends = pl.Series("e", [None if s is None else date.fromordinal(s.toordinal() + int(k)) for s, k in zip(starts.to_list(), spans)],
                     dtype=pl.Date)
    gen_dates = g.frame(starts, ends)
    t0 = g.datetimes(n, "us", lo=2024, hi=2024, null_rate=0.1, edges=False, name="s")
    hours = g.rng.integers(-6, 240, n)
    t1 = pl.Series("e", [None if s is None else s + np.timedelta64(int(h), "h") for s, h in zip(t0.to_list(), hours)],
                   dtype=pl.Datetime("us"))
    gen_dts = g.frame(t0, t1)
    secs = g.rng.integers(0, 86400, n)
    tm0 = pl.Series("s", [time(int(v) // 3600, int(v) % 3600 // 60, int(v) % 60) for v in secs], dtype=pl.Time)
    tm1 = pl.Series("e", [time(min(23, int(v) // 3600 + int(k)), int(v) % 3600 // 60) for v, k in zip(secs, g.rng.integers(0, 6, n))],
                    dtype=pl.Time)
    gen_times = g.frame(tm0, tm1)
    return [
        case("days", ONE, r(pl.date_range(date(2024, 1, 30), date(2024, 2, 2), "1d"))),
        case("months_each_from_the_start", ONE, r(pl.date_range(date(2024, 1, 31), date(2024, 5, 1), "1mo"))),
        case("weeks", ONE, r(pl.date_range(date(2024, 1, 1), date(2024, 1, 20), "1w"))),
        case("closed_left", ONE, r(pl.date_range(date(2024, 1, 1), date(2024, 1, 3), "1d", closed="left"))),
        case("closed_none", ONE, r(pl.date_range(date(2024, 1, 1), date(2024, 1, 3), "1d", closed="none"))),
        case("reversed", ONE, r(pl.date_range(date(2024, 1, 3), date(2024, 1, 1), "1d")), note="no rows"),
        case("negative_interval", ONE, r(pl.date_range(date(2024, 1, 3), date(2024, 1, 1), "-1d")), note="no rows"),
        case("from_an_instant", ONE, r(pl.date_range(pl.lit(datetime(2024, 1, 1, 12)), pl.lit(datetime(2024, 1, 3)), "1d"))),
        case("hours", ONE, r(pl.datetime_range(datetime(2024, 1, 1), datetime(2024, 1, 1, 3), "1h"))),
        case("ninety_minutes", ONE, r(pl.datetime_range(datetime(2024, 1, 1), datetime(2024, 1, 1, 3), "90m"))),
        case("from_dates", ONE, r(pl.datetime_range(date(2024, 1, 1), date(2024, 1, 2), "12h"))),
        case("ny_day_on_the_wall_clock", ONE, r(pl.datetime_range(ny(datetime(2024, 3, 9, 12)), ny(datetime(2024, 3, 11, 12)), "1d")),
             note="a day keeps 12:00 across the spring forward"),
        case("ny_hour_elapsed", ONE, r(pl.datetime_range(ny(datetime(2024, 3, 10, 0)), ny(datetime(2024, 3, 10, 4)), "1h")),
             note="an hour is elapsed time: 02:00 never reads"),
        case("months_at_six", ONE, r(pl.datetime_range(datetime(2024, 1, 31, 6), datetime(2024, 4, 30, 6), "1mo"))),
        case("a_month_and_a_day", ONE, r(pl.datetime_range(datetime(2024, 1, 31), datetime(2024, 6, 1), "1mo1d"))),
        case("times", ONE, r(pl.time_range(time(9, 0), time(11, 0), "30m"))),
        case("times_open", ONE, r(pl.time_range(time(9, 0), time(11, 0), "1h", closed="none"))),
        case("range_beside_columns", ONE,
             lambda df: df.with_columns(pl.date_range(date(2024, 1, 1), date(2024, 1, 3), "1d").dt.day().alias("d")),
             note="beside the frame's columns, a range of its height"),
        case("date_ranges_per_row", LISTS, sel(pl.date_ranges("s", "e", "1d"))),
        # --- generated ---------------------------------------------------------------------
        case("date_ranges_gen", gen_dates, sel(
            pl.date_ranges("s", "e", "1d").alias("d1"), pl.date_ranges("s", "e", "1w", closed="left").alias("w1"),
            pl.date_ranges("s", "e", "1mo", closed="right").alias("mo1"), pl.date_ranges("s", "e", "3d", closed="none").alias("d3"))),
        case("datetime_ranges_gen", gen_dts, sel(
            pl.datetime_ranges("s", "e", "6h").alias("h6"), pl.datetime_ranges("s", "e", "1d", closed="left").alias("d1"),
            pl.datetime_ranges("s", "e", "90m", closed="none").alias("m90"))),
        case("time_ranges_gen", gen_times, sel(
            pl.time_ranges("s", "e", "30m").alias("m30"), pl.time_ranges("s", "e", "1h", closed="right").alias("h1"))),
    ]
