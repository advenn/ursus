"""The upsample group: Polars' upsample, a row at each step from each group's first
instant to its last (upsample_test.go, P4 of v0.6.7-scope.md, step 306), with
maintain_order=True where there are groups, which is the order here. Upsample takes a
frame unsorted and leaves out a row of a null instant, both of which Polars refuses
(feature-gaps.md §9): those two cases ask Polars of the frame sorted, or without the
null row. Not here: a frame of no rows, which Polars refuses and which answers none
here; the refusals; the spilled and threaded differentials. The integer index cases
are the intindex group's.
"""

from __future__ import annotations

from datetime import date, datetime, timedelta

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col


def tm(ts: list, **cols) -> pl.DataFrame:
    return pl.DataFrame({"t": pl.Series("t", ts, dtype=pl.Datetime("us")), **cols})


def at(m: int, d: int, h: int = 0) -> datetime:
    return datetime(2024, m, d, h)


THREE = tm([at(1, 1), at(1, 3), at(1, 4)], v=[1, 2, 3])
GROUPED = tm([at(1, 2), at(1, 4), at(1, 1), at(1, 3)], g=["b", "b", "a", "a"], v=[3, 4, 1, 2])


def ams(df: pl.DataFrame) -> pl.DataFrame:
    return df.with_columns(c("t").dt.replace_time_zone("Europe/Amsterdam"))


def up(every: str, *group_by: str):
    if group_by:
        return lambda df: df.upsample("t", every=every, group_by=list(group_by), maintain_order=True)
    return lambda df: df.upsample("t", every=every)


def cases() -> list[Case]:
    g = Gen(5019)
    # 60 instants on a six-hour grid over ten days, three groups, in no order; v has
    # no null and no NaN, so a sort on t, g and v is total, and both sides sort first.
    steps = g.rng.integers(0, 40, 60)
    gen = pl.DataFrame({
        "t": pl.Series([datetime(2024, 5, 1) + timedelta(hours=6 * int(s)) for s in steps], dtype=pl.Datetime("us")),
        "g": pl.Series([["p", "q", "r"][int(k)] for k in g.rng.integers(0, 3, 60)], dtype=pl.String),
        "v": g.floats(60, null_rate=0, edges=False, name="v"),
    })
    return [
        case("daily", THREE, up("1d")),
        case("every_12h", THREE, up("12h")),
        case("every_12h_duration", THREE, up("12h"), note="the step as a Go time.Duration"),
        case("every_2d", THREE, up("2d"), note="the grid is Jan 1 and 3: Jan 4 falls between, and is left out"),
        case("a_row_between_two_steps", tm([at(1, 1), at(1, 2, 12), at(1, 4)], v=[1, 2, 3]), up("1d")),
        case("two_rows_at_one_instant", tm([at(1, 1), at(1, 1), at(1, 3)], v=[1, 2, 3]), up("1d")),
        case("monthly_from_jan_31", tm([at(1, 31), at(4, 30)], v=[1, 2]), up("1mo")),
        case("grouped", GROUPED, up("1d", "g"), note="maintain_order: b first, as it first appears"),
        case("a_null_key", tm([at(1, 1), at(1, 3)], g=pl.Series([None, None], dtype=pl.String), v=[1, 2]), up("1d", "g")),
        case("a_date", pl.DataFrame({"t": [date(2024, 1, 1), date(2024, 1, 3)], "v": [1, 2]}), up("1d")),
        case("days_in_a_zone", ams(tm([at(3, 30), at(4, 1)], v=[1, 2])), up("1d"),
             note="a day of Amsterdam's wall clock, 23 hours over the change to summer time"),
        case("hours_in_a_zone", ams(tm([at(3, 31, 1), at(3, 31, 4)], v=[1, 2])), up("1h"), note="02:00 is not: 01:00, 03:00, 04:00"),
        case("one_row", THREE.head(1), up("1d")),
        case("unsorted", tm([at(1, 3), at(1, 1)], v=[2, 1]), lambda df: df.sort("t").upsample("t", every="1d"),
             note="Polars refuses a frame not sorted by its time column, which this sorts (§9): asked of the frame sorted"),
        case("a_null_instant", tm([at(1, 1), None, at(1, 3)], v=[1, 2, 3]), lambda df: df.drop_nulls("t").upsample("t", every="1d"),
             note="Polars refuses a null instant, whose row has no step here (§9): asked without it"),
        case("upsample_gen", gen, lambda df: df.sort("t", "g", "v").upsample("t", every="6h", group_by="g", maintain_order=True),
             note="sorted on every column on both sides, so the groups' first appearances agree; repeats at one instant, gaps of several steps"),
        case("upsample_gen_ungrouped", gen.drop("g"), lambda df: df.sort("t", "v").upsample("t", every="12h")),
    ]
