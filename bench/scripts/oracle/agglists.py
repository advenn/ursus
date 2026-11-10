"""The agglists group: a non-aggregate in an aggregation is each group's values as a
list, a window in it computed a group at a time (agglists_test.go, step 331).

Spelled around: an integer Sum is an Int128 here, cumulative or under Over
(feature-gaps.md §9), so ursus's spelling casts those to Int64. A sum of a group of
only a null is null here and 0 in Polars (§9): over_y is in oracleDifferences.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
x, y, h = c("x"), c("y"), c("h")

LISTS = pl.DataFrame({
    "g": pl.Series([1, 1, 2, 2, 2], dtype=pl.Int64),
    "x": pl.Series([1, 2, 3, None, 5], dtype=pl.Int64),
    "y": pl.Series([5, 4, 3, 2, 1], dtype=pl.Int64),
    "h": pl.Series([1, 1, 1, 1, 1], dtype=pl.Int64),
})


def by_g(*exprs: pl.Expr):
    return lambda df: df.group_by("g").agg(*exprs).sort("g")


def cases() -> list[Case]:
    gen = Gen(5004)
    n = 150
    keys = gen.ints(n, pl.Int64, lo=0, hi=4, null_rate=0, edges=False, name="g")
    gen_lists = gen.frame(keys, gen.ints(n, pl.Int64, lo=-50, hi=50, name="x"), gen.ints(n, pl.Int64, lo=0, hi=9, null_rate=0, edges=False, name="y"))
    gen_full = gen.frame(keys, gen.ints(n, pl.Int64, lo=-50, hi=50, null_rate=0, name="x"),
                         gen.ints(n, pl.Int64, lo=0, hi=2, null_rate=0, edges=False, name="y"))
    return [
        case("column", LISTS, by_g(x)),
        case("arithmetic", LISTS, by_g(x * 2)),
        case("two_columns", LISTS, by_g(x + y)),
        case("cast", LISTS, by_g(x.cast(pl.Float64))),
        case("conditional", LISTS, by_g(pl.when(y > 2).then(x).otherwise(0))),
        case("alias", LISTS, by_g((x * 2).alias("z"))),
        case("name_suffix", LISTS, by_g((x * 2).name.suffix("_2"))),
        case("filter", LISTS, by_g(x.filter(y > 2))),
        case("cum_sum", LISTS, by_g(x.cum_sum()), note="a window a group at a time; ursus's Int128 cast to Int64"),
        case("shift", LISTS, by_g(x.shift(1))),
        case("diff", LISTS, by_g(x.diff(1))),
        case("interpolate", LISTS, by_g(x.interpolate())),
        case("window_under_arithmetic", LISTS, by_g(x.cum_sum() * 10), note="ursus's Int128 cast to Int64"),
        case("over_y", LISTS, by_g(x.sum().over("y")),
             note="an Over within the group, every y its own: each row's x; Polars sums the null to 0, ursus to null (§9): oracleDifferences"),
        case("over_h", LISTS, by_g(x.sum().over("h")), note="one h across the groups: its Over within each group; ursus's Int128 cast"),
        case("aggregate_list_literal", LISTS, by_g(x.sum().cast(pl.Int64), y.alias("ys"), pl.lit(1, dtype=pl.Int64).alias("one")),
             note="an aggregate stays one, a literal a scalar, beside a list; both sums cast to Int64"),
        case("window_in_aggregate", LISTS, by_g(x.cum_sum().max()), note="each group's greatest running total; ursus's Int128 cast"),
        case("mix_mean", LISTS, by_g(x - x.mean()), note="each row's value less its group's mean, a list a group (step 336)"),
        # --- generated ---------------------------------------------------------------------------
        case("lists_gen", gen_lists, by_g(x.alias("col"), (x * 2).alias("mul"), (x + y).alias("add"), x.cast(pl.Float64).alias("f"),
                                          pl.when(y > 4).then(x).otherwise(0).alias("cond"), x.filter(y > 4).alias("flt"),
                                          x.cum_sum().alias("cs"), x.shift(1).alias("sh"), x.diff(1).alias("df"),
                                          x.interpolate().alias("ip"), (x - x.mean()).alias("mix"), (x.cum_sum() * 10).alias("cs10")),
             note="ursus's Int128 cum_sums cast to Int64"),
        case("lists_over_gen", gen_full, by_g(x.sum().over("y").alias("over_y"), x.cum_sum().over("y").alias("cs_over_y"),
                                              x.cum_sum().max().alias("cs_max")),
             note="no null x, so no sum of only nulls; ursus's Int128 cast to Int64"),
    ]
