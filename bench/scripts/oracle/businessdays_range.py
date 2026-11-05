"""The businessdays_range group: add_business_days on a Date and on a Datetime of each
unit (businessdays_range_test.go, step 419): five business days from a Monday land on
the next Monday.

Not here: a count past any Date or Datetime, which Polars answers null and ursus
refuses (D10; the Go test keeps the refusal).
"""

from __future__ import annotations

from datetime import date, datetime

import polars as pl

from . import Case, case
from .dtmore import sel
from .gen import Gen

c = pl.col

MON = pl.DataFrame({"t": pl.Series([datetime(2024, 1, 1)], dtype=pl.Datetime("ns"))})


def cases() -> list[Case]:
    g = Gen(4992)
    n = 200
    gen = g.frame(g.dates(n, date(2000, 1, 1), date(2050, 12, 31), edges=False, name="d"),
                  g.ints(n, lo=-500, hi=500, edges=False, name="n"))
    return [
        case("add_5_date", MON, sel(c("t").cast(pl.Date).dt.add_business_days(5))),
        case("add_5_datetime_ns", MON, sel(c("t").dt.add_business_days(5))),
        case("add_5_datetime_ms", MON, sel(c("t").cast(pl.Datetime("ms")).dt.add_business_days(5))),
        # --- generated ---------------------------------------------------------------------
        case("add_gen", gen, sel(
            c("d").dt.add_business_days(c("n"), roll="forward").alias("date"),
            c("d").cast(pl.Datetime("ms")).dt.add_business_days(c("n"), roll="backward").alias("ms"),
            c("d").cast(pl.Datetime("ns")).dt.add_business_days(c("n"), roll="forward", week_mask=(True,) * 6 + (False,)).alias("ns"))),
    ]
