"""The components group: pl.date, pl.datetime, pl.time, pl.duration, pl.from_epoch and
business_day_count (components_test.go, G3 of v0.6.6-scope.md, step 265).

A query of literals alone is one row in Polars and a row for each of the frame's rows
here (feature-gaps.md §9), so such a case takes a frame of one row. Polars 2 names
pl.datetime of columns after its first input and pl.duration of columns `literal`,
where ursus names them `datetime` and `duration` (§11 and §9): the Go side names the
difference. Not here: the refusals, which the Go test keeps.
"""

from __future__ import annotations

from datetime import date

import polars as pl

from . import Case, case
from .dtmore import sel
from .gen import Gen

c = pl.col

DF = pl.DataFrame({
    "y": pl.Series([2024, 2024, None, 2024], dtype=pl.Int64), "m": pl.Series([2, 2, 1, 1], dtype=pl.Int64),
    "d": pl.Series([29, 3, 1, 1], dtype=pl.Int64), "h": pl.Series([1, 0, 0, 0], dtype=pl.Int64),
    "f": pl.Series([1.5, 0.25, None, -1.0], dtype=pl.Float64),
    "s": pl.Series([0, 1704067200, None, -86400], dtype=pl.Int64),
    "ms": pl.Series([0, 1704067200123, 1, -1], dtype=pl.Int64),
})
ONE = DF.head(1)
BD = pl.DataFrame({
    "s": pl.Series([date(2024, 1, 1), date(2024, 1, 6), date(2024, 1, 10), None], dtype=pl.Date),
    "e": pl.Series([date(2024, 1, 8), date(2024, 1, 8), date(2024, 1, 1), date(2024, 1, 2)], dtype=pl.Date),
})
MON_SAT = (True, True, True, True, True, True, False)


