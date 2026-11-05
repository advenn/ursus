"""The listmore group: list.concat, shift, diff, count_matches, any, all, item, sort
with nulls last and a sample's lengths (listmore_test.go, step 269), on Polars' frame
and then over generated lists.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

c, el = pl.col, pl.element
L, B = pl.col("l").list, pl.col("b").list

LM = pl.DataFrame({
    "l": pl.Series("l", [[3, 1, 2], None, [], [5, None, 4]], dtype=pl.List(pl.Int64)),
    "o": pl.Series("o", [[9], [8], None, []], dtype=pl.List(pl.Int64)),
    "b": pl.Series("b", [[True, False], [None, True], [], None], dtype=pl.List(pl.Boolean)),
    "v": pl.Series("v", [1, 2, 3, None], dtype=pl.Int64),
    "n": pl.Series("n", [1, -1, 2, 0], dtype=pl.Int64),
})


def sel(*exprs: pl.Expr):
    return lambda df: df.select(*exprs)


def one(name: str, e: pl.Expr, **opts) -> Case:
    return case(name, LM, sel(e.alias("r")), **opts)


def cases() -> list[Case]:
    g = Gen(4994)
    n = 150
    ints = g.ints(n, pl.Int64, lo=-9, hi=9, name="e")
    gen_lists = g.frame(g.lists(n, ints, maxlen=5, name="l"), g.lists(n, ints, maxlen=3, name="o"),
                        g.ints(n, pl.Int64, lo=-9, hi=9, name="v"), g.ints(n, pl.Int64, lo=-3, hi=3, null_rate=0, edges=False, name="n"))
    gen_bools = g.frame(g.lists(n, g.bools(n, null_rate=0.3, name="e"), maxlen=4, name="b"))
    gen_items = g.frame(g.lists(n, ints, maxlen=1, empty_rate=0.3, name="o"))
    return [
        one("concat", L.concat(c("o"))),
        one("concat_value", L.concat(c("v")), note="a value a row joins its list; a null value joins nothing"),
        one("shift", L.shift(1)), one("shift_back", L.shift(-1)), one("shift_by_column", L.shift(c("n"))),
        one("diff", L.diff(1)), one("diff_by_two", L.diff(2)), one("diff_drop_nulls", L.diff(1, "drop")),
        one("sort_nulls_last", L.sort(nulls_last=True)), one("desc_nulls_last", L.sort(descending=True, nulls_last=True)),
        one("count_matches_1", L.count_matches(1)), one("count_matches_null", L.count_matches(None)),
        one("any", B.any()), one("all", B.all()),
        one("any_kleene", B.any(ignore_nulls=False)), one("all_kleene", B.all(ignore_nulls=False)),
        one("item_empty_allowed", c("o").list.item(allow_empty=True)),
        case("sample_lengths", LM, sel(L.sample(n=4, with_replacement=True, seed=1).list.len().alias("n"),
                                       L.sample(fraction=0.5, seed=1).list.len().alias("f")),
             note="a seeded draw is each engine's own; its lengths are Polars'"),
        # --- generated -----------------------------------------------------------------
        case("methods_gen", gen_lists, sel(L.concat(c("o")).alias("cat"), L.concat(c("v")).alias("catv"), L.shift(1).alias("sh"),
                                           L.shift(c("n")).alias("shn"), L.diff(1).alias("d1"), L.diff(2, "drop").alias("d2"),
                                           L.sort(nulls_last=True).alias("snl"), L.sort(descending=True, nulls_last=True).alias("sdnl"),
                                           L.count_matches(3).alias("cm3"), L.count_matches(None).alias("cmn"),
                                           L.sample(n=3, with_replacement=True, seed=7).list.len().alias("sn"))),
        case("bools_gen", gen_bools, sel(B.any().alias("any"), B.all().alias("all"), B.any(ignore_nulls=False).alias("any_k"),
                                         B.all(ignore_nulls=False).alias("all_k"))),
        case("item_gen", gen_items, sel(c("o").list.item(allow_empty=True))),
    ]
