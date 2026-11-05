"""The exprroll group: Expr.Rolling, an aggregate expression over each row's window of
a rolling group (exprroll_test.go, step 337).

A sum of a window holding no value is null here and 0 in Polars (feature-gaps.md §9):
ahead_sum_no_offset_over_g, sum_1d_offset_m2d, filter_sum_2d and in_aggregation are in
oracleDifferences. Spelled around: a count is a Uint64 here and an integer Sum an
Int128, cast to Int64 in the spelling.

Not here: an index not sorted, which Polars refuses and ursus answers by value (§9),
held to a differential in the Go test; the refusals; the agreement of a window's own
rolling with the rolling group's over 400 rows; the spill past a limit.
"""

from __future__ import annotations

from datetime import datetime

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
x = c("x")

ER = pl.DataFrame({"t": pl.Series([1, 2, 3, 5, 6, 9], dtype=pl.Int64), "x": pl.Series([1.0, 4, None, 8, 3, 2], dtype=pl.Float64),
                   "g": ["a", "b", "a", "a", "b", "b"]})
P = pl.DataFrame({"t": pl.Series([1, 5, 2, 6, 7], dtype=pl.Int64), "g": ["a", "a", "b", "b", "a"], "x": pl.Series([1.0, 2, 3, 4, 5], dtype=pl.Float64)})
AHEAD = pl.DataFrame({"t": pl.Series([1, 10, 3], dtype=pl.Int64), "g": ["a", "b", "a"], "x": pl.Series([1.0, 2, 3], dtype=pl.Float64)})
TIE = pl.DataFrame({"t": pl.Series([1, 10, 1], dtype=pl.Int64), "g": ["a", "b", "a"], "x": pl.Series([1.0, 2, 3], dtype=pl.Float64)})
DT = pl.DataFrame({"t": pl.Series([datetime(2024, 1, 1), datetime(2024, 1, 1, 12), datetime(2024, 1, 2, 6), datetime(2024, 1, 4)], dtype=pl.Datetime("us")),
                   "x": pl.Series([1, 2, 3, 4], dtype=pl.Int64), "h": pl.Series([1, 1, 2, 1], dtype=pl.Int64), "k": ["p", "p", "p", "q"]})
I32 = pl.DataFrame({"t": pl.Series([1, 2, 4], dtype=pl.Int32), "x": pl.Series([1.0, 2, 4], dtype=pl.Float64)})


def sel(e: pl.Expr):
    return lambda df: df.select(e.alias("r"))


def roll(e: pl.Expr, period: str, offset: str | None = None, closed: str = "right") -> pl.Expr:
    return e.rolling(index_column="t", period=period, offset=offset, closed=closed)


