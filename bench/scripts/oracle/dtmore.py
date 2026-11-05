"""The dtmore group: iso_year, century and millennium, the business days, dt.replace,
the zone offsets, and a Duration's totals, the fractional second and the epochs
(dtmore_test.go, D of v0.6.6-scope.md). Datetimes of us, ms and ns, naive and zoned.

Not here, with the reason: with_time_unit is gone from Polars 2 (the Go test holds it
as a tick differential); Dublin's and Casablanca's offsets depend on the zone
database's form, main or rearguard, which Ubuntu's CI and Debian differ on (the Go
test holds their sum to Go's own zone reading); a gap made null by NonExistentNull has
no Polars 2 spelling (dt.replace lost non_existent; Go's time package is the oracle).
"""

from __future__ import annotations

from datetime import date, datetime, timedelta

import polars as pl

from . import Case, case
from .gen import Gen, days_from_civil

d = pl.col("d")


def dates(ymd: list[tuple[int, int, int] | None], name: str = "d") -> pl.DataFrame:
    """A Date column from year-month-day triples, year 0 and before included."""
    days = [None if v is None else days_from_civil(*v) for v in ymd]
    return pl.DataFrame({name: pl.Series(name, days, dtype=pl.Int32).cast(pl.Date)})


def dts(vals: list[datetime | None], unit: str = "us", zone: str | None = None, name: str = "d") -> pl.DataFrame:
    s = pl.Series(name, vals, dtype=pl.Datetime(unit))
    if zone:
        s = s.dt.replace_time_zone(zone)
    return pl.DataFrame({name: s})


def sel(*e: pl.Expr):
    return lambda df: df.select(*e)


ISO = dates([(2024, 12, 30), (2021, 1, 1), (1, 1, 1), (2000, 6, 15), (2001, 1, 1), None, (100, 1, 1)])
YEARS = dates([(y, 1, 1) for y in (-1101, -1100, -1099, -1001, -1000, -999, -201, -200, -199, -101, -100, -99, -1, 0, 1,
                                   99, 100, 101, 999, 1000, 1001, 1999, 2000, 2001)])
BD = dates([(2024, 1, 5), (2024, 1, 6), (2024, 1, 7), None, (2024, 12, 25)])
CHRISTMAS = [date(2024, 12, 25)]
MON_SAT = (True, True, True, True, True, True, False)
WINTER = [date(2024, 12, 25), date(2025, 1, 1)]
THU_OFF = (True, True, True, False, True, True, False)
ALL7 = (True,) * 7

REPL = dts([datetime(2024, 1, 31, 10, 20, 30, 123456), datetime(2023, 2, 28), None]).with_columns(
    pl.Series("day", [1, None, 3], dtype=pl.Int64))
NS = pl.DataFrame({"d": pl.Series("d", [1704067201123456789], dtype=pl.Int64).cast(pl.Datetime("ns"))})
NY_FOLD = dts([datetime(2024, 11, 3, 0, 30)], zone="America/New_York")

US = [1_000_500, -86_399_999_999, None, 1, 3, 7, 999_999, 123_456_789, -1, 1_000_000_000_001]
DU = pl.DataFrame({"d": pl.Series("d", US, dtype=pl.Int64).cast(pl.Duration("us"))})
SEC = dts([datetime(2024, 1, 1, 0, 0, 1, 500), datetime(2024, 1, 1, 0, 0, 59, 999999), None])
NANOS = [123456789, 999999999, 1, 500000000, 100000001, 333333333, 0]
SEC_NS = pl.DataFrame({"d": pl.Series("d", [1704067260_000000000 + n for n in NANOS], dtype=pl.Int64).cast(pl.Datetime("ns"))})
TM = pl.DataFrame({"d": pl.Series("d", [3_723_250_000_000], dtype=pl.Int64).cast(pl.Time)})
EP = dts([datetime(2024, 1, 1, 0, 0, 1, 500), datetime(1969, 12, 31, 23, 59, 59, 999999), None])
EP_DATES = dates([(2024, 1, 2), (1969, 12, 31)])
TOKYO = dts([datetime(2024, 1, 1)], zone="Asia/Tokyo")


def zoned_at(zone: str) -> pl.DataFrame:
    return dts([datetime(2024, 1, 15, 12), datetime(2024, 7, 15, 12), None], zone=zone)


def epochs(e: pl.Expr) -> list[pl.Expr]:
    return [e.dt.epoch("ns").alias("ns"), e.dt.epoch("us").alias("us"), e.dt.epoch("ms").alias("ms"),
            e.dt.epoch("s").alias("s"), e.dt.epoch("d").alias("days")]


def totals(e: pl.Expr) -> list[pl.Expr]:
    return [e.dt.total_milliseconds().alias("ms"), e.dt.total_microseconds().alias("us"),
            e.dt.total_nanoseconds().alias("ns")]


