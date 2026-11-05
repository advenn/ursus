"""The castround_byhand group: a float is not a go-between, audit.md §6's S1, S2 and
S4 (castround_byhand_test.go). A cast to a float rounds once to the nearest value of
that width, from a float, an integer, an Int128 or a Decimal; a string parses at the
width it is cast to; a Float32's maths is the Float64's rounded once; is_in compares
exactly at the boundaries. Every literal frame is the Go test's, and a generated case
follows each.

Not here, with the reason: the refusals (a float too large for a Float32, a string a
type cannot hold, "2^63 → Int64"), which the Go test keeps with their wording; "-0" as
a Uint8, which Polars' unsigned parse refuses and ursus reads as 0 (a differential
through Int64 in the Go test).
"""

from __future__ import annotations

import decimal

import polars as pl

from . import Case, case
from .gen import FLOAT_EDGES, Gen

nan, inf = float("nan"), float("inf")
x = pl.col("x")
P24, P53 = 1 << 24, 1 << 53
BETWEEN = P53 + (1 << 29) + 1  # rounds up to 2^53+2^30 as a float32, and to 2^53 through a float64
EVEN = "half_to_even"


def typed(vals: list, dtype: pl.DataType) -> pl.DataFrame:
    return pl.DataFrame({"x": pl.Series("x", vals, dtype=dtype)})


def dec(vals: list[str], precision: int, scale: int) -> pl.DataFrame:
    return typed([decimal.Decimal(v) for v in vals], pl.Decimal(precision, scale))


def sel(*e: pl.Expr):
    return lambda df: df.select(*e)


def cast(dt: pl.DataType, strict: bool = True):
    return sel(x.cast(dt, strict=strict))


