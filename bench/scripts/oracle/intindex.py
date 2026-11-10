"""The intindex group: GroupByDynamic, Rolling, the rolling and ewm functions by an
integer index, and Upsample over one, by Polars' integer intervals, "3i"
(intindex_test.go, G4 of v0.6.7-scope.md, step 312). A count is cast to Int64 and an
integer sum too on both sides, Polars' being a UInt32 and an Int64 and ursus's a
Uint64 and an Int128 (feature-gaps.md §9); the windows are the question. A rolling
group by a key puts the key first in Polars and the index first here (§9), so that
case selects the columns in ursus's order. GroupByDynamic makes a row of every window
of the grid, Polars only of those holding a row (§9): the generated dynamic case
filters its count on both sides. Not here: the refusals, the Int32 index's type, and
the differential against a Datetime(ns) index.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
LF = pl.DataFrame({"i": [0, 1, 2, 5, 6, 9], "v": [1, 2, 3, 4, 5, 6], "g": ["a", "a", "b", "a", "b", "a"]})
COUNT = pl.len().cast(pl.Int64)
SUM = c("v").sum().cast(pl.Int64)
UP = pl.DataFrame({"i": [0, 3, 5], "v": [1, 2, 3]})


def dyn(*aggs, **o):
    return lambda df: df.group_by_dynamic("i", **o).agg(*aggs)


def roll(*aggs, **o):
    return lambda df: df.rolling("i", **o).agg(*aggs)


def sel(*exprs):
    return lambda df: df.select(*exprs)


def cases() -> list[Case]:
    g = Gen(5028)
    gen = g.frame(g.ints(150, pl.Int64, lo=0, hi=1000, null_rate=0, edges=False, name="i"),
                  g.ints(150, pl.Int64, lo=-100, hi=100, name="v")).sort("i")
    # v has no null: a window of nulls alone sums to null here and 0 in Polars (§9), which
    # the frameagg group pins; f keeps its nulls, a mean passing them over on both sides.
    gen_by = g.frame(g.ints(150, pl.Int64, lo=0, hi=1000, null_rate=0, edges=False, name="i"),
                     g.ints(150, pl.Int64, lo=-100, hi=100, null_rate=0, name="v"), g.floats(150, edges=False, name="f")).sort("i")
    sparse = g.frame(pl.Series("i", sorted(set(int(v) for v in g.rng.choice(200, size=40, replace=False))), dtype=pl.Int64))
    sparse = sparse.with_columns(pl.Series("v", range(sparse.height), dtype=pl.Int64))
    return [
        case("every_3i", LF, dyn(COUNT, SUM, every="3i")),
        case("every_2i_period_3i", LF, dyn(COUNT, SUM, every="2i", period="3i")),
        case("offset_1i", LF, dyn(COUNT, every="3i", offset="1i")),
        case("boundaries", LF, dyn(COUNT, every="3i", include_boundaries=True)),
        case("closed_both", LF, dyn(COUNT, every="3i", closed="both")),
        case("label_right", LF, dyn(COUNT, every="3i", label="right")),
        case("start_at_the_first_row", LF, dyn(COUNT, every="3i", start_by="datapoint")),
        case("negative", pl.DataFrame({"i": [-5, -1, 0, 4]}), dyn(COUNT, every="3i"), note="floored: -5 is in the window of -6"),
        case("rolling_3i", LF, roll(COUNT, SUM, period="3i")),
        case("rolling_by_a_key", LF, lambda df: df.rolling("i", period="3i", group_by="g").agg(COUNT).select("i", "g", "len"),
             note="the index first, then the key, where Polars puts the key first (§9)"),
        case("label_datapoint", LF, dyn(COUNT, every="3i", label="datapoint")),
        case("having", LF, lambda df: df.group_by_dynamic("i", every="3i").having(pl.len() > 1).agg(SUM)),
        case("a_shorthand", LF.select("i", "v"), dyn(pl.all().first(), every="3i"), note="GroupByDynamic(...).First()"),
        case("rolling_over_uint32", LF.with_columns(c("i").cast(pl.UInt32)), roll(SUM, period="3i")),
        case("rolling_over_uint64", LF.with_columns(c("i").cast(pl.UInt64)), roll(SUM, period="3i")),
        case("int32_index_stays", LF.with_columns(c("i").cast(pl.Int32)), dyn(COUNT, every="3i")),
        # --- the functions by an integer ----------------------------------------------
        case("rolling_sum_by", LF, sel(c("v").rolling_sum_by("i", "3i").cast(pl.Int64))),
        case("rolling_mean_by_closed_both", LF, sel(c("v").rolling_mean_by("i", "3i", closed="both"))),
        case("ewm_mean_by", LF, sel(c("v").ewm_mean_by("i", half_life="2i"))),
        case("by_an_int32", LF, sel(c("v").rolling_sum_by(c("i").cast(pl.Int32), "3i").cast(pl.Int64))),
        case("by_a_uint32", LF, sel(c("v").rolling_sum_by(c("i").cast(pl.UInt32), "3i").cast(pl.Int64))),
        case("ewm_mean_by_uint64", LF, sel(c("v").ewm_mean_by(c("i").cast(pl.UInt64), half_life="2i"))),
        # --- upsample -------------------------------------------------------------------
        case("upsample_every_2i", UP, lambda df: df.upsample("i", every="2i"), note="3 and 5 are off the grid of 0, 2, 4"),
        case("upsample_int32", UP.with_columns(c("i").cast(pl.Int32)), lambda df: df.upsample("i", every="1i")),
        case("upsample_uint32", UP.with_columns(c("i").cast(pl.UInt32)), lambda df: df.upsample("i", every="2i")),
        case("upsample_negative", pl.DataFrame({"i": [-3, 0], "v": [1, 2]}), lambda df: df.upsample("i", every="2i")),
        case("upsample_groups", pl.DataFrame({"i": [0, 3, 1, 4], "g": ["a", "a", "b", "b"], "v": [1, 2, 3, 4]}),
             lambda df: df.upsample("i", every="1i", group_by="g", maintain_order=True)),
        # --- generated ------------------------------------------------------------------
        case("dynamic_gen", gen, lambda df: df.group_by_dynamic("i", every="50i", period="120i", include_boundaries=True).agg(COUNT, SUM),
             note="windows of no rows are left out on both sides: ursus's Filter(len > 0)"),
        case("dynamic_gen_closed_right_label", gen, lambda df: df.group_by_dynamic("i", every="70i", closed="right", label="right").agg(COUNT, SUM)),
        case("rolling_gen", gen_by, lambda df: df.rolling("i", period="30i").agg(COUNT, SUM, c("f").mean().alias("m")), float="rel:1e-12",
             note="a window's mean, Polars' running sum against exact sums"),
        case("by_gen", gen_by, sel(c("v").rolling_sum_by("i", "30i").cast(pl.Int64).alias("s"), c("v").rolling_max_by("i", "30i").alias("mx"),
                                   c("f").rolling_mean_by("i", "30i", closed="both").alias("m")), float="rel:1e-12"),
        case("upsample_int_gen", sparse, lambda df: df.upsample("i", every="5i")),
    ]
