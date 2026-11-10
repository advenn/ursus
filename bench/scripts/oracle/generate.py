"""The generate group: int_range, linear_space, repeat, ones and zeros, a column of
their own length, and their per-row forms, int_ranges, linear_spaces and repeat_by
(generate_test.go, G1 of v0.6.6-scope.md, step 263). A generator of literals alone
takes no frame: its case has no inputs, and its arguments are params, which the Go
side reads. Polars 2 names repeat after its first input, `literal`, where ursus names
it `repeat`, and makes a literal integer an Int32 where ursus's is an Int64
(feature-gaps.md §9 and §11): the Go side names both. Not here: the refusals.
"""

from __future__ import annotations

from datetime import date, datetime, timedelta

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
DF = pl.DataFrame({"a": [1, 2, 3], "s": [0, 5, None], "e": [3, 2, 4], "st": [1, -1, 2], "g": [1, 1, 2]})


def lone(name: str, query, **params) -> Case:
    """A generator of literals alone: no inputs, its arguments as params."""
    return case(name, {}, lambda: pl.select(query), params=params)


def ir(start, end, step) -> Case:
    return lone(f"int_range_{start}_{end}_{step}".replace("-", "m"), pl.int_range(start, end, step), start=start, end=end, step=step)


def ls(name: str, start, end, n, closed="both") -> Case:
    return lone(name, pl.linear_space(start, end, n, closed=closed), start=start, end=end, num_samples=n, closed=closed)


def cases() -> list[Case]:
    g = Gen(5032)
    gen = g.frame(g.ints(100, pl.Int64, lo=-5, hi=5, edges=False, name="s"), g.ints(100, pl.Int64, lo=-5, hi=5, edges=False, name="e"),
                  g.ints(100, pl.Int64, lo=1, hi=3, edges=False, name="st"), g.ints(100, pl.Int64, lo=0, hi=4, edges=False, name="n"),
                  g.floats(100, lo=-10, hi=10, edges=False, name="f"))
    return [
        ir(0, 5, 1), ir(0, 5, 2), ir(5, 0, -2), ir(5, 0, 1),
        case("a_row_index", DF, lambda df: df.with_columns(pl.int_range(0, pl.len(), 1).alias("i")).select("a", "i")),
        case("from_aggregates", DF, lambda df: df.select(pl.int_range(c("a").min(), c("a").max() + 2, 1))),
        case("beside_a_column", DF, lambda df: df.select("a", pl.int_range(10, 13, 1).alias("r"))),
        case("summed", DF, lambda df: df.select(pl.int_range(0, 5, 1).sum().cast(pl.Int64)), note="the Int128 sum here cast to Polars' Int64"),
        ls("linear_space_0_1_5", 0, 1, 5),
        ls("linear_space_far_end_exactly", 0, 0.3, 4),
        ls("linear_space_not_a_step_past", 0.2, 0.9, 8),
        ls("linear_space_closed_left", 0.1, 0.7, 7, "left"),
        ls("linear_space_closed_right", 0.1, 0.7, 7, "right"),
        ls("linear_space_closed_none", 0.1, 0.7, 7, "none"),
        ls("linear_space_one_sample_open", 0, 1, 1, "none"),
        ls("linear_space_no_sample", 0, 1, 0),
        lone("linear_space_float32", pl.linear_space(pl.lit(0, dtype=pl.Float32), pl.lit(1, dtype=pl.Float32), 3), num_samples=3),
        lone("linear_space_dates", pl.linear_space(date(2024, 1, 1), date(2024, 1, 2), 4), num_samples=4),
        lone("linear_space_instants", pl.linear_space(datetime(2024, 1, 1), datetime(2024, 1, 2), 3), num_samples=3),
        lone("linear_space_truncated_toward_zero", pl.linear_space(datetime(2024, 1, 1, 0, 0, 0, 3), datetime(2024, 1, 1), 3).dt.microsecond(),
             num_samples=3),
        lone("linear_space_durations", pl.linear_space(pl.lit(0).cast(pl.Duration("ns")), pl.lit(1_000_000_000).cast(pl.Duration("ns")), 3).cast(pl.Int64),
             num_samples=3),
        lone("repeat_1_3", pl.repeat(1, 3), value=1, n=3),
        lone("repeat_a_string", pl.repeat("x", 2), value="x", n=2),
        lone("repeat_none", pl.repeat(1, 0), value=1, n=0),
        case("repeat_a_total", DF, lambda df: df.select(pl.repeat(c("a").sum(), pl.len())), note="the Int128 sum here cast to Int64"),
        lone("ones_2", pl.ones(2), n=2),
        lone("zeros_2", pl.zeros(2), n=2),
        case("int_ranges", DF, lambda df: df.select(pl.int_ranges("s", "e", "st"))),
        case("linear_spaces", DF, lambda df: df.select(pl.linear_spaces("s", "e", 3))),
        case("linear_spaces_by_counts", DF, lambda df: df.select(pl.linear_spaces(0, 1, c("a")).alias("l"))),
        case("repeat_by", DF, lambda df: df.select(c("s").repeat_by(c("a")))),
        case("repeat_by_a_null_count", DF, lambda df: df.select(c("a").repeat_by(c("s")))),
        case("in_an_aggregation", DF, lambda df: df.group_by("g").agg(pl.int_range(0, pl.len(), 1).alias("r")).sort("g").select("r")),
        case("int_ranges_gen", gen, lambda df: df.select(pl.int_ranges("s", "e", "st"))),
        case("linear_spaces_gen", gen, lambda df: df.select(pl.linear_spaces("s", "e", c("n")).alias("l"), pl.linear_spaces(c("f"), 1, 4).alias("f"))),
        case("repeat_by_gen", gen, lambda df: df.select(c("f").repeat_by(c("n")))),
        lone("linear_space_gen_ends", pl.linear_space(-123.456, 789.0125, 97), start=-123.456, end=789.0125, num_samples=97, closed="both"),
    ]
