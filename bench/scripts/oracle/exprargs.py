"""The exprargs group, E5's first half (step 254): is_in against each row's own list,
and list.get and list.contains with an index and a value a row (exprargs_test.go).
The literal frame is the Go test's eight rows: null lists, empty ones and null
elements, an index past either end; a generated case follows.

Not here, with the reason: list.contains with a Float64 needle against a List(Int64),
which Polars 2.1 refuses and ursus meets as == meets them (a differential in the Go
test); the batch-size differential and the refusals, which the Go test keeps.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

nan = float("nan")
c = pl.col
tags, a, i, v = c("tags"), c("a"), c("i"), c("v")

LISTS = pl.DataFrame([
    pl.Series("id", [1, 2, 3, 4, 5, 6, 7, 8], dtype=pl.Int64),
    pl.Series("g", [1, 1, 1, 1, 2, 2, 2, 2], dtype=pl.Int64),
    pl.Series("tags", [[1, 2], [3], [None, 1], None, [], [None, 1], [4, 5], [3, None]], dtype=pl.List(pl.Int64)),
    pl.Series("a", [1, 2, None, 4, 5, 2, 5, 3], dtype=pl.Int64),
    pl.Series("i", [0, -1, 1, None, 5, -3, -2, 2], dtype=pl.Int64),
    pl.Series("v", [2, 9, 1, 6, None, None, 4, 3], dtype=pl.Int64),
])
NANZ = pl.DataFrame({"g": [1, 1, 2, 2, 3, 4], "f": [nan, 1.0, 0.0, 2.0, 1.0, 1.0], "x": [nan, 9.0, -0.0, 9.0, 1.5, None]})


def sel(*e: pl.Expr):
    return lambda df: df.select(*e)


def three(value: pl.Expr, lists: pl.Expr, index: pl.Expr, needle: pl.Expr) -> list[pl.Expr]:
    return [value.is_in(lists).alias("in"), lists.list.get(index, null_on_oob=True).alias("get"),
            lists.list.contains(needle).alias("has")]


def cases() -> list[Case]:
    g = Gen(4976)
    elems = g.ints(30, pl.Int64, lo=-3, hi=3, null_rate=0.15)
    gen = g.frame(g.lists(150, elems, name="tags"), g.ints(150, pl.Int64, lo=-3, hi=3, name="a"),
                  g.ints(150, pl.Int64, lo=-5, hi=5, name="i"), g.ints(150, pl.Int64, lo=-3, hi=3, name="v"))
    return [
        case("lists_plain", LISTS, sel(*three(a, tags, i, v))),
        case("lists_cast", LISTS, sel(*three(a.cast(pl.Int8), tags, i.cast(pl.Int8), v.cast(pl.Int8))),
             note="values meet as == compares them: a narrower value, an index of another integer type"),
        case("lists_strings", LISTS, sel(*three(a.cast(pl.String), tags.cast(pl.List(pl.String)), i, v.cast(pl.String)))),
        case("lists_agg", LISTS, lambda df: df.group_by("g", maintain_order=True).agg(
            a.is_in(tags).sum().alias("n_in"), tags.list.contains(v).sum().alias("n_has"))),
        case("lists_lit_index", LISTS, sel(tags.list.get(pl.lit(-1), null_on_oob=True).alias("last"))),
        case("is_in_list_nan_zero", NANZ, lambda df: df.group_by("g", maintain_order=True).agg(
            c("f").implode().alias("l"), c("x").first()).select(c("x").is_in(c("l"))),
             note="NaN is in a list holding NaN and -0.0 in one holding 0.0"),
        case("lists_gen", gen, sel(*three(a, tags, i, v))),
        case("lists_agg_gen", gen.with_columns((pl.int_range(pl.len()) % 7).alias("g")),
             lambda df: df.group_by("g", maintain_order=True).agg(a.is_in(tags).sum().alias("n_in"), tags.list.contains(v).sum().alias("n_has"))),
    ]
