"""The framenulls group: a fill strategy's limit, drop_nans and interpolate over a frame
(framenulls_test.go, C4 of v0.6.7-scope.md, step 301), and interpolate of the temporal
types, on their ticks in integers. Not here: the refusals, the strategies' names, the
plans, the eager and spilled differentials.
"""

from __future__ import annotations

from datetime import date, datetime, time

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
nan = float("nan")
LF = pl.DataFrame({"i": [1, None, None, None, 5], "f": [1.0, nan, None, 4.0, nan], "s": ["a", None, None, "d", None], "g": [nan, 2.0, 3.0, None, 5.0]})
us = lambda *a: datetime(*a)  # noqa: E731
TEMPORAL = pl.DataFrame({
    "tm": [time(1, 0), None, None, time(2, 0)],
    "t": pl.Series([us(2026, 1, 1), None, None, us(2026, 1, 1, 0, 0, 0, 10)], dtype=pl.Datetime("us")),
    "d": [date(2026, 1, 1), None, None, date(2026, 1, 2)],
    "neg": [date(1969, 12, 30), None, date(1969, 12, 31), None],
    "u": pl.Series([3_600_000_000_000, None, None, 7_200_000_000_000], dtype=pl.Int64).cast(pl.Duration("ns")),
    "b": [True, None, None, False],
    "i128": pl.Series([1, None, None, 4], dtype=pl.Int128),
})


def cases() -> list[Case]:
    g = Gen(5026)
    gen = g.frame(g.floats(150, null_rate=0.3, name="f"), g.ints(150, pl.Int64, null_rate=0.3, lo=-1000, hi=1000, edges=False, name="i"),
                  g.strings(150, null_rate=0.3, name="s"))
    return [
        case("forward_limit_1", LF, lambda df: df.fill_null(strategy="forward", limit=1), note="NaN is a value, and carried"),
        case("backward_limit_2", LF, lambda df: df.fill_null(strategy="backward", limit=2)),
        case("forward_no_limit", LF, lambda df: df.fill_null(strategy="forward")),
        case("forward_of_one_column_limit_1", LF, lambda df: df.with_columns(c("i").fill_null(strategy="forward", limit=1)).select("i")),
        case("an_expressions_limit_1", LF, lambda df: df.select(c("i").fill_null(strategy="forward", limit=1))),
        case("a_values_strategy_limit_2", LF, lambda df: df.select(c("i").fill_null(strategy="backward", limit=2))),
        case("drop_nans", LF, lambda df: df.drop_nans(), note="every float column; a null is kept"),
        case("drop_nans_of_f", LF, lambda df: df.drop_nans("f")),
        case("drop_nans_of_i", LF, lambda df: df.drop_nans("i").select("i"), note="an integer holds no NaN, and drops nothing"),
        case("interpolate", LF, lambda df: df.interpolate(), note="an integer becomes a Float64, text stays, a line to a NaN is NaN"),
        case("interpolate_temporal", TEMPORAL, lambda df: df.interpolate(),
             note="ticks in integers, the quotient toward zero: between day -2 and day -1 the middle is day -2; an Int128 a Float64, a Boolean as it is"),
        case("fill_limits_gen", gen, lambda df: df.select(c("f").fill_null(strategy="forward", limit=2).alias("ff2"),
                                                          c("f").fill_null(strategy="backward", limit=1).alias("bf1"),
                                                          c("i").fill_null(strategy="forward").alias("ff"),
                                                          c("s").fill_null(strategy="backward", limit=3).alias("bf3"))),
        case("frame_fill_gen", gen, lambda df: df.fill_null(strategy="backward", limit=2)),
        case("interpolate_gen", gen.drop("s"), lambda df: df.interpolate(), float="rel:1e-12",
             note="a line between two values over several rows: the formula's rounding, ulps apart"),
        case("drop_nans_gen", gen, lambda df: df.drop_nans()),
    ]
