"""The math group: the elementwise maths over the adversarial corpus (math_test.go),
abs, neg and sign of every numeric type and of an integer Sum, the float remainder
and floor division, pow, and round's edges. Every literal frame is the Go test's, a
generated case beside each; the Go oracle against the math package, the type checks
and the refusals stay in the Go test.

sqrt, floor, ceil, abs, neg and sign are exact in both and compared by bits; cbrt,
exp, log, log10 and log1p are Go's against glibc's, within a few ulps, held to
rel:1e-15 (rel:1e-6 at Float32, where each rounds its own double once). pow's
integer -1 is refused by Polars 2.1 ("conversion from i32 to u32"); the float -1.0 is
asked, and ursus's integer -1 answers the same, Pow always a float (feature-gaps.md
§9). The float remainder's divisor is positive in the generated case: a zero
remainder's sign differs (scalar_byhand's float_mod_zero_sign).
"""

from __future__ import annotations

import math

import polars as pl

from . import Case, case
from .gen import Gen

nan, inf, negz = float("nan"), float("inf"), -0.0
c = pl.col
v = c("v")

CORPUS = [
    0.0, negz, 1, -1, 2, 4, 9, 0.5, -0.5, 1.5, 2.5, -2.5, 0.125, 0.375, 2.675, 1.005, math.pi, math.e, 1e-300, 1e300, -1e300,
    5e-324, 5e-324 * 3, 1.7976931348623157e308, -4, -0.25, -2, inf, -inf, nan,
]
F32_CORPUS = [3.4028234663852886e38, 1.401298464324817e-45, 1.1754943508222875e-38, 89, -103, 0.1, 3, 10]
EXACT = {"sqrt": v.sqrt(), "floor": v.floor(), "ceil": v.ceil(), "abs": v.abs(), "neg": -v, "sign": v.sign()}
LIBM = {"cbrt": v.cbrt(), "exp": v.exp(), "ln": v.log(), "log10": v.log10(), "log1p": v.log1p()}
LIBM_NOTE = "Go's cbrt, exp and logs against glibc's"


def typed(vals: list, dtype: pl.DataType, name: str = "v") -> pl.DataFrame:
    return pl.DataFrame({name: pl.Series(name, vals, dtype=dtype)})


def sel(*e: pl.Expr):
    return lambda df: df.select(*e)


def named(ops: dict[str, pl.Expr]):
    return sel(*(e.alias(n) for n, e in ops.items()))


def f32_corpus() -> list[float]:
    seen, out = set(), []
    for f in CORPUS + F32_CORPUS:
        f32 = pl.Series([f], dtype=pl.Float32)[0]
        if f32 == f32 and f32 in seen:
            continue
        seen.add(f32)
        out.append(f)
    return out


MOD = pl.DataFrame({"a": [7.0, -7.0, 7.0, 5.5], "b": [2.0, 2.0, 0.0, 2.0], "ia": pl.Series([7, -7, 7, 5], dtype=pl.Int64),
                    "ib": pl.Series([2, 2, 0, 2], dtype=pl.Int64)})
EVERY = pl.DataFrame({"i8": pl.Series([-3], dtype=pl.Int8), "i16": pl.Series([-3], dtype=pl.Int16), "i32": pl.Series([-3], dtype=pl.Int32),
                      "i64": pl.Series([-3], dtype=pl.Int64), "u8": pl.Series([3], dtype=pl.UInt8), "u16": pl.Series([3], dtype=pl.UInt16),
                      "u32": pl.Series([3], dtype=pl.UInt32), "u64": pl.Series([3], dtype=pl.UInt64),
                      "f32": pl.Series([-3], dtype=pl.Float32), "f64": pl.Series([-3], dtype=pl.Float64)})
SIGNED = ["i8", "i16", "i32", "i64", "f32", "f64"]