def cases() -> list[Case]:
    g = Gen(4972)
    narrow = [e for e in FLOAT_EDGES if abs(e) < 3.4e38 or e != e or abs(e) == inf] + [1.5e-45, 3.4028235e38, -3.4028235e38]
    gen_f64 = g.frame(g.floats(240, lo=-1e6, hi=1e6, edges=False))
    gen_f64 = gen_f64.with_columns(pl.Series("x", g._place(gen_f64["x"].to_list(), 0, narrow), dtype=pl.Float64))
    gen_i64 = g.frame(g.ints(120, pl.Int64))
    gen_u64 = g.frame(g.ints(120, pl.UInt64))
    gen_i128 = g.frame(g.ints(120, pl.Int128, lo=-(1 << 100), hi=1 << 100))
    gen_dec = g.frame(g.decimals(120, 18, 6, name="x"))
    gen_f32 = g.frame(g.floats(120, dtype=pl.Float32))
    text_ints = gen_i64.select(x.cast(pl.String))
    text_floats = gen_f64.select(x.cast(pl.String))
    return [
        # --- casts to a float ----------------------------------------------------------
        case("f64_to_f32", typed([0.1, 3.4028235e38, nan, inf, -inf], pl.Float64), cast(pl.Float32),
             note="3.4028235e38 is above MaxFloat32 and nearer to it than to 2^128: it rounds down"),
        case("f64_to_f32_lossy", typed([0.1], pl.Float64), cast(pl.Float32, strict=False)),
        case("f64_1e39_to_f32_lossy", typed([1e39], pl.Float64), cast(pl.Float32, strict=False),
             note="Polars' cast overflows to an infinity; ursus's casts are exact or refused, null when lossy"),
        case("i64_to_f32", typed([P24 + 1, BETWEEN], pl.Int64), cast(pl.Float32)),
        case("u64_to_f32", typed([BETWEEN, (1 << 64) - 1], pl.UInt64), cast(pl.Float32)),
        case("i128_to_f64", typed([3 * ((1 << 63) - 1) + 2052], pl.Int128), cast(pl.Float64),
             note="2^64+2^63+2^11+1, past half an ulp above 2^64+2^63: the nearest float64 is 2^64+2^63+2^12"),
        case("i128_to_f32", typed([2 * ((1 << 63) - 1) + (1 << 40) + 3], pl.Int128), cast(pl.Float32),
             note="2^64+2^40+1, past half a float32 ulp above 2^64"),
        case("dec38_0_to_f32", dec([str(BETWEEN)], 38, 0), cast(pl.Float32)),
        case("dec10_2_to_f32", dec(["0.10"], 10, 2), cast(pl.Float32)),
        case("dt_ns_to_u64", typed([1700000000000000001], pl.Int64), sel(x.cast(pl.Datetime("ns")).cast(pl.UInt64))),
        case("dur_ns_to_i128", typed([P53 + 1], pl.Int64), sel(x.cast(pl.Duration("ns")).cast(pl.Int128))),
        case("to_float_gen", gen_f64, sel(x.cast(pl.Float32).alias("f32"), x.cast(pl.Float32, strict=False).alias("lossy"))),
        case("ints_to_float_gen", gen_i64, sel(x.cast(pl.Float32).alias("f32"), x.cast(pl.Float64).alias("f64"))),
        case("uints_to_float_gen", gen_u64, sel(x.cast(pl.Float32).alias("f32"), x.cast(pl.Float64).alias("f64"))),
        case("i128_to_float_gen", gen_i128, sel(x.cast(pl.Float32).alias("f32"), x.cast(pl.Float64).alias("f64"))),
        case("dec_to_float_gen", gen_dec, sel(x.cast(pl.Float32).alias("f32"), x.cast(pl.Float64).alias("f64"))),
        # --- strings parse at the width they are cast to --------------------------------
        case("str_to_i64", typed(["9007199254740993"], pl.String), sel(x.cast(pl.Int64).alias("cast"), x.str.to_integer().alias("to_integer"))),
        case("str_to_u64", typed(["9223372036854775807", "18446744073709551615"], pl.String), cast(pl.UInt64)),
        case("str_256_to_u8_lossy", typed(["256"], pl.String), cast(pl.UInt8, strict=False)),
        case("str_plus_5_to_u8", typed(["+5"], pl.String), cast(pl.UInt8)),
        case("str_to_f32", typed(["0.1", "1.00000005960464477539063", "1e-50", "inf", "NaN"], pl.String), cast(pl.Float32),
             note="the second is just above the midpoint 1+2^-24: through a float64 it lands on it and the tie goes to 1"),
        case("str_to_f32_lossy", typed(["0.1"], pl.String), cast(pl.Float32, strict=False)),
        case("text_ints_gen", text_ints, sel(x.cast(pl.Int64).alias("i64"), x.cast(pl.Float64).alias("f64"),
                                             x.cast(pl.Float32).alias("f32"), x.str.to_integer().alias("to_integer"))),
        case("text_floats_gen", text_floats, sel(x.cast(pl.Float64).alias("f64"), x.cast(pl.Float32).alias("f32"))),
        # --- a Float32's maths ---------------------------------------------------------
        case("f32_math", typed([2, None, 89, 0.1, 2.675, 4], pl.Float32),
             sel(x.sqrt().alias("sqrt"), x.round(1, mode=EVEN).alias("r1"), x.round(2, mode=EVEN).alias("r2")),
             note="float32(2.675) is 2.67499995, which rounds down"),
        case("f32_libm", typed([2, None, 89, 0.1, 2.675, 4], pl.Float32), sel(x.log().alias("ln"), x.exp().alias("exp")), float="rel:1e-6",
             note="glibc's logf(2) is an ulp above the correctly rounded float32, which Go's double rounded once gives"),
        case("f32_lit_sqrt", pl.DataFrame({"b": [True]}), lambda df: df.select(pl.lit(2.0, dtype=pl.Float32).sqrt().alias("x"))),
        case("f32_math_gen", gen_f32, sel(x.sqrt().alias("sqrt"), x.round(1, mode=EVEN).alias("r1"), x.round(2, mode=EVEN).alias("r2"))),
        case("f32_libm_gen", gen_f32, sel(x.log().alias("ln"), x.exp().alias("exp")), float="rel:1e-6",
             note="Go's log and exp of the double, rounded once to a float32, against glibc's float ones"),
        # --- is_in at the boundaries ---------------------------------------------------
        case("is_in_f32_0_1", typed([0.1, 0.2], pl.Float32), sel(x.is_in([0.1]))),
        case("is_in_f32_exact", typed([0.1, 0.2], pl.Float32), sel(x.is_in(pl.Series([0.1], dtype=pl.Float32)))),
        case("is_in_i64_p53", typed([P53, P53 + 1], pl.Int64), sel(x.is_in([P53 + 1]))),
        case("is_in_u64_max", typed([(1 << 64) - 1], pl.UInt64), sel(x.is_in([(1 << 64) - 1]))),
        case("is_in_gen", gen_i64, sel(x.is_in([P53, P53 + 1, 0, -1, (1 << 63) - 1, -(1 << 63)]))),
    ]