def fractional(e: pl.Expr) -> list[pl.Expr]:
    return [e.dt.total_days(fractional=True).alias("days"), e.dt.total_hours(fractional=True).alias("hours"),
            e.dt.total_minutes(fractional=True).alias("minutes"), e.dt.total_seconds(fractional=True).alias("seconds"),
            e.dt.total_milliseconds(fractional=True).alias("ms"), e.dt.total_microseconds(fractional=True).alias("us"),
            e.dt.total_nanoseconds(fractional=True).alias("ns")]


def cases() -> list[Case]:
    g = Gen(4963)
    gen_dates = g.frame(g.dates(200))
    gen_bdays = g.frame(g.dates(200, edges=False))
    gen_dt = g.frame(g.datetimes(200, "us"))
    gen_ny = g.frame(g.datetimes(120, "us", "America/New_York", lo=2023, hi=2025))
    gen_lh = g.frame(g.datetimes(120, "ms", "Australia/Lord_Howe", lo=2023, hi=2025))
    gen_du = g.frame(g.durations(200, "us"))
    gen_du_ms = g.frame(g.durations(100, "ms"))
    gen_ep = g.frame(g.datetimes(200, "us"), g.datetimes(200, "ns", "Asia/Tokyo", name="tz"))
    return [
        # --- iso_year, century, millennium ------------------------------------------
        case("iso_century_millennium", ISO,
             sel(d.dt.iso_year().alias("iso"), d.dt.century().alias("c"), d.dt.millennium().alias("m"))),
        case("century_millennium_around_year_0", YEARS, sel(d.dt.century().alias("c"), d.dt.millennium().alias("m")),
             note="year 0 is in no century; the centuries before it count down from -1"),
        case("iso_year_datetime", dts([datetime(2024, 12, 30, 5)]), sel(d.dt.iso_year())),
        case("iso_century_millennium_gen", gen_dates,
             sel(d.dt.iso_year().alias("iso"), d.dt.century().alias("c"), d.dt.millennium().alias("m"))),
        # --- business days ------------------------------------------------------------
        case("is_business_day", BD, sel(d.dt.is_business_day())),
        case("is_business_day_holiday", BD, sel(d.dt.is_business_day(holidays=CHRISTMAS))),
        case("is_business_day_mon_sat", BD, sel(d.dt.is_business_day(week_mask=MON_SAT))),
        case("add_1_forward", BD, sel(d.dt.add_business_days(1, roll="forward"))),
        case("add_1_backward", BD, sel(d.dt.add_business_days(1, roll="backward"))),
        case("add_minus1_forward", BD, sel(d.dt.add_business_days(-1, roll="forward"))),
        case("add_0_forward", BD, sel(d.dt.add_business_days(0, roll="forward"))),
        case("add_0_backward", BD, sel(d.dt.add_business_days(0, roll="backward"))),
        case("add_count_per_row", BD, sel(d.dt.add_business_days(pl.lit(1) + pl.int_range(0, pl.len()), roll="forward")),
             note="Christmas and five: New Year's Day"),
        case("add_null_count", pl.DataFrame({"d": pl.Series([date(2024, 1, 5)] * 2, dtype=pl.Date),
                                             "n": pl.Series([1, None], dtype=pl.Int64)}),
             sel(d.dt.add_business_days(pl.col("n"), roll="raise"))),
        case("add_1_past_christmas", dates([(2024, 12, 24)]), sel(d.dt.add_business_days(1, holidays=CHRISTMAS))),
        case("add_1000", dates([(2024, 1, 1)]), sel(d.dt.add_business_days(1000))),
        case("add_minus1000", dates([(2024, 1, 1)]), sel(d.dt.add_business_days(-1000))),
        case("add_1_datetime", dts([datetime(2024, 1, 5, 13, 30)]), sel(d.dt.add_business_days(1)),
             note="a Datetime keeps its time of day"),
        case("is_business_day_saturday_datetime", dts([datetime(2024, 1, 6, 13, 30)]), sel(d.dt.is_business_day())),
        case("add_1_zoned", dts([datetime(2024, 1, 5, 23, 30)], zone="Asia/Tokyo"), sel(d.dt.add_business_days(1)),
             note="23:30 on a Friday in Tokyo is 14:30 UTC: its local date moves, to Monday"),
        case("add_10_over_two_holidays", dates([(2024, 12, 20)]), sel(d.dt.add_business_days(10, holidays=WINTER))),
        case("add_minus10_over_two_holidays", dates([(2025, 1, 7)]), sel(d.dt.add_business_days(-10, holidays=WINTER))),
        case("add_23_thursdays_off", dates([(2024, 12, 20)]),
             sel(d.dt.add_business_days(23, week_mask=THU_OFF, holidays=WINTER))),
        case("business_days_gen", gen_bdays, sel(
            d.dt.is_business_day().alias("is"), d.dt.is_business_day(week_mask=MON_SAT, holidays=WINTER).alias("is_ms"),
            d.dt.add_business_days(3, roll="forward", holidays=WINTER).alias("add3"),
            d.dt.add_business_days(-2, roll="backward", week_mask=MON_SAT).alias("sub2"))),
        # --- dt.replace ---------------------------------------------------------------
        case("replace_hour_minute", REPL, sel(d.dt.replace(hour=23, minute=0))),
        case("replace_microsecond", REPL, sel(d.dt.replace(microsecond=5))),
        case("replace_day_per_row", REPL, sel(d.dt.replace(day=pl.col("day"))),
             note="a null in the column keeps the value's own day"),
        case("replace_year_0", REPL, sel(d.dt.replace(year=0, month=3)), note="year 0 is a leap year, as Polars' calendar has it"),
        case("replace_ns_hour", NS, sel(d.dt.replace(hour=5)), note="a Datetime(ns) keeps its nanoseconds"),
        case("replace_ns_microsecond", NS, sel(d.dt.replace(microsecond=5)), note="but where the microsecond is set"),
        case("replace_ms_microsecond", NS, sel(d.cast(pl.Datetime("ms")).dt.replace(microsecond=999999))),
        case("replace_date_day", dates([(2024, 1, 31)]), sel(d.dt.replace(day=2))),
        case("replace_date_hour_day", dates([(2024, 1, 31)]), sel(d.dt.replace(hour=4, day=2)),
             note="a Date's time is not read"),
        case("replace_fold_earliest", NY_FOLD, sel(d.dt.replace(hour=1, ambiguous="earliest")),
             note="in New York 01:30 on the 3rd of November is read twice"),
        case("replace_fold_latest", NY_FOLD, sel(d.dt.replace(hour=1, ambiguous="latest"))),
        case("replace_fold_null", NY_FOLD, sel(d.dt.replace(hour=1, ambiguous="null"))),
        case("replace_gen", gen_dt, lambda df: df.select(
            pl.col("t").dt.replace(hour=23, second=0).alias("h23"), pl.col("t").dt.replace(microsecond=1).alias("us1"),
            pl.col("t").dt.replace(month=6, day=15).alias("jun15"))),
        # --- the zone offsets ---------------------------------------------------------
        case("offsets_new_york", zoned_at("America/New_York"),
             sel(d.dt.base_utc_offset().alias("b"), d.dt.dst_offset().alias("o"))),
        case("offsets_lord_howe", zoned_at("Australia/Lord_Howe"),
             sel(d.dt.base_utc_offset().alias("b"), d.dt.dst_offset().alias("o")), note="half an hour of daylight saving"),
        case("offsets_kolkata", zoned_at("Asia/Kolkata"), sel(d.dt.base_utc_offset().alias("b"), d.dt.dst_offset().alias("o"))),
        case("offsets_utc", zoned_at("UTC"), sel(d.dt.base_utc_offset().alias("b"), d.dt.dst_offset().alias("o"))),
        case("offsets_gen_new_york", gen_ny, lambda df: df.select(
            pl.col("t").dt.base_utc_offset().alias("b"), pl.col("t").dt.dst_offset().alias("o"))),
        case("offsets_gen_lord_howe", gen_lh, lambda df: df.select(
            pl.col("t").dt.base_utc_offset().alias("b"), pl.col("t").dt.dst_offset().alias("o"))),
        # --- totals, the fractional second, the epochs ---------------------------------
        case("duration_totals", DU, sel(*totals(d))),
        case("duration_totals_fractional", DU, sel(*fractional(d)), note="every float to the bit"),
        case("duration_ms_fractional", DU.head(2), sel(
            d.cast(pl.Duration("ms")).dt.total_seconds(fractional=True).alias("s"),
            d.cast(pl.Duration("ms")).dt.total_nanoseconds().alias("ns")),
             note="a Duration(ms): a millisecond's reciprocal, and nanoseconds by multiplying"),
        case("second_fractional", SEC, sel(d.dt.second(fractional=True))),
        case("second_fractional_ns", SEC_NS, sel(d.dt.second(fractional=True))),
        case("second_fractional_time", TM, sel(d.dt.second(fractional=True))),
        case("epochs", EP, sel(*epochs(d), d.dt.timestamp("ms").alias("ts")),
             note="floored: a microsecond before 1970 is -1 in every coarser unit"),
        case("epochs_dates", EP_DATES, sel(d.dt.epoch("ns").alias("ns"), d.dt.epoch("s").alias("s"), d.dt.epoch("d").alias("days"))),
        case("epoch_zoned", TOKYO, sel(d.dt.epoch("s"))),
        case("duration_totals_gen", gen_du, lambda df: df.select(
            *totals(pl.col("du")), *[e.name.suffix("_f") for e in fractional(pl.col("du"))])),
        case("duration_ms_totals_gen", gen_du_ms, lambda df: df.select(
            pl.col("du").dt.total_seconds(fractional=True).alias("s"), pl.col("du").dt.total_nanoseconds().alias("ns"),
            pl.col("du").dt.total_days(fractional=True).alias("days"))),
        case("epochs_gen", gen_ep, lambda df: df.select(
            *epochs(pl.col("t")), pl.col("t").dt.timestamp("ns").alias("ts"), pl.col("t").dt.second(fractional=True).alias("sec"),
            pl.col("tz").dt.epoch("s").alias("tz_s"), pl.col("tz").dt.epoch("ns").alias("tz_ns"))),
    ]
