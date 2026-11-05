"""The castedges_byhand group: three temporal edges, audit.md's S18 to S20
(castedges_byhand_test.go). An Int64 outside a day is null as a lossy Time; a
Duration cast to a coarser unit truncates toward zero where a Datetime floors; abs
and neg of a Duration. Every literal frame is the Go test's, a generated case beside
each.

Not here, with the reason: seconds, a unit Polars has for neither a Duration nor a
Datetime (the Go test holds Duration(ms) → Duration(s) and Datetime(ms) →
Datetime(s) to Go's integer division and floor); the refusals (a strict Int64 past a
day, abs and neg of the minimum Duration, which Polars wraps), which the Go test keeps.
"""

from __future__ import annotations

from datetime import datetime

import polars as pl

from . import Case, case
from .gen import Gen

x = pl.col("x")
DAY_NS = 86_400_000_000_000
SECOND_NS = 1_000_000_000


def typed(vals: list, dtype: pl.DataType) -> pl.DataFrame:
    return pl.DataFrame({"x": pl.Series("x", vals, dtype=dtype)})


def durations(vals: list, unit: str) -> pl.DataFrame:
    return typed(vals, pl.Int64).select(x.cast(pl.Duration(unit)))


def sel(*e: pl.Expr):
    return lambda df: df.select(*e)


def cases() -> list[Case]:
    g = Gen(4973)
    gen_ticks = g.frame(g.ints(150, pl.Int64, lo=-2 * DAY_NS, hi=2 * DAY_NS))
    gen_dur_ns = g.frame(g.durations(150, "ns", name="x"))
    gen_dur_us = g.frame(g.durations(150, "us", name="x"))
    gen_dt_us = g.frame(g.datetimes(150, "us", name="x"))
    return [
        # --- S18: a Time holds a day --------------------------------------------------
        case("i64_to_time_lossy", typed([90000 * SECOND_NS, -1, 3600 * SECOND_NS], pl.Int64), sel(x.cast(pl.Time, strict=False)),
             note="90000 seconds is past a day, and so null, not 01:00"),
        case("i64_to_time_inside", typed([0, DAY_NS - 1], pl.Int64), sel(x.cast(pl.Time))),
        case("datetime_to_time", typed([datetime(2024, 3, 5, 1)], pl.Datetime("us")), sel(x.cast(pl.Time))),
        case("i64_to_time_gen", gen_ticks, sel(x.cast(pl.Time, strict=False))),
        case("datetime_to_time_gen", gen_dt_us, sel(x.cast(pl.Time))),
        # --- S19: a coarser Duration truncates, a coarser Datetime floors ---------------
        case("dur_us_to_ms_truncates", durations([-1500, 1500, -2000], "us"), sel(x.cast(pl.Duration("ms")))),
        case("dur_ns_to_us_truncates", durations([-1500, 999], "ns"), sel(x.cast(pl.Duration("us")))),
        case("dt_us_to_ms_floors", typed([-1500, 1500], pl.Int64), sel(x.cast(pl.Datetime("us")).cast(pl.Datetime("ms")))),
        case("dur_coarser_gen", gen_dur_ns, sel(x.cast(pl.Duration("us")).alias("us"), x.cast(pl.Duration("ms")).alias("ms"))),
        case("dt_coarser_gen", gen_dt_us, sel(x.cast(pl.Datetime("ms")).alias("ms"), x.cast(pl.Datetime("ns")).alias("ns"))),
        # --- S20: abs and neg of a Duration -------------------------------------------
        case("abs_neg_duration", durations([-SECOND_NS], "ns"), sel(x.abs().alias("a"), (-x).alias("n"))),
        case("abs_min_i64_wraps", typed([-(1 << 63)], pl.Int64), sel(x.abs()),
             note="integer arithmetic wraps, in ursus as in Polars; only temporal arithmetic is checked"),
        case("abs_neg_duration_gen", gen_dur_us, sel(x.abs().alias("a"), (-x).alias("n"))),
    ]
