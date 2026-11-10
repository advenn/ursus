"""The framecolumns group: insert_column of values or an expression, at an index
counted from either end, an aggregate and a literal broadcast (framecolumns_test.go, C5
of v0.6.7-scope.md, step 302). The column inserted is an input of its own, `n`. Not
here: the refusals, the eager differential, and hash_rows, whose numbers are ursus's
own.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
LF = pl.DataFrame({"a": [1, 2, 3], "b": ["x", "y", "z"], "c": [1.5, None, 2.5]})
N = pl.DataFrame({"n": [7, 8, 9]})


def ins(index: int, e):
    # Polars' eager insert_column changes the frame in place: a clone keeps the cases apart.
    return lambda df, **_: df.clone().insert_column(index, e)


def cases() -> list[Case]:
    g = Gen(5023)
    gen = g.frame(g.ints(100, pl.Int64, name="a"), g.strings(100, name="b"), g.floats(100, name="c"))
    gn = g.frame(g.floats(100, name="n"))
    return [
        case("values_at_1", {"df": LF, "n": N}, lambda df, n: df.clone().insert_column(1, n["n"])),
        case("an_expression_at_0", LF, ins(0, (c("a") * 10).alias("a10"))),
        case("at_the_width", LF, ins(3, c("a").alias("z"))),
        case("at_minus_1", LF, ins(-1, c("a").alias("m1"))),
        case("at_minus_3", LF, ins(-3, c("a").alias("m3"))),
        case("an_aggregate", LF, ins(1, c("a").sum().alias("s")), note="an aggregate is broadcast; the Int128 sum here cast to Int64"),
        case("a_literal", LF, ins(1, pl.lit(5, dtype=pl.Int64).alias("five"))),
        case("insert_gen", {"df": gen, "n": gn}, lambda df, n: df.clone().insert_column(1, n["n"]).insert_column(-1, (c("a") * 2).alias("a2"))),
    ]