def cases() -> list[Case]:
    gen = Gen(5017)
    n = 150
    ts = [int(k) for k in gen.rng.integers(1, 3, n).cumsum()]
    gen_er = pl.DataFrame({"t": pl.Series(ts, dtype=pl.Int64), "x": gen.floats(n, lo=-20, hi=20, null_rate=0, edges=False, name="x"),
                           "g": gen.strings(n, null_rate=0, edges=False, alphabet="abc", maxlen=1, name="g")})
    return [
        # --- an integer index -------------------------------------------------------------------------
        case("sum_2i", ER, sel(roll(x.sum(), "2i"))),
        case("range_3i", ER, sel(roll(x.max() - x.min(), "3i"))),
        case("count_3i", ER, sel(roll(x.count(), "3i").cast(pl.Int64)), note="a count is a Uint64 here: both cast to Int64"),
        case("len_3i", ER, sel(roll(pl.len(), "3i").cast(pl.Int64)), note="both cast to Int64"),
        case("list_2i", ER, sel(roll(x, "2i")), note="a row's values, a list"),
        case("sum_of_product_2i", ER, sel(roll((x * 2).sum(), "2i"))),
        case("no_offset_3i", ER, sel(roll(x.sum(), "3i", "0i")), note="the last row's window, ahead of every row, holds none: 0 there, null here (§9): oracleDifferences"),
        case("closed_both_3i", ER, sel(roll(x.sum(), "3i", None, "both"))),
        case("first_3i", ER, sel(roll(x.first(), "3i")), note="a first, its null"),
        case("sort_first_3i", ER, sel(roll(x.sort().first(), "3i")), note="a sort's first, nulls first"),
        case("mean_3i_over_g", ER, sel(roll(x.mean(), "3i").over("g"))),
        case("with_columns_sum_2i", ER, lambda df: df.with_columns(roll(x.sum(), "2i")), note="beside the frame's columns, named as the expression is"),
        # --- over a partition sorted within itself ----------------------------------------------------
        case("sum_3i_over_g_partitions", P, sel(roll(x.sum(), "3i").over("g"))),
        case("max_2i_over_g_with_columns", P, lambda df: df.with_columns(roll(x.max(), "2i").over("g"))),
        case("ahead_sum_no_offset_over_g", AHEAD, lambda df: df.select(roll(x.sum(), "3i", "0i").over("g")),
             note="a window ahead of its row, held to the end; Polars' empty windows are 0, null here (§9): oracleDifferences"),
        case("ahead_last_no_offset_over_g", AHEAD, lambda df: df.select(roll(x.last(), "3i", "0i").over("g"))),
        case("tie_last_3i_over_g", TIE, lambda df: df.select(roll(x.last(), "3i").over("g")),
             note="a tie in a partition after another partition's later row: the first row's window takes it"),
        case("first_3i_over_g_partitions", P, lambda df: df.select(roll(x.first(), "3i").over("g")),
             note="rolled by the frame's rolling group, a partition's rows put back in the frame's order"),
        # --- a datetime index ----------------------------------------------------------------------------
        case("sum_1d", DT, lambda df: df.select(roll(x.sum(), "1d")), note="ursus's Int128 cast to Int64"),
        case("sum_1d_offset_m2d", DT, lambda df: df.select(roll(x.sum(), "1d", "-2d")), note="empty windows 0 in Polars, null here (§9): oracleDifferences; Int128 cast"),
        case("filter_sum_2d", DT, lambda df: df.select(roll(x.filter(x > 1).sum(), "2d")), note="the first window's filter leaves none: 0 there, null here (§9); Int128 cast"),
        case("sum_3d_over_h_k", DT, lambda df: df.select(roll(x.sum(), "3d").over("h", "k")), note="Int128 cast"),
        case("mean_2i_int32", I32, lambda df: df.select(roll(x.mean(), "2i"))),
        # --- in an aggregation -----------------------------------------------------------------------------
        case("in_aggregation", ER, lambda df: df.group_by("g").agg(roll(x.sum(), "2i")).sort("g"),
             note="each group's rows' windows' answers, a list a group; a's second window holds only a null: 0 there, null here (§9): oracleDifferences"),
        # --- generated ----------------------------------------------------------------------------------------
        case("int_index_gen", gen_er, lambda df: df.select(
            roll(x.sum(), "5i").alias("sum5"), roll(x.mean(), "5i").alias("mean5"), roll(x.min(), "4i").alias("min4"), roll(x.max(), "4i").alias("max4"),
            roll(x.sum(), "4i", None, "both").alias("sum4_both"), roll(x.mean(), "4i", None, "left").alias("mean4_left"),
            roll(x.max(), "4i", "-6i").alias("max4_m6"), roll(x.first(), "5i").alias("first5"), roll(x.last(), "3i", "0i").alias("last3_0"),
            roll(pl.len(), "6i").cast(pl.Int64).alias("len6"), roll(x.mean(), "6i").over("g").alias("mean6_g"),
            roll(x.max() - x.min(), "7i").over("g").alias("range7_g")),
             float="rel:1e-12", note="no null x, and a sum only where the window holds its own row: a mean or a max where the first rows' windows are "
                                 "empty, null in both; a window's sum is an ulp apart in its order"),
        case("var_gen", gen_er, lambda df: df.select(roll(x.var(ddof=1), "6i").alias("var6"), roll(x.std(ddof=0), "5i").over("g").alias("std5_g")),
             float="rel:1e-12", note="a variance's merge order"),
    ]
