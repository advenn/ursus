"""The weeks group: a grid of weeks starts on a Monday, "7d" counts from the epoch, a
Thursday (weeks_test.go, step 184): truncate and round by "1w", "2w" and "7d", a
Date's week, a week in London, and group_by_dynamic's windows of each.

Not here: Every's rendering, an ursus spelling the Go test keeps.
"""

from __future__ import annotations

from datetime import datetime

import numpy as np
import polars as pl

from . import Case, case
from .dtmore import dates, dts, sel
from .gen import Gen

d, t = pl.col("d"), pl.col("t")

WEEK = dts([datetime(1970, 1, 1), datetime(1970, 1, 8, 5), datetime(2024, 1, 3), datetime(2024, 1, 1, 12)])
GRID = pl.DataFrame({"t": pl.Series([datetime(2024, 1, 3), datetime(2024, 1, 9)], dtype=pl.Datetime("us")),
                     "v": pl.Series([1, 2], dtype=pl.Int64)})


def cases() -> list[Case]:
    g = Gen(4988)
    gen_naive = g.frame(g.datetimes(200, "us", name="t"))
    gen_dates = g.frame(g.dates(200))
    # Ten weeks of 2024, 150 rows, so every window holds a row: ursus makes every window
    # of the grid, where Polars leaves an empty one out (feature-gaps.md §9).
    base = int(np.datetime64("2024-02-05T00:00:00", "us").astype(np.int64))
    ticks = base + g.rng.integers(0, 70 * 86_400_000_000, 150, dtype=np.int64)
    gen_grid = pl.DataFrame({"t": pl.Series("t", sorted(int(v) for v in ticks), dtype=pl.Int64).cast(pl.Datetime("us")),
                             "v": g.ints(150, lo=-100, hi=100, null_rate=0.1, edges=False, name="v")})
    return [
        case("truncate_1w", WEEK, sel(d.dt.truncate("1w"))),
        case("truncate_7d_from_the_epoch", WEEK, sel(d.dt.truncate("7d"))),
        case("truncate_2w", WEEK.head(3), sel(d.dt.truncate("2w"))),
        case("round_1w", WEEK, sel(d.dt.round("1w"))),
        case("date_week", dates([(1970, 1, 1), (2024, 1, 3)]), sel(d.dt.truncate("1w"))),
        case("week_in_london", dts([datetime(2024, 3, 27, 12)], zone="UTC").select(d.dt.convert_time_zone("Europe/London")),
             sel(d.dt.truncate("1w"))),
        case("group_by_dynamic_1w", GRID, lambda df: df.group_by_dynamic("t", every="1w").agg(pl.col("v").sum())),
        case("group_by_dynamic_7d", GRID, lambda df: df.group_by_dynamic("t", every="7d").agg(pl.col("v").sum())),
        # --- generated ---------------------------------------------------------------------
        case("weeks_gen", gen_naive, sel(
            t.dt.truncate("1w").alias("w1"), t.dt.truncate("2w").alias("w2"), t.dt.truncate("7d").alias("d7"),
            t.dt.round("1w").alias("r1w"), t.dt.round("7d").alias("r7d"))),
        case("date_weeks_gen", gen_dates, sel(d.dt.truncate("1w").alias("w1"), d.dt.truncate("2w").alias("w2"), d.dt.round("1w").alias("r1w"))),
        case("group_by_dynamic_gen", gen_grid, lambda df: df.group_by_dynamic("t", every="1w").agg(pl.col("v").sum())),
    ]
