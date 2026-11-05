"""The listset group: the four list set operations and list.gather (listset_test.go,
step 223): the first list's order then the second's, a null element a value, an empty
list, a null list on either side, NaN and -0 among floats; then generated lists.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
A = pl.col("a").list

SET = pl.DataFrame({
    "a": pl.Series("a", [[1, 2, 2, 3, None], [], None, [5, 4], [1, None], [7], [3, 1, 2]], dtype=pl.List(pl.Int64)),
    "b": pl.Series("b", [[3, 4, None, 4], [1], [1], None, [None, None], [], [2, 9, 1]], dtype=pl.List(pl.Int64)),
})
FLOATS = pl.DataFrame({"a": pl.Series("a", [[float("nan"), -0.0, 1.0]]), "b": pl.Series("b", [[0.0, float("nan")]])})
MIXED = pl.DataFrame({"a": pl.Series("a", [[1, 2]], dtype=pl.List(pl.Int32)), "b": pl.Series("b", [[2.5, 1.0]])})
GATHER = pl.DataFrame({
    "a": pl.Series("a", [[10, 20, 30], [], None, [1, 2], [10, 20, 30]], dtype=pl.List(pl.Int64)),
    "i": pl.Series("i", [[2, -1, 0, 5], [0], [0], None, [None, 1, -3]], dtype=pl.List(pl.Int64)),
})

OPS = [A.set_union(c("b")).alias("union"), A.set_intersection(c("b")).alias("inter"),
       A.set_difference(c("b")).alias("diff"), A.set_symmetric_difference(c("b")).alias("sym")]
SORTED_DIFF = [OPS[0], OPS[1], A.set_difference(c("b")).list.sort().alias("diff"), OPS[3]]


def sel(*exprs: pl.Expr):
    return lambda df: df.select(*exprs)


def cases() -> list[Case]:
    g = Gen(4995)
    n = 150
    small = g.ints(n, pl.Int64, lo=0, hi=6, null_rate=0.15, edges=False, name="e")
    gen_sets = g.frame(g.lists(n, small, maxlen=5, name="a"), g.lists(n, small, maxlen=5, name="b"))
    fl = g.floats(n, lo=-2, hi=2, null_rate=0.05, ties=True, name="e")
    gen_floats = g.frame(g.lists(n, fl, maxlen=5, name="a"), g.lists(n, fl, maxlen=5, name="b"))
    lists = g.lists(n, g.ints(n, pl.Int64, lo=-50, hi=50, name="e"), maxlen=4, name="a")
    gen_gather = g.frame(lists, g.lists(n, g.ints(n, pl.Int64, lo=-5, hi=5, null_rate=0.1, edges=False, name="e"), maxlen=3, name="i"))
    return [
        case("set_operations", SET, sel(*OPS)),
        case("set_floats", FLOATS, sel(A.set_intersection(c("b")).alias("i"), A.set_union(c("b")).alias("u")),
             note="NaN equals NaN, -0 equals 0, and the first list's -0 is kept"),
        case("set_union_i32_f64", MIXED, sel(A.set_union(c("b")).alias("u")), note="an Int32 list and a Float64 list meet at Float64"),
        case("gather", GATHER, sel(A.gather(c("i"), null_on_oob=True).alias("g"))),
        case("set_operations_gen", gen_sets, sel(*SORTED_DIFF),
             note="Polars 2.1 orders set_difference's elements by its hash table, the other three by the lists: the difference sorted on both sides"),
        case("set_floats_gen", gen_floats, sel(*SORTED_DIFF), note="NaN, -0 and halves among the elements; the difference sorted as above"),
        case("gather_gen", gen_gather, sel(A.gather(c("i"), null_on_oob=True).alias("g"))),
    ]
