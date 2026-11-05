"""The cumeval group: Polars' cumulative_eval (cumeval_test.go, tier 2 of
v0.6.6-scope.md), each prefix of the column an element's frame."""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

c, el = pl.col, pl.element
x, i = c("x"), c("i")

CE = pl.DataFrame({"x": pl.Series([1.0, 2, None, 4, 8], dtype=pl.Float64), "g": pl.Series([1, 1, 1, 2, 2], dtype=pl.Int64),
                   "i": pl.Series([3, 1, 4, 1, 5], dtype=pl.Int64)})
NULL_FIRST = pl.DataFrame({"x": pl.Series([None, 1.0, 2], dtype=pl.Float64)})


def sel(*exprs: pl.Expr):
    return lambda df: df.select(*exprs)


def cases() -> list[Case]:
    gen = Gen(5018)
    n = 120
    gen_ce = gen.frame(gen.floats(n, lo=-50, hi=50, edges=False, name="x"), gen.ints(n, pl.Int64, lo=0, hi=1, null_rate=0, edges=False, name="g"),
                       gen.ints(n, pl.Int64, lo=-20, hi=20, null_rate=0, name="i"))
    return [
        case("first_less_last_squared", CE, sel(x.cumulative_eval(el().first() - el().last().pow(2)))),
        case("sum", CE, sel(x.cumulative_eval(el().sum()))),
        case("mean_min_samples_2", CE, sel(x.cumulative_eval(el().mean(), min_samples=2))),
        case("sum_over_g", CE, sel(x.cumulative_eval(el().sum()).over("g"))),
        case("rank_last", CE, sel(i.cumulative_eval(el().rank("average").last()))),
        case("null_first_sum", NULL_FIRST, sel(x.cumulative_eval(el().sum()))),
        case("null_first_first", NULL_FIRST, sel(x.cumulative_eval(el().first()))),
        case("count", CE, sel(x.cumulative_eval(el().count()))),
        case("len", CE, sel(x.cumulative_eval(el().len()))),
        case("ints_max_less_min", CE, sel(i.cumulative_eval(el().max() - el().min()))),
        case("text_min", pl.DataFrame({"s": ["b", "a", "c"]}), sel(c("s").cumulative_eval(el().min()))),
        # --- generated -----------------------------------------------------------------------------
        case("floats_gen", gen_ce, sel(x.cumulative_eval(el().sum()).alias("sum"), x.cumulative_eval(el().mean(), min_samples=3).alias("mean3"),
                                       x.cumulative_eval(el().first() - el().last()).alias("first_last"), x.cumulative_eval(el().max()).alias("max"),
                                       x.cumulative_eval(el().count()).alias("count"), x.cumulative_eval(el().len()).alias("len"),
                                       x.cumulative_eval(el().sum()).over("g").alias("sum_g")), float="rel:1e-12", note="a prefix's sum, in Polars' order or ursus's: an ulp apart"),
        case("ints_gen", gen_ce, sel(i.cumulative_eval(el().max() - el().min()).alias("range"), i.cumulative_eval(el().rank("average").last()).alias("rank_last"),
                                     i.cumulative_eval(el().min()).alias("min"))),
    ]
