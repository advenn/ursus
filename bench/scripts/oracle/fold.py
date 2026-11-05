"""The fold group: concat_list, fold, reduce, their running forms, cum_sum_horizontal
and format (fold_test.go, F1 of v0.6.6-scope.md). The literal frame is the Go test's;
a generated case follows each function.

A fold's seed is spelled pl.lit(0, dtype=pl.Int64): Polars' bare 0 is an Int32 where
ursus's Go 0 is an Int64, the literal rule of feature-gaps.md §9, and the fold is the
question here. Not here, with the reason: concat_list of a number beside text, which
Polars casts and ursus refuses (§9; the Go test keeps the refusal), and a format of
too few arguments, refused in both.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
a, b, f, s, l = c("a"), c("b"), c("f"), c("s"), c("l")
ZERO = pl.lit(0, dtype=pl.Int64)

FIX = pl.DataFrame({
    "a": pl.Series([1, 2, None], dtype=pl.Int64), "b": pl.Series([10, None, 30], dtype=pl.Int64), "f": [0.5, 1.5, 2.5],
    "s": ["x", "y", None], "lo": pl.Series([1, None, 5], dtype=pl.Int64), "hi": pl.Series([3, 1, 5], dtype=pl.Int64),
}).with_columns(pl.int_ranges(c("lo"), c("hi")).alias("l"))  # [1, 2], null, []


def add(acc, x):
    return acc + x


def mul(acc, x):
    return acc * x


def sel(*e: pl.Expr):
    return lambda df: df.select(*e)


def fields(name: str, *names: str):
    return lambda df: df.select(c(name).struct.field(n) for n in names)


def cases() -> list[Case]:
    g = Gen(4977)
    gen = g.frame(g.ints(150, pl.Int64, lo=-100, hi=100, name="a"), g.ints(150, pl.Int64, lo=-100, hi=100, name="b"),
                  g.ints(150, pl.Int64, lo=-100, hi=100, name="c"), g.floats(150, lo=-10, hi=10, name="f"),
                  g.strings(150, name="s"), g.lists(150, g.ints(20, pl.Int64, lo=-5, hi=5), name="l"))
    return [
        # --- concat_list ----------------------------------------------------------------
        case("concat_list_two", FIX, sel(pl.concat_list(a, b).alias("r"))),
        case("concat_list_list_value", FIX, sel(pl.concat_list(l, a).alias("r"))),
        case("concat_list_int_float", FIX, sel(pl.concat_list(a, f).alias("r"))),
        case("concat_list_lit", FIX, sel(pl.concat_list(pl.lit(7, dtype=pl.Int64), a).alias("r"))),
        case("concat_list_two_lists", FIX, sel(pl.concat_list(l, l).alias("r"))),
        case("concat_list_gen", gen, sel(pl.concat_list(a, b, c("c")).alias("abc"), pl.concat_list(l, a).alias("la"),
                                         pl.concat_list(a, f).alias("af"), pl.concat_list(l, l).alias("ll"))),
        # --- fold, reduce and the running forms ------------------------------------------
        case("fold", FIX, sel(pl.fold(ZERO, add, [a, b]))),
        case("fold_from_col", FIX, sel(pl.fold(f, mul, [a, b]))),
        case("fold_all", FIX.select("a", "b"), sel(pl.fold(ZERO, add, pl.all()))),
        case("reduce", FIX, sel(pl.reduce(add, [a, b]))),
        case("cum_fold", FIX, lambda df: df.select(pl.cum_fold(ZERO, add, [a, b])).pipe(fields("cum_fold", "a", "b"))),
        case("cum_reduce", FIX, lambda df: df.select(pl.cum_reduce(add, [a, b])).pipe(fields("cum_reduce", "a", "b"))),
        case("cum_sum_horizontal", FIX, lambda df: df.select(pl.cum_sum_horizontal(a, f)).pipe(fields("cum_sum", "a", "f"))),
        case("fold_agg", FIX, sel(pl.fold(ZERO, add, [a.sum(), b.sum()]))),
        case("fold_gen", gen, sel(pl.fold(ZERO, add, [a, b, c("c")]).alias("fold"), pl.fold(f, mul, [a, b]).alias("mul"),
                                  pl.reduce(add, [a, b, c("c")]).alias("reduce"))),
        case("cum_fold_gen", gen, lambda df: df.select(pl.cum_fold(ZERO, add, [a, b, c("c")])).pipe(fields("cum_fold", "a", "b", "c"))),
        case("cum_sum_horizontal_gen", gen, lambda df: df.select(pl.cum_sum_horizontal(a, f, b)).pipe(fields("cum_sum", "a", "f", "b"))),
        # --- format -----------------------------------------------------------------------
        case("format_two", FIX, sel(pl.format("{} and {}", a, s))),
        case("format_text_around", FIX, sel(pl.format("n={}!", f))),
        case("format_escaped", FIX, sel(pl.format("{{}} {}", a))),
        case("format_plain", FIX, sel(a, pl.format("plain"))),
        case("format_gen", gen, sel(pl.format("{}-{}", a, s).alias("as"), pl.format("[{}]", f).alias("f"))),
    ]
