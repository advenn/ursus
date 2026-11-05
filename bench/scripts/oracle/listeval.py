"""The listeval group: List().Eval, Agg and Filter, the body over each list's elements
(listeval_test.go, step 268), on Polars' l [[3, 1, 2], None, [], [5, None, 4]], then
over generated lists of integers, floats and words.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

c, el = pl.col, pl.element
L = pl.col("l").list

EV = pl.DataFrame({"l": pl.Series("l", [[3, 1, 2], None, [], [5, None, 4]], dtype=pl.List(pl.Int64))})
WORDS = pl.DataFrame({"s": ["a,bb", "ccc"]})


def sel(*exprs: pl.Expr):
    return lambda df: df.select(*exprs)


def one(name: str, e: pl.Expr, **opts) -> Case:
    return case(name, EV, sel(e), **opts)


def cases() -> list[Case]:
    g = Gen(4993)
    n = 150
    gen_ints = g.frame(g.lists(n, g.ints(n, pl.Int64, lo=-30, hi=30, name="e"), maxlen=5, name="l"))
    gen_floats = g.frame(g.lists(n, g.floats(n, edges=False, name="e"), maxlen=5, name="l"))
    gen_words = g.frame(g.lists(n, g.strings(n, name="e"), maxlen=4, name="l"))
    return [
        one("doubled", L.eval(el() * 2)),
        one("sum", L.eval(el().sum()), note="an Int64 sums to an Int128 here; an empty list's sum is null here, 0 in Polars (§9)"),
        one("mean", L.eval(el().mean())),
        one("centred", L.eval(el() - el().mean())),
        one("filtered", L.eval(el().filter(el() > 1))),
        one("sorted", L.eval(el().sort())),
        one("sorted_doubled", L.eval(el().sort() * 2)),
        one("head", L.eval(el().head(2))),
        one("filtered_sum", L.eval(el().filter(el() > 1).sum()), note="as sum"),
        one("ranked", L.eval(el().drop_nulls().rank("average")),
             note="without the drop, Polars leaves a null's rank null where ursus ranks it first (feature-gaps.md §1)"),
        one("cum_sum", L.eval(el().cum_sum()), note="a running sum has Sum's width here, an Int128 for an Int64 (§9)"),
        one("shifted", L.eval(el().shift(1))),
        one("cast", L.eval(el().cast(pl.String))),
        one("is_null", L.eval(el().is_null())),
        one("constant", L.eval(pl.lit(1)), note="a literal has its Go value's type here, Int64, where Polars' is an Int32 (§9)"),
        one("count", L.eval(el().count()), note="a count inside a list's body is a UInt32, as Polars'"),
        one("len", L.eval(el().len())),
        one("first", L.eval(el().first())),
        one("agg_values", L.agg(el() * 2)),
        one("agg_filter", L.agg(el().filter(el() > 1))),
        one("filter", L.filter(el() > 1)),
        one("filter_not_null", L.filter(el().is_not_null())),
        case("agg_aggregates", EV, sel(L.agg(el().sum()).alias("s"), L.agg(el().mean()).alias("m")), note="the sum as sum"),
        case("strings", WORDS, lambda df: df.select(c("s").str.split(",")).select(c("s").list.eval(el().str.len_chars()))),
        # --- generated -----------------------------------------------------------------
        case("eval_gen", gen_ints, sel(L.eval(el() * 2).alias("x2"), L.eval(el().sort()).alias("sorted"), L.eval(el().cum_sum()).alias("cum"),
                                       L.eval(el().shift(1)).alias("sh"), L.eval(el().is_null()).alias("isn"),
                                       L.eval(el().cast(pl.String)).alias("txt"), L.eval(el().head(2)).alias("h2"),
                                       L.eval(el().filter(el() > 0)).alias("pos"), L.eval(el().drop_nulls().rank("min")).alias("rk"),
                                       L.filter(el() < 0).alias("neg"), L.eval(el().first()).alias("first"))),
        case("agg_gen", gen_ints, sel(L.agg(el().max()).alias("max"), L.agg(el().min()).alias("min"), L.agg(el().mean()).alias("mean"),
                                      L.agg(el().first()).alias("first"), L.agg(el().last()).alias("last"),
                                      L.agg(el().filter(el() > 0).max()).alias("posmax"))),
        case("eval_floats_gen", gen_floats, sel(L.eval(el() - el().mean()).alias("centred"), L.eval(el().cum_sum()).alias("cum"),
                                                L.agg(el().mean()).alias("mean")), float="rel:1e-12",
             note="a mean's and a running sum's order leave last bits apart (feature-gaps.md §1)"),
        case("eval_words_gen", gen_words, sel(L.eval(el().str.len_chars()).alias("n"), L.eval(el().str.to_uppercase()).alias("up"),
                                              L.filter(el().str.starts_with("a")).alias("as"), L.eval(el().sort()).alias("sorted"))),
    ]
