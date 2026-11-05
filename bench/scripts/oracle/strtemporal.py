"""The strtemporal group: str.to_date, str.to_datetime and str.to_time with no format,
and strptime with none, by Polars' patterns (strtemporal_test.go, step 356).

ursus's ToDate, ToDatetime and ToTime of no format are lenient, null where nothing
parses, where Polars' are strict by default (feature-gaps.md §9), so the lenient
question is asked with strict=False; a wall clock a zone skips or reads twice is null
there, which Polars answers with strict=False and ambiguous="null". Not here: two
values of two patterns in one column, which Polars refuses and ursus reads each by its
own (§9; Go's time package is the oracle), and the strict refusals.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .dtmore import sel
from .gen import Gen

x = pl.col("x")
NY = "America/New_York"


def strs(*vals: str | None) -> pl.DataFrame:
    return pl.DataFrame({"x": pl.Series("x", list(vals), dtype=pl.String)})


def cases() -> list[Case]:
    g = Gen(4986)
    n = 150
    dates = g.dates(n, edges=False).to_list()
    bad = {int(i) for i in g.rng.choice(n, 8, replace=False)}
    text = lambda f: pl.Series("x", [None if v is None else ("x" if i in bad else f(v)) for i, v in enumerate(dates)], dtype=pl.String)
    gen_iso = pl.DataFrame({"x": text(lambda v: v.isoformat())})
    gen_slash = pl.DataFrame({"x": text(lambda v: v.strftime("%d/%m/%Y"))})
    gen_dots = pl.DataFrame({"x": text(lambda v: v.strftime("%Y.%m.%d"))})
    dts = g.datetimes(n, "us", lo=1960, hi=2040, edges=False).to_list()
    gen_dts = pl.DataFrame({"x": pl.Series("x", [None if v is None else v.strftime("%Y-%m-%d %H:%M:%S.%f") for v in dts], dtype=pl.String)})
    gen_zoned = pl.DataFrame({"x": pl.Series("x", [None if v is None else v.strftime("%Y-%m-%dT%H:%M:%S") + "+09:00" for v in dts],
                                             dtype=pl.String)})
    tods = g.rng.integers(0, 86_400_000_000, n)
    gen_times = pl.DataFrame({"x": pl.Series("x", [None if g.rng.random() < 0.1 else
                                                   f"{v // 3_600_000_000:02d}:{v // 60_000_000 % 60:02d}:{v // 1_000_000 % 60:02d}.{v % 1_000_000:06d}"
                                                   for v in tods], dtype=pl.String)})
    return [
        case("to_date_day_first", strs("02/01/2020", "31/12/2021"), sel(x.str.to_date())),
        case("to_date_lenient_null", strs("02/01/2020", "x"), sel(x.str.to_date(strict=False)),
             note="ursus's ToDate is lenient, null where nothing parses; Polars' asked with strict=False"),
        case("to_date_null_first", strs(None, "2020.01.02"), sel(x.str.to_date())),
        case("to_date_never_month_first", strs("01/02/2020"), sel(x.str.to_date())),
        case("strptime_no_format", strs("02/01/2020"), sel(x.str.strptime(pl.Date, None))),
        case("to_datetime_two_instants", strs("2020-01-02 03:04:05", "2021-01-02T03:04"), sel(x.str.to_datetime(time_unit="us"))),
        case("to_datetime_milliseconds", strs("2020-01-02 03:04:05.123456"), sel(x.str.to_datetime(time_unit="ms"))),
        case("to_datetime_nanoseconds", strs("2020-01-02T03:04:05.123456789"), sel(x.str.to_datetime(time_unit="ns"))),
        case("to_datetime_of_a_date", strs("2020-01-02"), sel(x.str.to_datetime(time_unit="us"))),
        case("to_datetime_zone_wall_clock", strs("2020-01-02 03:04:05"), sel(x.str.to_datetime(time_unit="us", time_zone="Asia/Tokyo")),
             note="a text of no offset is read on the zone's wall clock"),
        case("to_datetime_offset_in_zone", strs("2020-01-02T03:04:05+0900"), sel(x.str.to_datetime(time_unit="us", time_zone="Asia/Tokyo"))),
        case("to_datetime_offset_in_utc", strs("2020-01-02T03:04:05+0900"), sel(x.str.to_datetime(time_unit="us", time_zone="UTC"))),
        case("to_time_one_digit_hour", strs("3:04:05"), sel(x.str.to_time(strict=False))),
        case("to_datetime_skipped_wall_clock", strs("2024-03-10 02:30:00"),
             sel(x.str.to_datetime(time_unit="us", time_zone=NY, strict=False)),
             note="a wall clock New York skips is null when lenient"),
        case("to_datetime_wall_clock_read_twice", strs("2024-11-03 01:30:00"),
             sel(x.str.to_datetime(time_unit="us", time_zone=NY, strict=False, ambiguous="null")),
             note="and one it reads twice; Polars asked with ambiguous null"),
        # --- generated ---------------------------------------------------------------------
        case("to_date_gen_iso", gen_iso, sel(x.str.to_date(strict=False))),
        case("to_date_gen_day_first", gen_slash, sel(x.str.to_date(strict=False))),
        case("to_date_gen_dots", gen_dots, sel(x.str.to_date(strict=False))),
        case("to_datetime_gen", gen_dts, sel(
            x.str.to_datetime(time_unit="us").alias("us"), x.str.to_datetime(time_unit="ms").alias("ms"),
            x.str.to_datetime(time_unit="ns").alias("ns"), x.str.to_datetime(time_unit="us", time_zone="Asia/Kolkata").alias("kolkata"))),
        case("to_datetime_gen_offsets", gen_zoned, sel(
            x.str.to_datetime(time_unit="us", time_zone="UTC").alias("utc"),
            x.str.to_datetime(time_unit="us", time_zone="Asia/Tokyo").alias("tokyo"))),
        case("to_time_gen", gen_times, sel(x.str.to_time())),
    ]
