"""The seq group: rle and rle_id, append and extend_constant (seq_test.go, M4 of
v0.6.6-scope.md). A rle's struct is unnested on both sides.

Not here: Sample and Shuffle, whose draws are ursus's own (feature-gaps.md §9), held to
their sizes and membership in the Go test; the refusals.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

nan, negz = float("nan"), -0.0
c = pl.col
a, f, s, g = c("a"), c("f"), c("s"), c("g")

SEQ = pl.DataFrame({
    "g": pl.Series([1, 1, 1, 2, 2, 2], dtype=pl.Int64),
    "a": pl.Series([1, 1, 2, None, None, 1], dtype=pl.Int64),
    "f": pl.Series([1.0, nan, nan, negz, 0.0, 2.0], dtype=pl.Float64),
    "s": pl.Series(["x", "x", "y", "y", None, "x"], dtype=pl.String),
})
ONE = pl.DataFrame({"a": pl.Series([1], dtype=pl.Int64)})


def sel(*exprs: pl.Expr):
    return lambda df: df.select(*exprs)


def by_g(*exprs: pl.Expr):
    return lambda df: df.group_by("g").agg(*exprs).sort("g")


def runs(col: str):
    return lambda df: df.select(c(col).rle().alias("r")).unnest("r")


def cases() -> list[Case]:
    gen = Gen(5008)
    n = 160
    gen_seq = gen.frame(gen.ints(n, pl.Int64, lo=0, hi=3, null_rate=0, edges=False, name="g"), gen.ints(n, pl.Int64, lo=0, hi=2, name="a"),
                        gen.floats(n, lo=0, hi=1, ties=True, name="f"), gen.strings(n, alphabet="xy", maxlen=1, name="s"),
                        gen.ints(n, pl.Int64, lo=-5, hi=5, null_rate=0, edges=False, name="b"))
    return [
        # --- rle and rle_id -----------------------------------------------------------------------
        case("rle", SEQ, runs("a")),
        case("rle_nan_zeros", SEQ, runs("f"), note="NaN runs with NaN, -0 with 0"),
        case("rle_text", SEQ, runs("s")),
        case("rle_id", SEQ, sel(a.rle_id())),
        case("rle_id_nan_zeros", SEQ, sel(f.rle_id())),
        case("rle_id_over_g", SEQ, sel(a.rle_id().over("g"))),
        case("rle_by_group", SEQ, lambda df: df.group_by("g").agg(a.rle()).sort("g").explode("a").unnest("a")),
        case("rle_id_no_rows", SEQ.filter(g > 5), sel(a.rle_id())),
        case("rle_id_struct", SEQ, sel(pl.struct("a", "s").rle_id()), note="a Struct's runs, field by field (step 344)"),
        # --- append and extend_constant -----------------------------------------------------------
        case("append", SEQ, sel(a.append(g, upcast=True))),
        case("append_constant", SEQ, sel(a.append(pl.lit(9, dtype=pl.Int64), upcast=True))),
        case("append_constant_by_group", SEQ, by_g(a.append(pl.lit(9, dtype=pl.Int64), upcast=True))),
        case("append_by_group", SEQ, by_g(a.append(g, upcast=True))),
        case("append_float", SEQ, sel(a.append(f, upcast=True)), note="an Int64 appended by a Float64 meets at Float64"),
        case("extend_constant", SEQ, sel(a.extend_constant(7, 2))),
        case("extend_null", SEQ, sel(a.extend_constant(None, 1))),
        case("extend_float_truncated", SEQ, sel(a.extend_constant(1.5, 1)), note="a float fill of an integer column, truncated"),
        case("extend_negative_truncated", ONE, sel(a.extend_constant(-1.5, 1))),
        case("extend_past_int8", ONE.select(a.cast(pl.Int8)), sel(a.extend_constant(300, 1)), note="past the type: null"),
        case("extend_number_to_text", pl.DataFrame({"a": ["x"]}), sel(a.extend_constant(5, 1))),
        case("extend_n_expression", SEQ, sel(a.extend_constant(0, pl.lit(2, dtype=pl.Int64)))),
        case("extend_by_group", SEQ, by_g(a.extend_constant(0, 1))),
        case("extend_float_by_group", SEQ, by_g(a.extend_constant(-1.5, 1))),
        # --- generated --------------------------------------------------------------------------------
        case("rle_id_gen", gen_seq, sel(a.rle_id().alias("a"), f.rle_id().alias("f"), s.rle_id().alias("s"), a.rle_id().over("g").alias("ag"),
                                        pl.struct("a", "s").rle_id().alias("as"))),
        case("rle_gen", gen_seq, runs("a")),
        case("rle_text_gen", gen_seq, runs("s")),
        case("rle_by_group_gen", gen_seq, lambda df: df.group_by("g").agg(a.rle()).sort("g").explode("a").unnest("a")),
        case("append_gen", gen_seq, sel(a.append(c("b"), upcast=True))),
        case("extend_gen", gen_seq, sel(a.extend_constant(-3, 3))),
        case("extend_by_group_gen", gen_seq, by_g(a.extend_constant(9, 2))),
    ]
