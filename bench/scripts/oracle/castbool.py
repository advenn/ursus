"""The castbool group: a Boolean cast to each number type and each number type cast
to a Boolean (internal/kernel/castbool_test.go, step 503): false and true are 0 and
1, a null stays null, and a number is true where it is not zero, NaN and the
infinities included, -0 not. The Go test keeps the agreement of CanCast and the
kernel and the refusals of a Decimal either way; Polars 2.1 refuses a Boolean to a
Decimal and casts a Decimal to a Boolean, true where it is not zero, which ursus
refuses on purpose (a truth value is not an amount, step 69; feature-gaps §9): that
refusal is decimal_test.go's TestDecimalGuards since step 507, not a case here.
"""

from __future__ import annotations

import math

import polars as pl

from . import Case, case
from .gen import Gen

NUMBERS: dict[str, pl.DataType] = {
    "int8": pl.Int8, "int16": pl.Int16, "int32": pl.Int32, "int64": pl.Int64, "int128": pl.Int128,
    "uint8": pl.UInt8, "uint16": pl.UInt16, "uint32": pl.UInt32, "uint64": pl.UInt64,
    "float32": pl.Float32, "float64": pl.Float64,
}
BOOLS = pl.DataFrame({"b": pl.Series("b", [False, True, None], dtype=pl.Boolean)})
nan, inf = float("nan"), float("inf")


def frame(name: str, s: pl.Series) -> pl.DataFrame:
    return pl.DataFrame({name: s.alias(name)})


def numbers(dt: pl.DataType) -> pl.DataFrame:
    vals = [0, 1, None, 2] + ([-1] if dt.is_signed_integer() or dt.is_float() else [])
    return frame("v", pl.Series(vals, dtype=pl.Int64).cast(dt))


def cases() -> list[Case]:
    g = Gen(5035)
    n = 120
    out: list[Case] = []
    for name, dt in NUMBERS.items():
        out.append(case(f"bool_to_{name}", BOOLS, lambda df, dt=dt: df.select(pl.col("b").cast(dt))))
    for name, dt in NUMBERS.items():
        out.append(case(f"{name}_to_bool", numbers(dt), lambda df: df.select(pl.col("v").cast(pl.Boolean))))
    specials = [nan, inf, -inf, -0.0, 0.0, 2.5, None, 1e-320]
    out += [
        case("float_specials_to_bool",
             pl.DataFrame([pl.Series("f64", specials, dtype=pl.Float64), pl.Series("f32", specials, dtype=pl.Float32)]),
             lambda df: df.select(pl.col("f64").cast(pl.Boolean), pl.col("f32").cast(pl.Boolean))),
        case("bool_to_decimal", BOOLS, lambda df: df.select(pl.col("b").cast(pl.Decimal(10, 2)))),
        case("gen_bool_to_numbers", frame("b", g.bools(n)),
             lambda df: df.select(pl.col("b").cast(dt).alias(name) for name, dt in NUMBERS.items())),
        case("gen_numbers_to_bool",
             pl.DataFrame([g.ints(n, pl.Int64, name="i64"), g.ints(n, pl.Int8, name="i8"), g.ints(n, pl.UInt16, name="u16"),
                           g.ints(n, pl.Int128, name="i128"), g.floats(n, name="f64"), g.floats(n, dtype=pl.Float32, name="f32")]),
             lambda df: df.select(pl.all().cast(pl.Boolean))),
    ]
    return out