def cases() -> list[Case]:
    g = Gen(4983)
    n = 200
    gen_parts = g.frame(
        g.ints(n, lo=1, hi=9999, edges=False, name="y"), g.ints(n, lo=1, hi=12, edges=False, name="m"),
        g.ints(n, lo=1, hi=28, edges=False, name="d"), g.ints(n, lo=0, hi=23, edges=False, name="h"),
        g.ints(n, lo=0, hi=59, edges=False, name="mi"), g.ints(n, lo=0, hi=59, edges=False, name="sec"),
        g.ints(n, lo=0, hi=999_999, edges=False, name="us"))
    gen_dur = g.frame(g.ints(n, lo=-400, hi=400, edges=False, name="days"), g.floats(n, -100, 100, edges=False, name="hours"),
                      g.floats(n, -1e6, 1e6, edges=False, name="ms"))
    gen_epoch = g.frame(g.ints(n, lo=-10_000_000_000, hi=10_000_000_000, name="s"),
                        g.ints(n, lo=-(1 << 50), hi=1 << 50, name="ms"), g.floats(n, -1e9, 1e9, edges=False, name="f"))
    # The end on or after the start: where the end is before it, Polars counts minus the
    # business days in (end, start], numpy's rule, and ursus those in [end, start), its
    # doc's (feature-gaps §9); business_day_count_reversed below asks it so (step 507).
    a = g.dates(n, date(2020, 1, 1), date(2030, 12, 31), edges=False, name="s").to_list()
    b = g.dates(n, date(2020, 1, 1), date(2030, 12, 31), edges=False, name="e").to_list()
    gen_bd = pl.DataFrame({"s": pl.Series("s", [None if None in (x, y) else min(x, y) for x, y in zip(a, b)], dtype=pl.Date),
                           "e": pl.Series("e", [None if None in (x, y) else max(x, y) for x, y in zip(a, b)], dtype=pl.Date)})
    winter = [date(2024, 12, 25), date(2025, 1, 1)]
    reversed_ends = pl.DataFrame({
        "s": pl.Series([date(2024, 1, 8), date(2024, 1, 6), date(2024, 1, 7), date(2024, 1, 9), date(2024, 1, 8), date(2024, 1, 13),
                        date(2024, 1, 10), date(2024, 1, 8)], dtype=pl.Date),
        "e": pl.Series([date(2024, 1, 6), date(2024, 1, 8), date(2024, 1, 5), date(2024, 1, 8), date(2024, 1, 5), date(2024, 1, 6),
                        date(2024, 1, 1), date(2024, 1, 8)], dtype=pl.Date)})
    return [
        case("date", DF, sel(pl.date("y", "m", "d"))),
        case("date_int8_month", DF, sel(pl.date("y", c("m").cast(pl.Int8), 3))),
        case("datetime", DF, sel(pl.datetime("y", "m", "d", "h", 30, 15, 250)),
             note="Polars 2 names it after its first input, y; ursus names it datetime"),
        case("time", DF, sel(pl.time("h", 5, 7, 250))),
        case("duration_every_part", ONE, sel(pl.duration(weeks=1, days=2, hours=3, minutes=4, seconds=5, milliseconds=6, microseconds=7))),
        case("duration_nanoseconds", ONE, sel(pl.duration(nanoseconds=5))),
        case("duration_in_milliseconds", ONE, sel(pl.duration(seconds=1, time_unit="ms"))),
        case("duration_float_hours", DF, sel(pl.duration(hours=c("f"))), note="Polars names a duration of columns literal; ursus duration"),
        case("duration_of_columns", DF, sel(pl.duration(days=c("d"), hours=c("h")))),
        case("duration_negative", ONE, sel(pl.duration(days=-1, hours=2))),
        case("duration_fractions_summed", ONE, sel(pl.duration(microseconds=0.6, milliseconds=0.0004)),
             note="fractions summed, then truncated"),
        case("duration_toward_zero", ONE, sel(pl.duration(microseconds=-0.6))),
        case("duration_no_part", ONE, sel(pl.duration())),
        case("from_epoch_seconds", DF, sel(pl.from_epoch("s", "s"))),
        case("from_epoch_milliseconds", DF, sel(pl.from_epoch("ms", "ms"))),
        case("from_epoch_microseconds", DF, sel(pl.from_epoch("ms", "us"))),
        case("from_epoch_nanoseconds", DF, sel(pl.from_epoch("ms", "ns"))),
        case("from_epoch_float", ONE, sel(pl.from_epoch(pl.lit(1.0000009), "s")), note="truncated toward zero at the answer's unit"),
        case("from_epoch_float_negative", ONE, sel(pl.from_epoch(pl.lit(-1.0000009), "s"))),
        case("in_zone", ONE, sel(
            pl.datetime(2024, 3, 10, 12, 0, 0, 0).dt.replace_time_zone("America/New_York").dt.strftime("%H:%M %z").alias("a"),
            pl.datetime(2024, 11, 3, 1, 30, 0, 0).dt.replace_time_zone("America/New_York", ambiguous="earliest")
            .dt.strftime("%H:%M %z").alias("b")),
             note="01:30 on 2024-11-03 is read twice in New York, the earlier here"),
        case("business_day_count", BD, sel(pl.business_day_count("s", "e"))),
        case("business_day_count_mon_sat", BD, sel(pl.business_day_count("s", "e", week_mask=MON_SAT))),
        case("business_day_count_holidays", BD, sel(pl.business_day_count("s", "e", holidays=[date(2024, 1, 2), date(2024, 1, 6)]))),
        case("business_day_count_reversed", reversed_ends,
             sel(pl.when(c("e") < c("s")).then(-pl.business_day_count("e", "s")).otherwise(pl.business_day_count("s", "e"))
                 .alias("s")),
             note="spelled to ursus's documented rule (step 507): with the end before the start, BusinessDayCount is minus the "
                  "business days in [end, start), 0 from a Monday back to the Saturday before; Polars counts those in (end, start], "
                  "numpy's rule, -1 there, so it is asked minus the count from end to start"),
        # --- generated ---------------------------------------------------------------------
        case("components_gen", gen_parts, sel(
            pl.date("y", "m", "d").alias("date"), pl.datetime("y", "m", "d", "h", "mi", "sec", "us").alias("datetime"),
            pl.time("h", "mi", "sec", "us").alias("time"))),
        case("duration_gen", gen_dur, sel(
            pl.duration(days=c("days"), hours=c("hours")).alias("dh"),
            pl.duration(milliseconds=c("ms")).alias("ms"),
            pl.duration(days=c("days"), time_unit="ms").alias("d_ms"))),
        case("from_epoch_gen", gen_epoch, sel(
            pl.from_epoch("s", "s").alias("s"), pl.from_epoch("ms", "ms").alias("ms"),
            pl.from_epoch("ms", "us").alias("us"), pl.from_epoch("ms", "ns").alias("ns"), pl.from_epoch("f", "s").alias("f"))),
        case("business_day_count_gen", gen_bd, sel(
            pl.business_day_count("s", "e").alias("n"), pl.business_day_count("s", "e", week_mask=MON_SAT, holidays=winter).alias("n_ms"))),
    ]
