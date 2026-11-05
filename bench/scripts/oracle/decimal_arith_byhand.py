"""The decimal_arith_byhand group: Decimal arithmetic where Polars 2.1 has the exact
answer (decimal_arith_byhand_test.go, v0.3-scope.md §3 item 2). A sum, a difference
and a product by an integer are exact in both; Polars types every one Decimal(38, s)
and ursus a digit wider than the operands' (+ and -) or every digit of the product
(*), the rule decimal_arith_test.go holds against big.Rat, so Polars' answer is cast
to ursus's type in the query, as numodds casts the Int128 sum: the digits are the
question here. Comparisons, a relaxed concat and a conditional meet exactly.

Not here, with the reason: a product of two Decimals, which Polars rounds to the wider
scale (1.25 × 2.125 is 2.656 there and 2.65625 here), and a quotient, which Polars
rounds to a Decimal and ursus answers as the nearest Float64, by zero an infinity or
NaN where Polars refuses: both against big.Rat in decimal_arith_test.go, over the
types' edges and seeded values. A join of Decimal(10, 2) to Decimal(12, 3) keys, which
Polars refuses and ursus matches at the value (a count in the Go test); the refusals.
"""

from __future__ import annotations

import decimal

import polars as pl

from . import Case, case
from .gen import Gen

D = decimal.Decimal
c = pl.col
a, b, i = c("a"), c("b"), c("i")


def dec(vals: list[str], precision: int, scale: int, name: str) -> pl.Series:
    return pl.Series(name, [None if v is None else D(v) for v in vals], dtype=pl.Decimal(precision, scale))


AB = pl.DataFrame([dec(["1.25", "-3.50"], 10, 2, "a"), dec(["2.125", "0.375"], 12, 3, "b"), pl.Series("i", [2, 3], dtype=pl.Int64)])


def sel(*e: pl.Expr):
    return lambda df: df.select(*e)


def cases() -> list[Case]:
    g = Gen(4982)
    gen = g.frame(g.decimals(150, 12, 4, name="a"), g.decimals(150, 10, 2, name="b"), g.ints(150, pl.Int64, lo=-100, hi=100, name="i"))
    return [
        # --- + and -: a digit wider than the operands -----------------------------------
        case("add", AB, sel((a + b).cast(pl.Decimal(13, 3)))),
        case("sub", AB, sel((a - b).cast(pl.Decimal(13, 3)))),
        case("add_same_type", AB, sel((a + a).cast(pl.Decimal(11, 2)))),
        case("add_max_10_2", pl.DataFrame([dec(["99999999.99"], 10, 2, "a")]), sel((a + a).cast(pl.Decimal(11, 2)))),
        case("add_rescale_past_128_bits", pl.DataFrame([dec(["17014118346046923173168730372"], 38, 0, "a"),
                                                        dec(["-9999999999999999999999999999.9999999999"], 38, 10, "b")]), sel(a + b),
             note="the left operand at scale 10 is 1.70e38, past 128 bits, and the sum still has 38 digits"),
        case("add_int_col", AB, sel((a + i).cast(pl.Decimal(22, 2)))),
        case("add_int_lit", AB, sel((a + 1).cast(pl.Decimal(22, 2)))),
        case("add_float_lit", AB, sel(a + 0.5), note="a float beside a Decimal is a Float64"),
        case("add_sub_gen", gen, sel((a + b).cast(pl.Decimal(13, 4)).alias("add"), (a - b).cast(pl.Decimal(13, 4)).alias("sub"),
                                     (a + i).cast(pl.Decimal(24, 4)).alias("add_i"), (a + 0.5).alias("add_f"))),
        # --- *: by an integer, every digit of the product ------------------------------------
        case("mul_int_col", AB, sel((a * i).cast(pl.Decimal(29, 2)))),
        case("mul_int_lit", AB, sel((a * 2).cast(pl.Decimal(29, 2)))),
        case("mul_int_gen", gen, sel((a * i).cast(pl.Decimal(31, 4)).alias("mul_i"), (b * 3).cast(pl.Decimal(29, 2)).alias("mul_3"))),
        # --- comparisons, a concat, a conditional: the values meet exactly ---------------------
        case("gt", AB, sel(a > b)),
        case("eq_1_00_1_000", pl.DataFrame([dec(["1.00"], 10, 2, "a"), dec(["1.000"], 12, 3, "b")]), sel(a == b)),
        case("eq_float_lit", AB, sel(a == 1.25)),
        case("concat_relaxed", {"left": AB.select(a.alias("x")), "right": AB.select(b.alias("x"))},
             lambda left, right: pl.concat([left, right], how="vertical_relaxed")),
        case("when_two_types", AB, sel(pl.when(i == 2).then(a).otherwise(b).alias("x"))),
        case("compare_gen", gen, sel((a > b).alias("gt"), (a == b).alias("eq"), (a <= b).alias("le"), (a == 0).alias("zero"))),
        case("when_gen", gen, sel(pl.when(i > 0).then(a).otherwise(b).alias("x"))),
    ]
