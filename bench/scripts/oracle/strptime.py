"""The strptime group: parsing and formatting with strftime directives
(strptime_test.go, step 113). Polars has no second unit and no Date formatted with a
clock, so those are held to Go's time in the Go file; a Time is Polars' nanoseconds.
"""

from __future__ import annotations

import datetime as dt

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
S = pl.col("s").str
US, US_UTC = pl.Datetime("us"), pl.Datetime("us", "UTC")

AT = dt.datetime(2024, 2, 9, 15, 4, 5, 123456)
STAMPS = pl.DataFrame({"ts": pl.Series("ts", [AT], dtype=US_UTC)}).with_columns(
    c("ts").cast(pl.Date).alias("day"), c("ts").dt.convert_time_zone("Asia/Tokyo").alias("tokyo"),
    c("ts").dt.replace_time_zone(None).alias("naive")).with_columns(c("naive").cast(pl.Time).alias("clock"))


def parse(name: str, vals: list[str], dtype: pl.DataType, fmt: str, strict: bool = True, **opts) -> Case:
    return case(name, pl.DataFrame({"s": vals}), lambda df: df.select(S.strptime(dtype, fmt, strict=strict).alias("v")),
                params={"format": fmt}, **opts)


def fmt(name: str, col: str, f: str, **opts) -> Case:
    return case(name, STAMPS, lambda df: df.select(c(col).dt.strftime(f).alias("s")), params={"format": f}, **opts)


def cases() -> list[Case]:
    g = Gen(49915)
    n = 150
    stamps = g.datetimes(n, "us", lo=1900, hi=2100, name="t")
    dates = g.dates(n, lo=dt.date(1800, 1, 1), hi=dt.date(2200, 12, 31), name="d")
    zoned = g.datetimes(n, "us", "Europe/Paris", lo=2024, hi=2024, name="z")
    gen = g.frame(stamps, dates, zoned, g.times(n))
    texts = gen.select(
        c("t").dt.strftime("%Y-%m-%d %H:%M:%S%.6f").alias("iso"), c("t").dt.strftime("%d/%m/%Y %I:%M %p").alias("dmy_12h"),
        c("d").dt.strftime("%A %e %B %Y").alias("long"), c("d").dt.strftime("%Y-%j").alias("doy"),
        c("z").dt.strftime("%Y-%m-%d %H:%M:%S %z").alias("offset"), c("tm").dt.strftime("%H:%M:%S%.9f").alias("clock"))
    return [
        parse("day_month_year", ["09/02/2024", "29/02/2024", "1/3/2024"], pl.Date, "%d/%m/%Y"),
        parse("month_names", ["Feb  9, 2024", "Dec 31, 1999"], pl.Date, "%b %e, %Y"),
        parse("day_of_year", ["2024-060", "2023-001"], pl.Date, "%Y-%j"),
        parse("naive_with_a_fraction", ["2024-02-09 15:04:05.123456", "1969-12-31 23:59:59"], US, "%Y-%m-%d %H:%M:%S%.f"),
        parse("offset_read_into_utc", ["2024-02-09 15:04 +0530", "2024-02-09 15:04 -0100"], US_UTC, "%Y-%m-%d %H:%M %z"),
        parse("offset_read_into_naive", ["2024-02-09 15:04 +0530"], US, "%Y-%m-%d %H:%M %z",
              note="the naive Datetime of its UTC wall clock here; Polars types the column UTC (feature-gaps.md §9)"),
        parse("a_zones_wall_clock", ["2024-01-15 09:00", "2024-07-15 09:00"], pl.Datetime("us", "America/New_York"), "%Y-%m-%d %H:%M"),
        parse("digits_finer_than_the_unit", ["2024-02-09 15:04:05.123999"], pl.Datetime("ms"), "%Y-%m-%d %H:%M:%S%.6f"),
        parse("twelve_hour_clock", ["12:05 AM", "12:05 PM", "07:30 pm"], pl.Time, "%I:%M %p"),
        parse("not_strict_is_null", ["2024-02-09", "2023-02-29", "nope", "2024-02-09 "], pl.Date, "%Y-%m-%d", strict=False),
        fmt("date_long", "day", "%A, %e %B %Y"),
        fmt("naive_twelve_hour", "naive", "%d/%m/%y %I:%M:%S%.3f %p"),
        fmt("utc_has_an_offset", "ts", "%H:%M %z"),
        fmt("zoned_wall_clock", "tokyo", "%F %T %z %Z"),
        fmt("time", "clock", "%Hh%M"),
        fmt("composite_directives", "naive", "%j %a %h %y %D %R"),
        # --- generated -----------------------------------------------------------------
        case("strftime_gen", gen, lambda df: texts, note="six formats over instants, dates, a zone's instants and times of day"),
        case("strptime_gen", texts, lambda df: df.select(
            c("iso").str.strptime(US, "%Y-%m-%d %H:%M:%S%.6f"), c("dmy_12h").str.strptime(US, "%d/%m/%Y %I:%M %p"),
            c("long").str.strptime(pl.Date, "%A %e %B %Y"), c("doy").str.strptime(pl.Date, "%Y-%j"),
            c("offset").str.strptime(US_UTC, "%Y-%m-%d %H:%M:%S %z"), c("clock").str.strptime(pl.Time, "%H:%M:%S%.9f")),
             note="the generated texts parsed back"),
    ]
