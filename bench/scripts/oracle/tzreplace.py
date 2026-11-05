"""The tzreplace group: replace_time_zone (tzreplace_test.go, step 174), read back in
UTC: a naive clock gaining a zone, London's spring forward made null and its fall back
read earliest, latest or null, Samoa's day that never read, Lord Howe's half-hour fold,
a zoned clock changing zone or made naive, a fold read in a zone without one, before
1970, nanoseconds, and a column of milliseconds keeping its unit. London, Apia, Lord
Howe, Tokyo, New York and Paris have one form in the zone database; Dublin and
Casablanca, which have two, are not here.

Not here: the refusals and the raise by default, which the Go test keeps.
"""

from __future__ import annotations

from datetime import datetime

import polars as pl

from . import Case, case
from .dtmore import dts, sel
from .gen import Gen

d, t = pl.col("d"), pl.col("t")


def in_utc(e: pl.Expr) -> pl.Expr:
    return e.dt.convert_time_zone("UTC")


def at(vals: list[datetime | None], zone: str) -> pl.DataFrame:
    """UTC instants read in zone."""
    return dts(vals, "us", "UTC").select(d.dt.convert_time_zone(zone))


def cases() -> list[Case]:
    g = Gen(4982)
    gen_naive = g.frame(g.datetimes(200, "us", lo=2023, hi=2025))
    gen_ny = g.frame(g.datetimes(120, "us", "America/New_York", lo=2023, hi=2025))
    gen_ms = g.frame(g.datetimes(100, "ms", lo=2023, hi=2025))
    london = "Europe/London"
    fall = [datetime(2024, 10, 27, 0, 0), datetime(2024, 10, 27, 0, 30), datetime(2024, 10, 27, 1, 0),
            datetime(2024, 10, 27, 1, 30), datetime(2024, 10, 27, 2, 0)]
    lord_howe = [datetime(2024, 4, 7, 1, 15), datetime(2024, 4, 7, 1, 30), datetime(2024, 4, 7, 1, 45), datetime(2024, 4, 7, 2, 0)]
    return [
        case("naive_gains_london", dts([datetime(2024, 3, 31, 0, 30), None]), sel(in_utc(d.dt.replace_time_zone(london)))),
        case("spring_forward_null",
             dts([datetime(2024, 3, 31, 0, 0), datetime(2024, 3, 31, 0, 30), datetime(2024, 3, 31, 1, 0), datetime(2024, 3, 31, 1, 30)]),
             sel(in_utc(d.dt.replace_time_zone(london, non_existent="null"))),
             note="London's clocks go from 01:00 GMT to 02:00 BST: 01:00 and 01:30 never read"),
        case("fall_back_earliest", dts(fall), sel(in_utc(d.dt.replace_time_zone(london, ambiguous="earliest"))),
             note="and back from 02:00 BST to 01:00 GMT: 01:00 and 01:30 read twice"),
        case("fall_back_latest", dts(fall), sel(in_utc(d.dt.replace_time_zone(london, ambiguous="latest")))),
        case("fold_and_gap_null",
             dts([datetime(2024, 3, 31, 0, 30), datetime(2024, 3, 31, 1, 30), datetime(2024, 3, 31, 2, 30),
                  datetime(2024, 10, 27, 0, 30), datetime(2024, 10, 27, 1, 30), datetime(2024, 10, 27, 2, 30), None]),
             sel(in_utc(d.dt.replace_time_zone(london, ambiguous="null", non_existent="null")))),
        case("apia_day_never_read",
             dts([datetime(2011, 12, 29, 23), datetime(2011, 12, 30, 0), datetime(2011, 12, 30, 12), datetime(2011, 12, 30, 23, 59),
                  datetime(2011, 12, 31, 0)]),
             sel(in_utc(d.dt.replace_time_zone("Pacific/Apia", non_existent="null"))),
             note="Samoa crossed the date line: 2011-12-30 never began in Apia, a gap of a day"),
        case("lord_howe_earliest", dts(lord_howe), sel(in_utc(d.dt.replace_time_zone("Australia/Lord_Howe", ambiguous="earliest"))),
             note="Lord Howe Island falls back half an hour, from 02:00 to 01:30"),
        case("lord_howe_latest", dts(lord_howe), sel(in_utc(d.dt.replace_time_zone("Australia/Lord_Howe", ambiguous="latest")))),
        case("zoned_changes_zone", dts([datetime(2024, 1, 1, 12)], zone="UTC"), sel(in_utc(d.dt.replace_time_zone("Asia/Tokyo")))),
        case("zoned_made_naive", at([datetime(2024, 1, 1, 12)], "Asia/Tokyo"), sel(d.dt.replace_time_zone(None)),
             note="12:00 UTC reads 21:00 in Tokyo, and stays 21:00 naive"),
        case("fold_read_in_zone_without_one",
             at([datetime(2024, 11, 3, 4), datetime(2024, 11, 3, 5), datetime(2024, 11, 3, 6), datetime(2024, 11, 3, 7),
                 datetime(2024, 11, 3, 8)], "America/New_York"),
             sel(in_utc(d.dt.replace_time_zone(london))),
             note="New York reads 01:00 twice; London reads it once, in November at UTC"),
        case("same_zone_keeps_fold", at([datetime(2024, 10, 27, 0, 30), datetime(2024, 10, 27, 1, 30)], london),
             sel(d.dt.replace_time_zone(london)),
             note="both instants read 01:30 in London; kept in London, they stay two"),
        case("fold_in_another_zone", at([datetime(2024, 10, 27, 0, 30), datetime(2024, 10, 27, 1, 30)], london),
             sel(in_utc(d.dt.replace_time_zone("Europe/Paris"))),
             note="in Paris, where 01:30 reads once, they become one"),
        case("before_1970", dts([datetime(1900, 6, 1, 12)]), sel(in_utc(d.dt.replace_time_zone(london)))),
        case("nanoseconds", pl.DataFrame({"d": pl.Series("d", [1717243200_000000001], dtype=pl.Int64).cast(pl.Datetime("ns"))}),
             sel(in_utc(d.dt.replace_time_zone("America/New_York")))),
        case("keeps_unit_ms", dts([datetime(2024, 3, 31, 0, 30)], "ms"), sel(d.dt.replace_time_zone("Asia/Tokyo"))),
        # --- generated ---------------------------------------------------------------------
        case("replace_gen_naive", gen_naive, sel(
            in_utc(t.dt.replace_time_zone("America/New_York", ambiguous="earliest", non_existent="null")).alias("ny_earliest"),
            in_utc(t.dt.replace_time_zone("America/New_York", ambiguous="latest", non_existent="null")).alias("ny_latest"),
            in_utc(t.dt.replace_time_zone("Australia/Lord_Howe", ambiguous="null", non_existent="null")).alias("lord_howe_null"),
            in_utc(t.dt.replace_time_zone("Asia/Tokyo")).alias("tokyo"))),
        case("replace_gen_zoned", gen_ny, sel(
            in_utc(t.dt.replace_time_zone("Europe/Paris", ambiguous="earliest", non_existent="null")).alias("paris"),
            t.dt.replace_time_zone(None).alias("naive"))),
        case("replace_gen_ms", gen_ms, sel(t.dt.replace_time_zone("Asia/Kolkata"))),
    ]
