"""The argsort group: Nth, First and Last as selectors, and the index functions
arg_where, arg_true, arg_sort, arg_sort_by and arg_unique, a UInt32 column of their
own length, and in an aggregation a list a group of positions within it
(argsort_test.go, F2 of v0.6.6-scope.md). Polars 2.1's arg_sort_by of one key in an
aggregation is its arg_sort, where 1.44.1 answered positions past the group's end.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
a, b, p = c("a"), c("b"), c("p")

ARG = pl.DataFrame({
    "g": pl.Series([1, 1, 2, 2, 1], dtype=pl.Int64),
    "a": pl.Series([3, 1, None, 1, 2], dtype=pl.Int64),
    "b": pl.Series([5, 6, 7, 8, 9], dtype=pl.Int64),
    "p": pl.Series([True, False, True, None, True], dtype=pl.Boolean),
})


def sel(*exprs: pl.Expr):
    return lambda df: df.select(*exprs)


def by_g(*exprs: pl.Expr):
    return lambda df: df.group_by("g").agg(*exprs).sort("g")


IN_AGG = [a.arg_sort().alias("sort"), p.arg_true().alias("true"), pl.arg_where(b > 6).alias("where"), a.arg_unique().alias("unique"),
          pl.arg_sort_by("a", maintain_order=True).alias("by"), a.arg_sort(descending=True).alias("desc")]


def cases() -> list[Case]:
    gen = Gen(5005)
    n = 120
    gen_arg = gen.frame(gen.ints(n, pl.Int64, lo=0, hi=3, null_rate=0, edges=False, name="g"), gen.ints(n, pl.Int64, lo=-20, hi=20, name="a"),
                        gen.ints(n, pl.Int64, lo=-20, hi=20, null_rate=0, name="b"), gen.bools(n, name="p"))
    return [
        # --- selection by position ----------------------------------------------------------------
        case("nth_1", ARG, sel(pl.nth(1))),
        case("nth_last", ARG, sel(pl.nth(-1))),
        case("nth_0_2", ARG, sel(pl.nth(0, 2))),
        case("nth_1_times_2", ARG, sel(pl.nth(1) * 2)),
        case("first", ARG, sel(pl.first())),
        case("last", ARG, sel(pl.last())),
        case("first_a", ARG, sel(a.first())),
        # --- the index functions --------------------------------------------------------------------
        case("arg_where", ARG, sel(pl.arg_where(p))),
        case("arg_where_comparison", ARG, sel(pl.arg_where(b > 6))),
        case("arg_sort", ARG, sel(a.arg_sort())),
        case("arg_sort_descending", ARG, sel(a.arg_sort(descending=True)), note="ties in order"),
        case("arg_sort_nulls_last", ARG, sel(a.arg_sort(nulls_last=True))),
        case("arg_sort_descending_nulls_last", ARG, sel(a.arg_sort(descending=True, nulls_last=True))),
        case("arg_sort_by", ARG, sel(pl.arg_sort_by(["a", "b"], maintain_order=True))),
        case("arg_sort_by_descending_second", ARG, sel(pl.arg_sort_by(["a", "b"], descending=[False, True], maintain_order=True))),
        case("arg_sort_by_nulls_last", ARG, sel(pl.arg_sort_by(["g", "a"], descending=[True, False], nulls_last=[True, True], maintain_order=True))),
        case("arg_unique", ARG, sel(a.arg_unique())),
        case("arg_unique_key", ARG, sel(c("g").arg_unique())),
        case("arg_sort_beside", ARG, lambda df: df.with_columns(a.arg_sort().alias("o")).select("o")),
        case("in_aggregation", ARG, by_g(*IN_AGG), note="a list a group of positions within it"),
        # --- generated ------------------------------------------------------------------------------
        case("positions_gen", gen_arg, sel(pl.nth(1).alias("n1"), pl.nth(-1).alias("nl"), (pl.nth(1) * 2).alias("n2"), pl.first().alias("f"),
                                           pl.last().alias("l"))),
        case("arg_sort_gen", gen_arg, sel(a.arg_sort().alias("s"), a.arg_sort(descending=True).alias("sd"), a.arg_sort(nulls_last=True).alias("snl"),
                                          a.arg_sort(descending=True, nulls_last=True).alias("sdnl"), pl.arg_sort_by(["a", "b"], maintain_order=True).alias("by"),
                                          pl.arg_sort_by(["a", "b"], descending=[True, False], nulls_last=[True, False], maintain_order=True).alias("by2"))),
        case("arg_where_gen", gen_arg, sel(pl.arg_where(p))),
        case("arg_unique_gen", gen_arg, sel(a.arg_unique())),
        case("in_aggregation_gen", gen_arg, by_g(*IN_AGG)),
    ]
