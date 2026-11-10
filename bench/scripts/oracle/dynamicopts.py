"""The dynamicopts group: GroupByDynamic's Label, StartBy, Offset, Period and
IncludeBoundaries (dynamicopts_test.go, step 187). ursus keeps a dynamic group's empty
windows, where Polars drops them (feature-gaps.md §9), so the Go side filters its
windows on a count above zero; the sum is cast to Int64 on both sides, ursus's being
an Int128. Not here: the refusals.
"""

from __future__ import annotations

from datetime import datetime, timedelta

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col


def at(m: int, d: int, h: int, mi: int) -> datetime:
    return datetime(2024, m, d, h, mi)


def frame(ts: list[datetime]) -> pl.DataFrame:
    return pl.DataFrame({"t": pl.Series(ts, dtype=pl.Datetime("us")), "v": pl.Series(range(1, len(ts) + 1), dtype=pl.Int64)})


HOURLY = frame([at(1, 1, 0, 30), at(1, 1, 1, 10), at(1, 1, 2, 45), at(1, 3, 9, 0)])
WEEKLY = frame([at(1, 4, 10, 30), at(1, 9, 1, 10), at(1, 20, 2, 45)])


def dyn(**o):
    return lambda df: df.group_by_dynamic("t", **o).agg(c("v").sum().cast(pl.Int64))


def cases() -> list[Case]:
    g = Gen(5029)
    minutes = sorted(int(v) for v in g.rng.integers(0, 30 * 24 * 60, 120))
    gen = frame([datetime(2024, 2, 1) + timedelta(minutes=m) for m in minutes])
    return [
        case("label_left", HOURLY, dyn(every="1h")),
        case("label_right", HOURLY, dyn(every="1h", label="right")),
        case("label_datapoint", HOURLY, dyn(every="1h", label="datapoint")),
        case("boundaries", HOURLY, dyn(every="1h", include_boundaries=True)),
        case("period_2h_right_boundaries", HOURLY, dyn(every="1h", period="2h", label="right", include_boundaries=True)),
        case("start_by_datapoint_hourly", HOURLY, dyn(every="1h", start_by="datapoint")),
        case("weeks_from_monday", WEEKLY, dyn(every="1w")),
        case("weeks_from_wednesday", WEEKLY, dyn(every="1w", start_by="wednesday")),
        case("weeks_from_sunday", WEEKLY, dyn(every="1w", start_by="sunday")),
        case("fortnights_from_friday", WEEKLY, dyn(every="2w", start_by="friday")),
        case("weeks_from_the_first_row", WEEKLY, dyn(every="1w", start_by="datapoint")),
        case("weeks_from_wednesday_offset_3h", WEEKLY, dyn(every="1w", start_by="wednesday", offset="3h")),
        case("days_from_the_first_row_offset_ignored", WEEKLY, dyn(every="1d", start_by="datapoint", offset="2h")),
        case("days_from_the_first_row_right", WEEKLY, dyn(every="1d", start_by="datapoint", label="right")),
        case("dynamic_opts_gen", gen, dyn(every="1d", period="3d", label="right", include_boundaries=True),
             note="120 instants over a month; windows of no rows left out on both sides"),
        case("dynamic_opts_gen_weeks", gen, dyn(every="1w", start_by="thursday", offset="-6h", closed="right")),
    ]
