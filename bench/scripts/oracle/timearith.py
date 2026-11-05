"""The timearith group: a Time minus a Time is a signed Duration (timearith_test.go,
step 236).

A Time plus or minus a Duration, the file's subject, Polars 2.1 refuses ("failed to
determine supertype of time and duration"), so the wrap at midnight at every unit is
held to Go's time package in the Go test; and Polars' Time is nanoseconds alone, so
the difference is recorded at that unit.
"""

from __future__ import annotations

from datetime import time

import polars as pl

from . import Case, case
from .dtmore import sel
from .gen import Gen

c = pl.col

AB = pl.DataFrame({"a": pl.Series([time(1), None, time(23)], dtype=pl.Time), "b": pl.Series([time(23), time(1), time(1)], dtype=pl.Time)})


def cases() -> list[Case]:
    g = Gen(4991)
    gen = g.frame(g.times(200, name="a"), g.times(200, name="b"))
    return [
        case("time_minus_time", AB, sel((c("a") - c("b")).alias("d")), note="01:00 minus 23:00 is minus twenty-two hours, not two"),
        case("time_minus_time_gen", gen, sel((c("a") - c("b")).alias("d"), (c("b") - c("a")).alias("back"))),
    ]