def cases() -> list[Case]:
    g = Gen(4979)
    gen = g.frame(g.floats(240, lo=-1e3, hi=1e3, name="v"))
    gen_f32 = g.frame(g.floats(120, dtype=pl.Float32, name="v"))
    # The dividend stays small: Polars' remainder is a - b*floor(a/b), which is 0 or
    # garbage once the quotient's ulp passes the divisor, past 2^53.
    gen_mod = g.frame(g.floats(150, lo=-100, hi=100, edges=False, name="a"), g.floats(150, lo=0.5, hi=10, edges=False, name="b"),
                      g.ints(150, pl.Int64, lo=-100, hi=100, name="ia"), g.ints(150, pl.Int64, lo=-7, hi=7, name="ib"))
    gen_mod = gen_mod.with_columns(pl.Series("a", g._place(gen_mod["a"].to_list(), 0.0, [nan, inf, -inf, -0.0, 0.0, 0.5, -0.5, 2.5, -2.5, 99.5]), dtype=pl.Float64))
    gen_types = g.frame(g.ints(100, pl.Int8, name="i8"), g.ints(100, pl.Int32, name="i32"), g.ints(100, pl.UInt32, name="u32"),
                        g.ints(100, pl.UInt64, name="u64"), g.floats(100, dtype=pl.Float32, name="f32"))
    gen_pow = g.frame(g.ints(100, pl.Int64, lo=-20, hi=20, name="v"), g.floats(100, lo=-5, hi=5, name="f"))
    return [
        # --- the corpus ------------------------------------------------------------------
        case("corpus_exact", typed(CORPUS, pl.Float64), named(EXACT)),
        case("corpus_libm", typed(CORPUS, pl.Float64), named(LIBM), float="rel:1e-15", note=LIBM_NOTE),
        case("corpus_f32_exact", typed(f32_corpus() + [None], pl.Float32), named(EXACT), note="a trailing null stays null"),
        case("corpus_f32_libm", typed(f32_corpus() + [None], pl.Float32), named(LIBM), float="rel:1e-6",
             note="each rounds its own double once to a float32: an ulp of a float32 apart at most"),
        case("math_gen", gen, named(EXACT)),
        case("libm_gen", gen, named(LIBM), float="rel:1e-15", note=LIBM_NOTE),
        case("math_f32_gen", gen_f32, named(EXACT)),
        case("libm_f32_gen", gen_f32, named(LIBM), float="rel:1e-6", note="each rounds its own double once to a float32"),
        # --- round's edges ------------------------------------------------------------------
        case("round_int_identity", typed([7, -7], pl.Int64, "i"), sel(c("i").round(3))),
        case("round_400", typed([1.23456, nan], pl.Float64, "f"), sel(c("f").round(400)), note="a count too large to scale: the value"),
        case("round_gen", gen, sel(v.round(400).alias("r400"), v.round(0, mode="half_away_from_zero").alias("r0"))),
        # --- abs, neg and sign of every numeric type, and of a Sum ----------------------------
        case("abs_sign_every_type", EVERY, sel(*(c(n).abs().alias("abs_" + n) for n in EVERY.columns),
                                               *(c(n).sign().alias("sign_" + n) for n in EVERY.columns),
                                               *((-c(n)).alias("neg_" + n) for n in SIGNED))),
        case("sum_abs_neg_sign_sqrt", pl.DataFrame({"g": ["a", "a"], "v": pl.Series([-3, 1], dtype=pl.Int64)}),
             lambda df: df.group_by("g").agg(v.sum().abs().alias("abs"), (-v.sum()).alias("neg"), v.sum().sign().alias("sign"),
                                             v.sum().sqrt().alias("sqrt")),
             note="an integer Sum is an Int128 here, an Int64 in Polars; its sqrt a Float64 in both"),
        case("u32_abs_sign", typed([0, 7, 4294967295], pl.UInt32, "u"), sel(c("u").abs().alias("a"), c("u").sign().alias("s"))),
        case("abs_sign_gen", gen_types, sel(*(c(n).abs().alias("abs_" + n) for n in gen_types.columns),
                                            *(c(n).sign().alias("sign_" + n) for n in gen_types.columns),
                                            (-c("i8")).alias("neg_i8"), (-c("i32")).alias("neg_i32"), (-c("f32")).alias("neg_f32"))),
        # --- the remainder and floor division -----------------------------------------------
        case("float_mod_floordiv", MOD, sel((c("a") % c("b")).alias("fmod"), (c("a") // c("b")).alias("fdiv"), (c("ia") % c("ib")).alias("imod")),
             note="the floored remainder takes the divisor's sign; by zero NaN and an infinity, an integer's null"),
        case("floordiv_stays_finite", pl.DataFrame({"a": [1e300, -1e300, 1e18], "b": [1.0, 1.0, 1.0]}), sel((c("a") // c("b")).alias("fd")),
             note="a value past int64's range is already an integer"),
        case("mod_floordiv_gen", gen_mod, sel((c("a") % c("b")).alias("fmod"), (c("a") // c("b")).alias("fdiv"),
                                              (c("ia") % c("ib")).alias("imod"), (c("ia") // c("ib")).alias("idiv")), float="rel:1e-9",
             note="Polars' remainder is a - b*floor(a/b), inexact in its last bits, where ursus's fmod is exact"),
        # --- pow ---------------------------------------------------------------------------
        case("pow", typed([2, 4, 10], pl.Int64), sel(v.pow(-1.0).alias("inv"), v.pow(0.5).alias("root"), v.pow(2).alias("sq")),
             note="2**-1 is 0.5; the square is an Int64 in Polars and a Float64 here, Pow always a float"),
        case("pow_gen", gen_pow, sel(v.pow(2.0).alias("sq"), v.pow(0.5).alias("root"), c("f").pow(c("f")).alias("ff"), c("f").pow(3.0).alias("cube")),
             float="rel:1e-14", note="Go's pow is within a few ulps of glibc's"),
    ]
