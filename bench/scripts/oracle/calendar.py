"""The calendar group: offset_by, month_start and month_end, is_leap_year, round,
convert_time_zone, and truncate across a midnight that does not exist
(calendar_test.go's by-hand tables, step 145): Dates, and Datetimes of microseconds,
naive and in UTC, New York, Tokyo, Kolkata, Santiago, Havana and Asunción, zones of
one form in the zone database.

Not here, with the reason: the refusals, which the Go test keeps; Round by a
time.Duration on a zoned column, the epoch's grid, which Polars' round has no spelling
for (Go's time package is the oracle); and TestCalendarAgainstTheOracle's sweeps, a Go
oracle already. Recorded and not asserted, two findings for the maintainer: round("1mo"),
which Polars 2.1 answers by adding fourteen days and truncating, so the 17th of January
rounds down, where ursus rounds to the nearer month and a value halfway up (Round's
doc); and group_by_dynamic's day across Santiago's missing midnight, which Polars
starts at 23:00 the day before, a day off its own truncate, where ursus starts it at
01:00 (step 145).
"""

from __future__ import annotations

from datetime import datetime

import polars as pl

from . import Case, case
from .dtmore import dates, dts, sel
from .gen import Gen

d, t = pl.col("d"), pl.col("t")


def at(vals: list[datetime | None], zone: str) -> pl.DataFrame:
    """UTC instants read in zone."""
    return dts(vals, "us", "UTC").select(d.dt.convert_time_zone(zone))


