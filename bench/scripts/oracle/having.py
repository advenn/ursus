"""The having group: GroupBy.having, the groups whose predicates, aggregates of their
own, all hold, a null one left out, over Agg, the shorthands, Head and Tail and a
dynamic group's windows (having_test.go, G2 of v0.6.7-scope.md, step 309), with
maintain_order=True, the order here. A sum and a count are cast to Int64 on both sides
(feature-gaps.md §9). Not here: the refusals, Len's name through a cast, and the
keyless cases, whose answers are the frame's own Head, Go differentials.
"""

from __future__ import annotations

from datetime import date

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
LF = pl.DataFrame({"k": ["a", "b", "a", "b", "c", None, None], "v": [1, 2, 3, 4, 5, None, 7], "w": [1.0, 2.0, 3.0, 4.0, 5.0, 6.0, 7.0]})
SUM = c("v").sum().cast(pl.Int64)
LEN = pl.len().cast(pl.Int64)
DAYS = pl.DataFrame({"t": [date(2024, 1, 1), date(2024, 1, 2), date(2024, 1, 3), date(2024, 1, 6)], "v": [1, 2, 3, 4]})


def hv(*preds):
    return lambda df: df.group_by("k", maintain_order=True).having(*preds)


def cases() -> list[Case]:
    g = Gen(5030)
    gen = g.frame(g.strings(150, alphabet="pqrstu", maxlen=1, name="k"), g.ints(150, pl.Int64, lo=0, hi=1000, edges=False, name="v"),
                  g.floats(150, edges=False, name="w"))
    return [
        case("len_gt_1", LF, lambda df: hv(pl.len() > 1)(df).agg(SUM)),
        case("a_sum_over_3", LF, lambda df: hv(c("v").sum() > 3)(df).agg(c("w").mean())),
        case("two_predicates", LF, lambda df: hv(pl.len() > 1, c("v").max() < 4)(df).agg(SUM)),
        case("chained", LF, lambda df: hv(pl.len() > 1)(df).having(c("v").max() < 6).agg(SUM), note="both hold: c's max is under 6, but it is one row"),
        case("a_null_predicate", LF, lambda df: hv(c("v").first() > 0)(df).agg(LEN), note="the null group's first v is null, and the group is left out"),
        case("a_predicate_of_a_column_not_aggregated", LF, lambda df: hv(c("w").min() > 2)(df).agg(SUM)),
        case("no_aggregate", LF, lambda df: hv(pl.len() > 1)(df).agg()),
        case("sum_shorthand", LF, lambda df: hv(pl.len() > 1)(df).sum().with_columns(c("v").cast(pl.Int64))),
        case("len_shorthand", LF, lambda df: hv(pl.len() > 1)(df).len().with_columns(c("len").cast(pl.Int64))),
        case("head", LF, lambda df: hv(pl.len() > 1)(df).head(1)),
        case("tail", LF, lambda df: hv(c("v").sum() > 4)(df).tail(1)),
        case("a_literal_true", LF, lambda df: hv(pl.lit(True))(df).agg(LEN)),
        case("every_group_left_out", LF, lambda df: hv(pl.len() > 10)(df).agg(LEN)),
        case("a_dynamic_group", DAYS, lambda df: df.group_by_dynamic("t", every="2d").having(pl.len() > 1).agg(SUM),
             note="Polars' windows of Dec 31 and Jan 6 a row each; the windows of none here fail the count too"),
        case("having_gen", gen, lambda df: hv(pl.len() > 20, c("v").max() < 990)(df).agg(SUM, c("w").max().alias("w"), LEN.alias("n"))),
        case("having_gen_head", gen, lambda df: hv(c("w").min() < -900)(df).head(3)),
    ]
