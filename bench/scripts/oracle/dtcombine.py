"""The dtcombine group: dt.days_in_month and dt.combine (dtcombine_test.go, step 230):
February in a leap year and not, a null, a Date and a Datetime; a Time of day joined to
each, a null on either side null; a Datetime keeping its millisecond unit, asked of
Polars with time_unit="ms"; February's days in New York.

Not here: the refusals (a skipped wall clock, a day past Datetime(ns)'s range, a Time
or an Int64 as the day), which the Go test keeps. Recorded and not asserted, a finding
for the maintainer: combine of a zoned Datetime, whose day Polars 2.1 reads in UTC,
where ursus reads it on the zone's wall clock (Combine's doc).
"""

from __future__ import annotations

from datetime import datetime, time

import numpy as np
import polars as pl

from . import Case, case
from .dtmore import sel
from .gen import Gen

c = pl.col

TS = pl.DataFrame({
    "ts": pl.Series([datetime(2024, 2, 10, 23), datetime(2023, 2, 1), datetime(2024, 12, 31, 12), None], dtype=pl.Datetime("us")),
    "t": pl.Series([time(3, 4, 5, 6000), time(0), None, time(1)], dtype=pl.Time),
}).with_columns(c("ts").dt.date().alias("d"), c("ts").cast(pl.Datetime("ms")).alias("ms"))


def ny(v: datetime) -> pl.DataFrame:
    return pl.DataFrame({"ts": pl.Series([v], dtype=pl.Datetime("ns")).dt.replace_time_zone("America/New_York")})


def cases() -> list[Case]:
    g = Gen(4989)
    n = 200
    tods = g.rng.integers(0, 86_400_000_000, n, dtype=np.int64) * 1000
    tm = pl.Series("t", g._place([int(v) for v in tods], 0.1, [0, 86_399_999_999_000]), dtype=pl.Int64).cast(pl.Time)
    gen = g.frame(g.dates(n, name="d"), g.datetimes(n, "us", name="us"), g.datetimes(n, "ms", name="ms"), tm)
    return [
        case("days_in_month", TS, sel(c("d").dt.days_in_month().alias("dim"), c("ts").dt.days_in_month().alias("dimts"))),
        case("combine_date", TS, sel(c("d").dt.combine(c("t")))),
        case("combine_datetime", TS, sel(c("ts").dt.combine(c("t")))),
        case("combine_datetime_ms", TS, sel(c("ms").dt.combine(c("t"), time_unit="ms")), note="a Datetime(ms) keeps its unit"),
        case("combine_zoned_ny", ny(datetime(2024, 3, 11, 22)), sel(c("ts").dt.combine(time(13, 15), time_unit="ns")),
             note="a finding: 22:00 on the 11th in New York is 02:00 on the 12th in UTC; Polars combines the 12th, ursus the 11th, "
                  "the day on New York's wall clock"),
        case("days_in_month_ny", ny(datetime(2024, 2, 29, 21)), sel(c("ts").dt.days_in_month()),
             note="February's 29 days in New York, at 21:00 on the 29th there, March 1 in UTC"),
        # --- generated ---------------------------------------------------------------------
        case("combine_gen", gen, sel(
            c("d").dt.days_in_month().alias("dim_d"), c("us").dt.days_in_month().alias("dim_us"),
            c("d").dt.combine(c("t")).alias("c_d"), c("us").dt.combine(c("t")).alias("c_us"),
            c("ms").dt.combine(c("t"), time_unit="ms").alias("c_ms"), c("d").dt.combine(time(13, 15)).alias("c_lit"))),
    ]