def cases() -> list[Case]:
    g = Gen(4981)
    gen_dates = g.frame(g.dates(200))
    gen_ny = g.frame(g.datetimes(150, "us", "America/New_York", lo=2023, hi=2025))
    gen_naive = g.frame(g.datetimes(150, "us"))
    gen_utc = g.frame(g.datetimes(120, "us", "UTC", lo=2020, hi=2026))
    return [
        # --- offset_by ---------------------------------------------------------------------
        case("offset_1mo_dates", dates([(2024, 1, 31), (2023, 1, 31), (2024, 3, 31), None]), sel(d.dt.offset_by("1mo")),
             note="months clamp to the end of the month"),
        case("offset_1y_leap_day", dates([(2024, 2, 29)]), sel(d.dt.offset_by("1y"))),
        case("offset_minus_1mo_utc", dts([datetime(2024, 3, 31, 10, 15)], zone="UTC"), sel(d.dt.offset_by("-1mo"))),
        case("offset_2d_date", dates([(2024, 12, 31)]), sel(d.dt.offset_by("2d")),
             note="a Date by a whole number of days, spelled as a Duration here"),
        case("offset_1d2h_naive", dts([datetime(2024, 3, 10, 1, 30)]), sel(d.dt.offset_by("1d2h")),
             note="a naive Datetime moves its wall clock"),
        case("offset_1h_ny_spring_forward", dts([datetime(2024, 3, 10, 1, 30)], zone="America/New_York"),
             sel(d.dt.offset_by("1h")), note="an hour is elapsed time: 01:30 EST is 03:30 EDT an hour later"),
        case("offset_minus_90m_ny", dts([datetime(2024, 3, 10, 3, 30)], zone="America/New_York"), sel(d.dt.offset_by("-90m"))),
        # --- month_start, month_end, is_leap_year -------------------------------------------
        case("month_start_dates", dates([(2024, 2, 10), (2023, 12, 31), None]), sel(d.dt.month_start())),
        case("month_end_dates", dates([(2024, 2, 10), (2023, 2, 10), (1900, 2, 1), (2000, 2, 1)]), sel(d.dt.month_end())),
        case("month_start_keeps_time", dts([datetime(2024, 2, 10, 13, 45, 1, 500000)], zone="UTC"), sel(d.dt.month_start())),
        case("month_end_tokyo", at([datetime(2024, 1, 31, 20)], "Asia/Tokyo"), sel(d.dt.month_end()),
             note="2024-01-31T20:00Z is February the 1st in Tokyo: its month is February"),
        case("is_leap_year", dates([(1900, 6, 1), (2000, 6, 1), (2023, 6, 1), (2024, 6, 1), (2100, 6, 1), (4, 6, 1), None]),
             sel(d.dt.is_leap_year())),
        case("is_leap_year_zoned", dts([datetime(2024, 12, 31, 23, 30)], zone="UTC"),
             sel(d.dt.is_leap_year().alias("utc"), d.dt.convert_time_zone("Asia/Tokyo").dt.is_leap_year().alias("tokyo")),
             note="the year is the zone's: the last half hour of 2024 in UTC is 2025 in Tokyo"),
        # --- round -------------------------------------------------------------------------
        case("round_15m", dts([datetime(2024, 1, 1, 10, 7, 30), datetime(2024, 1, 1, 10, 7, 29, 999999), None], zone="UTC"),
             sel(d.dt.round("15m")), note="halfway rounds up, and only halfway"),
        case("round_1h_before_epoch", dts([datetime(1969, 12, 31, 23, 30), datetime(1969, 12, 31, 23, 29, 59)]), sel(d.dt.round("1h"))),
        case("round_1mo", dts([datetime(2024, 2, 15, 12), datetime(2024, 2, 15, 11, 59, 59)], zone="UTC"), sel(d.dt.round("1mo")),
             note="a finding: Polars adds fourteen days and truncates, so noon on the 15th of a 29-day February rounds down; "
                  "ursus rounds to the nearer month, halfway up"),
        case("round_1mo_dates", dates([(2024, 1, 16), (2024, 1, 17), (2023, 2, 15)]), sel(d.dt.round("1mo")),
             note="a finding: the 17th of January rounds down in Polars, up here, to the nearer month"),
        case("round_1h_kolkata", dts([datetime(2024, 6, 1, 10, 47)], zone="Asia/Kolkata"), sel(d.dt.round("1h")),
             note="the wall clock's hour at +05:30"),
        # --- convert_time_zone -------------------------------------------------------------
        case("convert_tokyo", dts([datetime(2024, 6, 1, 9), None], zone="UTC"),
             sel(d.dt.convert_time_zone("Asia/Tokyo"), d.dt.convert_time_zone("Asia/Tokyo").dt.hour().alias("h"),
                 d.dt.convert_time_zone("Asia/Tokyo").dt.convert_time_zone("UTC").alias("back")),
             note="the components follow the new zone, the instants do not move"),
        # --- a day that starts where its midnight does not exist -----------------------------
        case("truncate_1d_santiago", dts([datetime(2024, 9, 8, 10)], zone="America/Santiago"), sel(d.dt.truncate("1d")),
             note="2024-09-08 begins at 01:00 in Santiago"),
        case("truncate_1d_havana", dts([datetime(2024, 3, 10, 10)], zone="America/Havana"), sel(d.dt.truncate("1d"))),
        case("truncate_1mo_asuncion", dts([datetime(2023, 10, 15, 10)], zone="America/Asuncion"), sel(d.dt.truncate("1mo")),
             note="October 2023 began at 01:00 in Asunción"),
        case("midnight_gap_windows",
             dts([datetime(2024, 9, 7, 12), datetime(2024, 9, 7, 23, 30), datetime(2024, 9, 8, 12)], zone="America/Santiago"),
             lambda df: df.group_by_dynamic("d", every="1d").agg(pl.len().alias("n")),
             note="a finding: Polars starts the 8th's window at 23:00 on the 7th, a day off its own truncate; "
                  "ursus starts it at 01:00, where the day begins (step 145)"),
        # --- generated ---------------------------------------------------------------------
        case("offset_gen_dates", gen_dates, sel(
            d.dt.offset_by("1mo").alias("mo"), d.dt.offset_by("-1mo").alias("mo_back"), d.dt.offset_by("1y").alias("y"),
            d.dt.offset_by("1d").alias("day"), d.dt.offset_by("2w").alias("w2"), d.dt.offset_by("1mo1d").alias("mo1d"))),
        case("offset_gen_ny", gen_ny, sel(
            t.dt.offset_by("1mo").alias("mo"), t.dt.offset_by("1y").alias("y"), t.dt.offset_by("1d").alias("day"),
            t.dt.offset_by("2d3h").alias("d2h3"), t.dt.offset_by("1h").alias("h"), t.dt.offset_by("-90m").alias("m90"))),
        case("month_gen_dates", gen_dates, sel(
            d.dt.month_start().alias("start"), d.dt.month_end().alias("end"), d.dt.is_leap_year().alias("leap"))),
        case("month_gen_ny", gen_ny, sel(
            t.dt.month_start().alias("start"), t.dt.month_end().alias("end"), t.dt.is_leap_year().alias("leap"))),
        case("round_gen_naive", gen_naive, sel(
            t.dt.round("15m").alias("m15"), t.dt.round("1h").alias("h"), t.dt.round("1d").alias("day"))),
        case("round_gen_utc", gen_utc, sel(t.dt.round("15m").alias("m15"), t.dt.round("1h").alias("h"))),
        case("convert_gen", gen_utc, sel(
            t.dt.convert_time_zone("Asia/Tokyo").alias("tokyo"), t.dt.convert_time_zone("America/New_York").alias("ny"),
            t.dt.convert_time_zone("Asia/Kolkata").dt.hour().alias("kolkata_hour"))),
    ]
