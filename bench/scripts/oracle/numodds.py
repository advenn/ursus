"""The numodds group: round's modes, truncate, round_sig_figs, cot and the one-sided
clips (numodds_test.go, M5 of v0.6.6-scope.md). Every literal frame is the one the Go
test held by hand, and a generated case follows each function's.

Every float is compared by its bits unless the case says why not: cot is Go's tangent
against glibc's, within four ulps over 20,000 values (rel:1e-15 is at most nine).
"""

from __future__ import annotations

import math

import polars as pl

from . import Case, case
from .gen import Gen

nan, inf, negz = float("nan"), float("inf"), -0.0
MAXF = 1.7976931348623157e308
MAXI64, MINI64 = (1 << 63) - 1, -(1 << 63)


def f64(vals: list, name: str = "x") -> pl.DataFrame:
    return pl.DataFrame({name: pl.Series(name, vals, dtype=pl.Float64)})


def typed(vals: list, dtype: pl.DataType, name: str = "x") -> pl.DataFrame:
    return pl.DataFrame({name: pl.Series(name, vals, dtype=dtype)})


def sel(e: pl.Expr):
    return lambda df: df.select(e)


x = pl.col("x")
EVEN, AWAY = "half_to_even", "half_away_from_zero"

TIES = f64([0.5, 1.5, 2.5, -0.5, -2.5, 2.675, 0.125, 1e300, nan, None, -0.4])
EXACT = f64([1.005, 1.015, 1.025, 0.285, 1.0000000000000002, 4503599627370497.0, 4503599627370496.5])
SUB = f64([1.5e-310, 2.5e-310, -1.5e-310, 1.25e-305, 0.1, 3.0])
TRUNC = f64([0.25, -0.25, 0.99, -0.99, 2.675, -2.675, 1e300, nan, inf, None, negz, 1.5e300, 123.456, 0.29])
THIRDS = f64([0.30000000000000004, 1.0 / 3, 2.0 / 3, 123456.789e-10])
F32 = typed([0.125, 2.5, 1e-30], pl.Float32)

MIXED = f64([1234.5678, -0.0012345, 0.0, negz, nan, inf, None, 9.995, 1e-320, MAXF])
EDGES = f64([1.5e-315, 1.7e308, 9.6e307, 0.95, 99.5, 123.456, 1e-5, 5e-324, 2.5e-308, 8.5e307, -1.7e308, 1.23456789e-310])
TENS = f64([1000.0, 100.0, 0.001, 1e23, 1e22, 1e-7, 999.9999999999999, 1e308, 1e-300])
HALVES = f64([0.125, 0.135, 2.5, 3.5, 15.0, 25.0, -2.5])
MANY = f64([1000.0, 1e5, 1e-3, 1e10, 1e20, 1e-10, 100.0])
I64 = typed([123456, -987, 0, None, (1 << 62) + 12345], pl.Int64)
ENDS = typed([MAXI64, MINI64, 999, 950, 949, -950], pl.Int64)

COT = f64([0.0, negz, 1.0, None, nan, 2.0, -3.0, 1e10, 1e300, 0.001])

CLIP = pl.DataFrame({
    "x": pl.Series([1, 5, 10, None], dtype=pl.Int64),
    "lo": pl.Series([2, None, 3, 1], dtype=pl.Int64),
    "f": pl.Series([1.0, nan, -1.0, None], dtype=pl.Float64),
})


def sig_figs_1_to_18(v: float) -> pl.DataFrame:
    return f64([v])


