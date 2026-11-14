"""The castmore group, step 325's casts as Polars': str.to_time, str.to_decimal,
dt.cast_time_unit and a float cast to text (castmore_test.go). Every literal frame
is the Go test's, a generated case beside each; the refusals and the type checks stay
in the Go test.
"""

from __future__ import annotations

from decimal import ROUND_HALF_UP, Decimal

import polars as pl

from . import Case, case
from .gen import Gen

nan, inf = float("nan"), float("inf")
t, v, f = pl.col("t"), pl.col("v"), pl.col("f")

TEXT_TIMES = pl.DataFrame({"t": ["10:30:00", "bad", None]})
TEXT_DECIMALS = pl.DataFrame({"t": ["1.25", "3.5", "x", None, "1.255", "-1.255", "1e3"]})
TICKS = pl.DataFrame({"v": pl.Series([-1999, 1999, -1000, -1], dtype=pl.Int64)})
F64 = [1, -0.0, 0, nan, inf, -inf, 0.1, 1.5, 100, 1234.5, 123456789, 1e15, 1e16, 1e17, 1.5e16, 1e20, 1e21, 1.2345e21,
       1e100, 1e-4, 1e-5, 1e-6, 1e-7, 1.5e-7, 0.000123, 5e-324, 1.7976931348623157e308, 2.0**53, 0.30000000000000004, -2.5e-10]
F32 = [1, 0.1, 16777217, 3.4e38, 1e-45, 1e20, 1e-7, 123456.7, 1.5e-5]


def sel(*e: pl.Expr):
    return lambda df: df.select(*e)


def python_half_up(df: pl.DataFrame) -> pl.DataFrame:
    """str.to_decimal at scales 2 and 0 by Python's decimal, each tie rounded half away
    from zero (ROUND_HALF_UP), as Decimal(38, s): the oracle of to_decimal_ties (step
    507)."""
    def at(scale: int) -> pl.Series:
        q = Decimal(1).scaleb(-scale)
        vals = [Decimal(v).quantize(q, rounding=ROUND_HALF_UP) for v in df["t"].to_list()]
        return pl.Series(f"s{scale}", vals, dtype=pl.Decimal(38, scale))
    return pl.DataFrame([at(2), at(0)])


def cases() -> list[Case]:
    g = Gen(4974)
    times = g.times(120, name="t")
    text_times = pl.DataFrame({"t": times.dt.to_string("%H:%M:%S")})
    text_times = text_times.with_columns(pl.Series("t", g._place(text_times["t"].to_list(), 0.0, ["bad", "25:00:00", "7:5:1", ""])))
    # No tie at the second decimal: a text ending in 50 gets a 1, so the rounding of a tie is to_decimal_ties' alone.
    text_decimals = [None if v is None else (v[:-1] + "1" if v.endswith("50") else v) for v in g.decimals(120, 12, 4, name="t").cast(pl.String).to_list()]
    text_decimals = pl.DataFrame({"t": pl.Series("t", g._place(text_decimals, 0.0, ["x", "1e3", "-0.0051", "12345678.9999"]), dtype=pl.String)})
    gen_ticks = g.frame(g.ints(150, pl.Int64, lo=-(1 << 40), hi=1 << 40, name="v"))
    gen_f64 = g.frame(g.floats(240, name="f"))
    gen_f32 = g.frame(g.floats(120, dtype=pl.Float32, name="f"))
    return [
        case("to_time_lenient", TEXT_TIMES, sel(t.str.to_time("%H:%M:%S", strict=False))),
        case("to_time_format", pl.DataFrame({"t": ["10h30"]}), sel(t.str.to_time("%Hh%M"))),
        case("to_time_no_format", pl.DataFrame({"t": ["10:30:00.5"]}), sel(t.str.to_time()), note="no format: as a cast reads it"),
        case("to_time_gen", text_times, sel(t.str.to_time("%H:%M:%S", strict=False))),
        case("to_decimal_2", TEXT_DECIMALS, sel(t.str.to_decimal(scale=2))),
        case("to_decimal_ties", pl.DataFrame({"t": ["0.005", "-0.005", "0.015", "0.025", "1.255", "1.245", "2.5", "3.5", "-2.5", "0.0050"]}),
             python_half_up,
             note="the want is Python's decimal, ROUND_HALF_UP, not Polars' (step 507): ToDecimal rounds a tie half away from "
                  "zero, as its doc says and Round does by default; Polars 2.1's str.to_decimal rounds it half to even"),
        case("to_decimal_gen", text_decimals, sel(t.str.to_decimal(scale=2).alias("s2"), t.str.to_decimal(scale=4).alias("s4"))),
        case("cast_time_unit", TICKS, sel(v.cast(pl.Datetime("us")).dt.cast_time_unit("ms").alias("dt"),
                                          v.cast(pl.Duration("us")).dt.cast_time_unit("ms").alias("du"),
                                          v.cast(pl.Datetime("us")).dt.cast_time_unit("ns").alias("up"))),
        case("cast_time_unit_gen", gen_ticks, sel(v.cast(pl.Datetime("us")).dt.cast_time_unit("ms").alias("dt_ms"),
                                                  v.cast(pl.Datetime("us")).dt.cast_time_unit("ns").alias("dt_ns"),
                                                  v.cast(pl.Duration("us")).dt.cast_time_unit("ms").alias("du_ms"),
                                                  v.cast(pl.Duration("us")).dt.cast_time_unit("ns").alias("du_ns"))),
        case("float_as_text", pl.DataFrame({"f": pl.Series(F64, dtype=pl.Float64)}), sel(f.cast(pl.String))),
        case("f32_as_text", pl.DataFrame({"f": pl.Series(F32, dtype=pl.Float32)}), sel(f.cast(pl.String))),
        case("float_as_text_gen", gen_f64, sel(f.cast(pl.String))),
        case("f32_as_text_gen", gen_f32, sel(f.cast(pl.String))),
    ]
