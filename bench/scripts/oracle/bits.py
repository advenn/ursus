"""The bits group, M2 of v0.6.6-scope.md (step 278): And, Or, Xor and Not of integers
are their bits', of Booleans Kleene's (bits_test.go). The literal frame is the Go
test's; a generated case follows each. The refusals (a Boolean beside an integer, a
float) stay in the Go test.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
a, b, u, i8 = c("a"), c("b"), c("u"), c("i8")

FRAME = pl.DataFrame({"a": pl.Series([12, -1, None, 5], dtype=pl.Int64), "b": pl.Series([10, 3, 7, None], dtype=pl.Int64),
                      "u": pl.Series([12, 255, 0, 5], dtype=pl.UInt8), "i8": pl.Series([12, -1, 0, 5], dtype=pl.Int8)})
BOOLS = pl.DataFrame({"p": [True, False, None]})


def sel(*e: pl.Expr):
    return lambda df: df.select(*e)


def cases() -> list[Case]:
    g = Gen(4981)
    gen = g.frame(g.ints(150, pl.Int64, name="a"), g.ints(150, pl.Int64, name="b"), g.ints(150, pl.UInt8, name="u"),
                  g.ints(150, pl.Int8, name="i8"), g.ints(150, pl.Int32, name="i32"), g.ints(150, pl.UInt16, name="u16"))
    gen_bools = g.frame(g.bools(150, null_rate=0.2, name="p"), g.bools(150, null_rate=0.2, name="q"))
    gen_i128 = g.frame(g.ints(100, pl.Int128, name="x"), g.ints(100, pl.Int128, name="y"))
    return [
        case("and", FRAME, sel((a & b).alias("r"))),
        case("or", FRAME, sel((a | b).alias("r"))),
        case("xor", FRAME, sel((a ^ b).alias("r"))),
        case("not", FRAME, sel((~a).alias("r"))),
        case("not_u8", FRAME, sel((~u).alias("r"))),
        case("and_const", FRAME, sel((a & 6).alias("r"))),
        case("u8_and_i8", FRAME, sel((u & i8).alias("r")), note="a UInt8 and an Int8 meet at Int16"),
        case("u8_or_i64", FRAME, sel((u | a).alias("r"))),
        case("not_not", FRAME, sel((~~a).alias("r"))),
        case("u8_and_const", FRAME, sel((u & 6).alias("r")), note="a Go integer beside a UInt8 is a UInt8, as Polars keeps it"),
        case("i128", pl.DataFrame({"x": pl.Series([5], dtype=pl.Int128)}), sel(c("x") & pl.lit(3, dtype=pl.Int128), (~c("x")).alias("n"))),
        case("null_and", FRAME, sel((pl.lit(None) & a).alias("r")), note="a null of no type beside an integer is a null integer"),
        case("bool_kleene", BOOLS, sel((c("p") & False).alias("and"), (c("p") | True).alias("or"), (~c("p")).alias("not"))),
        case("bits_gen", gen, sel((a & b).alias("and"), (a | b).alias("or"), (a ^ b).alias("xor"), (~a).alias("not"), (~u).alias("not_u"),
                                  (u & i8).alias("u_and_i8"), (u | a).alias("u_or_a"), (a & 6).alias("a_and_6"), (u & 6).alias("u_and_6"),
                                  (c("i32") ^ c("u16")).alias("i32_xor_u16"), (~c("u16")).alias("not_u16"))),
        case("bits_i128_gen", gen_i128, sel((c("x") & c("y")).alias("and"), (c("x") | c("y")).alias("or"), (c("x") ^ c("y")).alias("xor"), (~c("x")).alias("not"))),
        case("bool_kleene_gen", gen_bools, sel((c("p") & c("q")).alias("and"), (c("p") | c("q")).alias("or"), (~c("p")).alias("not"),
                                               (c("p") & False).alias("and_f"), (c("p") | True).alias("or_t"))),
    ]