def cases() -> list[Case]:
    g = Gen(4961)
    gen_floats = g.frame(g.floats(240))
    gen_f32 = g.frame(g.floats(120, dtype=pl.Float32))
    gen_i64 = g.frame(g.ints(120, pl.Int64))
    gen_i16 = g.frame(g.ints(120, pl.Int16))
    gen_u8 = g.frame(g.ints(120, pl.UInt8))
    gen_cot = g.frame(g.floats(240, lo=-50, hi=50, edges=False))
    gen_clip = g.frame(g.ints(150, pl.Int64, lo=-100, hi=100, name="x"), g.ints(150, pl.Int64, lo=-50, hi=0, name="lo"),
                       g.ints(150, pl.Int64, lo=0, hi=50, name="hi"), g.floats(150, lo=-10, hi=10, name="f"))
    out = [
        # --- round's modes and truncate ---------------------------------------------
        case("round_even_0", TIES, sel(x.round(0, mode=EVEN))),
        case("round_even_2", TIES, sel(x.round(2, mode=EVEN))),
        case("round_away_0", TIES, sel(x.round(0, mode=AWAY))),
        case("round_away_2", TIES, sel(x.round(2, mode=AWAY))),
        case("round_even_2_exact", EXACT, sel(x.round(2, mode=EVEN)), note="the floats there are, not the decimals"),
        case("round_even_0_past_2p52", EXACT, sel(x.round(0, mode=EVEN))),
        case("round_even_15_thirds", THIRDS, sel(x.round(15, mode=EVEN))),
        case("round_even_23_thirds", THIRDS, sel(x.round(23, mode=EVEN)), note="past 10^22 the scale is inexact"),
        case("round_even_300_sub", SUB, sel(x.round(300, mode=EVEN)), note="2^d and 5^d apart"),
        case("round_even_310_sub", SUB, sel(x.round(310, mode=EVEN))),
        case("round_away_310_sub", SUB, sel(x.round(310, mode=AWAY))),
        case("round_even_325_sub", SUB, sel(x.round(325, mode=EVEN))),
        case("round_away_326_sub", SUB, sel(x.round(326, mode=AWAY)), note="past every double's digits: as it is"),
        case("round_even_20_big", f64([1.5e300, 0.1]), sel(x.round(20, mode=EVEN))),
        case("truncate_0", TRUNC, sel(x.truncate(0))),
        case("truncate_1", TRUNC, sel(x.truncate(1))),
        case("truncate_2", TRUNC, sel(x.truncate(2))),
        case("truncate_20", TRUNC, sel(x.truncate(20))),
        case("truncate_30_thirds", THIRDS, sel(x.truncate(30))),
        case("truncate_310_sub", SUB, sel(x.truncate(310))),
        case("round_f32_even_2", F32, sel(x.round(2, mode=EVEN))),
        case("round_f32_even_0", F32, sel(x.round(0, mode=EVEN))),
        case("round_f32_even_25", F32, sel(x.round(25, mode=EVEN))),
        case("round_f32_even_46", F32, sel(x.round(46, mode=EVEN))),
        case("round_f32_even_47", F32, sel(x.round(47, mode=EVEN)), note="past a float32's digits: as it is"),
        case("truncate_f32_2", typed([0.29, -2.675], pl.Float32), sel(x.truncate(2))),
        case("truncate_int_1", typed([5, -5], pl.Int64), sel(x.truncate(1))),
        case("round_int_even_1", typed([5, -5], pl.Int64), sel(x.round(1, mode=EVEN))),
        case("round_modes_gen", gen_floats, lambda df: df.select(
            x.round(0, mode=EVEN).alias("e0"), x.round(2, mode=EVEN).alias("e2"), x.round(15, mode=EVEN).alias("e15"),
            x.round(0, mode=AWAY).alias("a0"), x.round(3, mode=AWAY).alias("a3"),
            x.truncate(0).alias("t0"), x.truncate(2).alias("t2"), x.truncate(12).alias("t12"))),
        case("round_f32_gen", gen_f32, lambda df: df.select(
            x.round(0, mode=EVEN).alias("e0"), x.round(2, mode=AWAY).alias("a2"), x.truncate(1).alias("t1"))),
        # --- round_sig_figs ---------------------------------------------------------
        case("sig_figs_1_mixed", MIXED, sel(x.round_sig_figs(1))),
        case("sig_figs_2_mixed", MIXED, sel(x.round_sig_figs(2))),
        case("sig_figs_3_mixed", MIXED, sel(x.round_sig_figs(3))),
        case("sig_figs_1_edges", EDGES, sel(x.round_sig_figs(1))),
        case("sig_figs_2_edges", EDGES, sel(x.round_sig_figs(2))),
        case("sig_figs_20_edges", EDGES, sel(x.round_sig_figs(20))),
        case("sig_figs_1_tens", TENS, sel(x.round_sig_figs(1))),
        case("sig_figs_3_tens", TENS, sel(x.round_sig_figs(3))),
        case("sig_figs_17_tens", TENS, sel(x.round_sig_figs(17))),
        case("sig_figs_17_many", MANY, sel(x.round_sig_figs(17)), note="each power of ten's own exponent"),
        case("sig_figs_20_many", MANY, sel(x.round_sig_figs(20))),
        case("sig_figs_23_many", MANY, sel(x.round_sig_figs(23))),
        case("sig_figs_25_many", MANY, sel(x.round_sig_figs(25))),
        case("sig_figs_30_many", MANY, sel(x.round_sig_figs(30))),
        case("sig_figs_2_halves", HALVES, sel(x.round_sig_figs(2))),
        case("sig_figs_1_halves", HALVES, sel(x.round_sig_figs(1))),
        case("sig_figs_1e23_1_to_18", f64([1e23]),
             lambda df: df.select(x.round_sig_figs(d).alias(f"d{d}") for d in range(1, 19)),
             note="log10 scaled from log2 falls short of the exponent at 10^23"),
        case("sig_figs_1e46_1_to_18", f64([1e46]),
             lambda df: df.select(x.round_sig_figs(d).alias(f"d{d}") for d in range(1, 19))),
        case("sig_figs_f32_1", typed([3.4e38, 1.5e-44, 0.125, 2.5], pl.Float32), sel(x.round_sig_figs(1))),
        case("sig_figs_i64_2", I64, sel(x.round_sig_figs(2)), note="through a float64 and back, saturating"),
        case("sig_figs_i64_ends_1", ENDS, sel(x.round_sig_figs(1))),
        case("sig_figs_i64_ends_2", ENDS, sel(x.round_sig_figs(2))),
        case("sig_figs_i64_19", typed([MAXI64, 1234567890123456789], pl.Int64), sel(x.round_sig_figs(19))),
        case("sig_figs_u8_1", typed([251, 250, 249, 15, 0], pl.UInt8), sel(x.round_sig_figs(1))),
        case("sig_figs_u8_2", typed([251, 250, 249, 15, 0], pl.UInt8), sel(x.round_sig_figs(2))),
        case("sig_figs_i8_1", typed([127, -128, -127, 125, 99, -99], pl.Int8), sel(x.round_sig_figs(1))),
        case("sig_figs_i8_2", typed([127, -128, -127, 125, 99, -99], pl.Int8), sel(x.round_sig_figs(2))),
        case("sig_figs_i16_1", typed([32767, 32749, 32750], pl.Int16), sel(x.round_sig_figs(1))),
        case("sig_figs_i16_2", typed([32767, 32749, 32750], pl.Int16), sel(x.round_sig_figs(2))),
        case("sig_figs_u64_1", typed([(1 << 64) - 1, (1 << 63) + 5], pl.UInt64), sel(x.round_sig_figs(1))),
        case("sig_figs_u64_2", typed([(1 << 64) - 1, (1 << 63) + 5], pl.UInt64), sel(x.round_sig_figs(2))),
        case("sig_figs_sum_2", typed([123456, 1000], pl.Int64), sel(x.sum().round_sig_figs(2).cast(pl.Int64)),
             note="ursus's integer Sum is an Int128 where Polars' is an Int64: the figures alike, cast back"),
        case("sig_figs_gen", gen_floats, lambda df: df.select(
            x.round_sig_figs(1).alias("d1"), x.round_sig_figs(2).alias("d2"), x.round_sig_figs(3).alias("d3"),
            x.round_sig_figs(7).alias("d7"), x.round_sig_figs(17).alias("d17"))),
        case("sig_figs_ints_gen", g.frame(gen_i64["x"], gen_i16["x"].alias("i16"), gen_u8["x"].alias("u8")),
             lambda df: df.select(
                 x.round_sig_figs(1).alias("x1"), x.round_sig_figs(3).alias("x3"), x.round_sig_figs(19).alias("x19"),
                 pl.col("i16").round_sig_figs(1).alias("i16_1"), pl.col("i16").round_sig_figs(2).alias("i16_2"),
                 pl.col("u8").round_sig_figs(1).alias("u8_1"), pl.col("u8").round_sig_figs(2).alias("u8_2"))),
        # --- cot --------------------------------------------------------------------
        case("cot", COT, sel(x.cot()), float="rel:1e-15", note="Go's tangent is within four ulps of glibc's"),
        case("cot_pole", f64([math.pi / 2]), sel(x.cot()), float="rel:1e-14",
             note="near a multiple of pi/2 Go reduces the argument in three parts, and its relative error grows"),
        case("cot_ints", I64, sel(x.cot()), float="rel:1e-15", note="a Float64 of an integer"),
        case("cot_f32", typed([1.0], pl.Float32), sel(x.cot()), note="a Float32's cot is a Float32, to the bit"),
        case("cot_gen", gen_cot, sel(x.cot()), float="rel:1e-15", note="Go's tangent against glibc's"),
        # --- clip -------------------------------------------------------------------
        case("clip_lower_3", CLIP, sel(x.clip(lower_bound=3))),
        case("clip_upper_6", CLIP, sel(x.clip(upper_bound=6))),
        case("clip_lower_col", CLIP, sel(x.clip(lower_bound=pl.col("lo"))), note="a bound null in a row"),
        case("clip_col_8", CLIP, sel(x.clip(pl.col("lo"), 8))),
        case("clip_null_lower_6", CLIP, sel(x.clip(pl.lit(None, dtype=pl.Int64), 6))),
        case("clip_3_null_upper", CLIP, sel(x.clip(3, pl.lit(None, dtype=pl.Int64)))),
        case("clip_8_2", CLIP, sel(x.clip(8, 2)), note="lo above hi"),
        case("clip_f_0_05", CLIP, sel(pl.col("f").clip(0.0, 0.5)), note="NaN kept"),
        case("clip_f_lower_0", CLIP, sel(pl.col("f").clip(lower_bound=0.0))),
        case("clip_f_lower_nan", CLIP, sel(pl.col("f").clip(lower_bound=nan)), note="a NaN bound clips nothing"),
        case("clip_i32_lower_3", CLIP, sel(x.cast(pl.Int32).clip(lower_bound=3)), note="a Go bound is weak: an Int32 stays one"),
        case("clip_gen", gen_clip, lambda df: df.select(
            x.clip(lower_bound=-10).alias("lo10"), x.clip(upper_bound=10).alias("hi10"),
            x.clip(pl.col("lo"), pl.col("hi")).alias("cols"), x.clip(pl.col("lo")).alias("lo_col"),
            pl.col("f").clip(-1.5, 1.5).alias("f"), pl.col("f").clip(lower_bound=nan).alias("f_nan"))),
    ]
    return out
