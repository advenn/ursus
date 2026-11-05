"""The modeagg group: a group's Mode as an aggregate of its own (modeagg_test.go, step
340): with maintain_order, Polars' order is the one the values first appear in, as
ursus's Mode(true); without, Polars' is its hash table's where ursus's is sorted,
nulls first, so those are asked of Polars as mode().list.sort(). A Struct's modes
have no order and come as they first appear in both (feature-gaps.md §9), so ursus's
Mode(false) of a Struct is asked as Polars' maintain_order=True.

Not here: the plan, the four-thread first-seen order, and the spill past a limit.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

nan, negz = float("nan"), -0.0
c = pl.col
x, s, b, i, d = c("x"), c("s"), c("b"), c("i"), c("d")

MODE = pl.DataFrame({
    "g": pl.Series([1, 1, 1, 1, 2, 2, 2, 3, 3, 3, 3], dtype=pl.Int64),
    "x": pl.Series([3.0, None, None, 3, negz, 0, 2, nan, 1, nan, 1], dtype=pl.Float64),
    "s": pl.Series(["b", "a", "b", "a", "z", "y", "z", None, None, "q", "q"], dtype=pl.String),
    "b": pl.Series([True, False, True, None, None, None, True, False, False, True, True], dtype=pl.Boolean),
    "i": pl.Series([5, 7, 7, 5, 1, 2, 3, 9, 9, 8, 8], dtype=pl.Int64),
}).with_columns(i.cast(pl.Int32).cast(pl.Date).alias("d"))


def sel(*exprs: pl.Expr):
    return lambda df: df.select(*exprs)


def by_g(*exprs: pl.Expr):
    return lambda df: df.group_by("g").agg(*exprs).sort("g")


def cases() -> list[Case]:
    gen = Gen(5009)
    n = 200
    gen_mode = gen.frame(gen.ints(n, pl.Int64, lo=0, hi=3, null_rate=0, edges=False, name="g"), gen.ints(n, pl.Int64, lo=0, hi=4, name="i"),
                         gen.strings(n, alphabet="abc", maxlen=1, name="s"), gen.floats(n, lo=0, hi=2, edges=False, ties=True, name="f"),
                         gen.bools(n, name="b"))
    return [
        case("as_they_come", MODE, by_g(x.mode(maintain_order=True), s.mode(maintain_order=True), b.mode(maintain_order=True),
                                        d.mode(maintain_order=True), i.mode(maintain_order=True).first().alias("i1"))),
        case("sorted", MODE, by_g(x.mode().sort(), s.mode().sort(), b.mode().sort(), d.mode().sort()),
             note="ursus's Mode(false) is sorted, nulls first, NaN last, as list.sort sorts"),
        case("over_the_frame", MODE, sel(s.mode(maintain_order=True).first(), i.mode(maintain_order=True).last())),
        case("in_a_dynamic_group", MODE, lambda df: df.with_row_index("r").with_columns(c("r").cast(pl.Int64))
             .group_by_dynamic("r", every="4i").agg(i.mode(maintain_order=True))),
        case("mode_of_a_sort", MODE, by_g(i.sort().mode(maintain_order=True)), note="a mode of a reshape reads the reshape's list"),
        case("struct_mode", MODE, by_g(pl.struct("x").mode(maintain_order=True).alias("m")), note="a Struct's modes, field by field (step 344)"),
        case("struct_mode_unordered", MODE, by_g(pl.struct("x").mode(maintain_order=True).alias("m")),
             note="ursus's Mode(false) of a Struct, which has no order, comes as first seen (§9): Polars' maintain_order=True"),
        # --- generated -----------------------------------------------------------------------------
        case("mode_gen_first_seen", gen_mode, by_g(i.mode(maintain_order=True).alias("i"), s.mode(maintain_order=True).alias("s"),
                                                   c("f").mode(maintain_order=True).alias("f"), c("b").mode(maintain_order=True).alias("b"))),
        case("mode_gen_sorted", gen_mode, by_g(i.mode().sort().alias("i"), s.mode().sort().alias("s"), c("f").mode().sort().alias("f"),
                                               c("b").mode().sort().alias("b"))),
        case("mode_gen_first", gen_mode, by_g(i.mode(maintain_order=True).first().alias("i1"), s.mode(maintain_order=True).last().alias("sl"))),
    ]
