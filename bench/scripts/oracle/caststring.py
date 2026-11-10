"""The caststring group: a number, an Int128, a Decimal and a Boolean cast to text
(internal/kernel/caststring_test.go, step 503): the edges of each width, which a
detour through a float64 rounded, and generated columns of each type. The Go test
keeps the agreement of CanCast and the kernel, and that a null stays null.
"""

from __future__ import annotations

import decimal

import polars as pl

from . import Case, case
from .gen import Gen

D = decimal.Decimal
MAXI64, MINI64, MAXU64 = (1 << 63) - 1, -(1 << 63), (1 << 64) - 1
MAXI128, MINI128 = (1 << 127) - 1, -(1 << 127)


def frame(vals: list, dt: pl.DataType) -> pl.DataFrame:
    return pl.DataFrame({"v": pl.Series("v", vals, dtype=dt)})


def text(df: pl.DataFrame) -> pl.DataFrame:
    return df.select(pl.all().cast(pl.String))


def cases() -> list[Case]:
    g = Gen(5036)
    n = 120
    return [
        case("int64_edges", frame([MINI64, MAXI64, 0, -1, None], pl.Int64), text),
        case("uint64_edges", frame([MAXU64, 0, 1 << 53, (1 << 53) + 1, None], pl.UInt64), text),
        case("int128_edges", frame([1 << 64, MINI128, MAXI128, 0, -1, None], pl.Int128), text),
        case("decimal_10_2", frame([D("12.34"), D("-0.05"), D("0.00"), D("99999999.99"), D("-99999999.99"), None],
                                   pl.Decimal(10, 2)), text),
        case("decimal_38_0", frame([D(10**38 - 1), D(-(10**38 - 1)), D(0), None], pl.Decimal(38, 0)), text),
        case("bool", frame([True, False, None], pl.Boolean), text),
        case("gen_ints", pl.DataFrame([g.ints(n, pl.Int8, name="i8"), g.ints(n, pl.Int16, name="i16"), g.ints(n, pl.Int32, name="i32"),
                                       g.ints(n, pl.Int64, name="i64")]), text),
        case("gen_uints", pl.DataFrame([g.ints(n, pl.UInt8, name="u8"), g.ints(n, pl.UInt16, name="u16"),
                                        g.ints(n, pl.UInt32, name="u32"), g.ints(n, pl.UInt64, name="u64")]), text),
        case("gen_int128", pl.DataFrame([g.ints(n, pl.Int128, name="i128")]), text),
        case("gen_decimals", pl.DataFrame([g.decimals(n, precision=12, scale=4, name="d12_4"),
                                           g.decimals(n, precision=20, scale=0, name="d20_0"),
                                           g.decimals(n, precision=5, scale=5, name="d5_5")]), text),
        case("gen_floats", pl.DataFrame([g.floats(n, name="f64"), g.floats(n, dtype=pl.Float32, name="f32")]), text),
        case("gen_bools", pl.DataFrame([g.bools(n)]), text),
    ]
