"""The dummies group: to_dummies, its options, and each type's dummy names, Polars'
display of its values (dummies_test.go, tier 2 of v0.6.7-scope.md, step 320). Not
here: the refusals, the frame of none left, and the memory limit.
"""

from __future__ import annotations

import math

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
nan = float("nan")
DF = pl.DataFrame({"foo": [1, 2, 3, 1], "bar": ["a", "b", None, "a"], "x": [1.5, 2.5, 3.5, 4.5]})
DURATIONS = [0, 1, 999, 1000, 1001, 1_000_000, 1_500_000, 1_000_000_000, 61_000_000_123, -90_000_000_000, -1,
             86_400_000_000_000 + 1, 3_600_000_000_000 * 25 + 7_000]
LOCAL = [1704164645000000, 1719889445001000]  # 2024-01-02 03:04:05, 2024-07-02 03:04:05.001


def v(vals, dtype=None) -> pl.DataFrame:
    return pl.DataFrame({"v": pl.Series("v", vals, dtype=dtype)})


def cast(vals, dtype, from_: pl.DataType = pl.Int64) -> pl.DataFrame:
    return pl.DataFrame({"v": pl.Series("v", vals, dtype=from_).cast(dtype)})


def zoned(tz: str) -> pl.DataFrame:
    return pl.DataFrame({"v": pl.Series("v", LOCAL).cast(pl.Datetime("us")).dt.replace_time_zone(tz)})


def dum(**o):
    return lambda df: df.to_dummies(**o)


def cases() -> list[Case]:
    g = Gen(5016)
    gen = g.frame(g.strings(120, alphabet="abc", maxlen=2, name="s"), g.ints(120, pl.Int64, lo=0, hi=5, edges=False, name="i"),
                  g.floats(120, lo=0, hi=3, edges=False, name="f"))
    names = {
        "float64": v([0.30000000000000004, 12.0000000001, 1234567.0, 999999.0, 999999.5, -1e-7, 1e-6, 123456.789, math.inf, -math.inf, nan, -0.0]),
        "float64_more": v([1e15, 0.0000011, 99999.99999, 1234567.5, 5e-324, 1.7976931348623157e308, 1.0 / 3, 2.0 / 3 * 1e6, 1e20, 1.5e-7,
                           100.0, 123456789.0, 2.5]),
        "float32": v([1e20, 1.5e-7, 0.30000001, 2.0, 1234567.0, 16777217.0, 3.4e38, 1e-45, -2.5, 0.1], pl.Float32),
        "duration_ns": cast(DURATIONS, pl.Duration("ns")),
        "duration_us": cast(DURATIONS, pl.Duration("us")),
        "duration_ms": cast(DURATIONS, pl.Duration("ms")),
        "date": cast([-800000, 3000000, 0, -719162], pl.Date, pl.Int32),
        "datetime_us": cast([-62135596800000000 - 86400000000, 253402300800000000, 1704164645123456], pl.Datetime("us")),
        "datetime_ns": cast([1_000_000_123, 1_500_000_000, 1_000_000_000], pl.Datetime("ns")),
        "datetime_ms": cast([1_000_123, 1_000_000], pl.Datetime("ms")),
        "time": cast([1, 1000, 1_000_000, 86_399_999_999_999, 11045000000000], pl.Time),
        "berlin": zoned("Europe/Berlin"), "kolkata": zoned("Asia/Kolkata"), "tashkent": zoned("Asia/Tashkent"), "utc": zoned("UTC"),
        "new_york": zoned("America/New_York"),
        "binary": v([b"a'b", b'q"', b"back\\", b"nl\n", b"\t\r", "ü".encode(), bytes([0x7f]), b" ~", b""], pl.Binary),
        "decimal": pl.DataFrame({"v": pl.Series("v", ["1.50", "2.25", "-3.00", "-0.05", "0.00"]).str.to_decimal().cast(pl.Decimal(10, 2))}),
        "decimal_no_scale": pl.DataFrame({"v": pl.Series("v", ["1", "-20"]).str.to_decimal().cast(pl.Decimal(10, 0))}),
        "uint64": v([(1 << 64) - 1, 0], pl.UInt64),
        "int8": v([-128, 5, 127], pl.Int8),
        "boolean": v([True, False, None, True], pl.Boolean),
        "enum": v(["b", "a", "b"], pl.Enum(["b", "a"])),
    }
    out = [
        case("a_column_in_its_place", DF, dum(columns=["bar"])),
        case("a_separator_the_first_dropped", DF, dum(columns=["bar"], separator=":", drop_first=True)),
        case("the_null_dropped", DF, dum(columns=["bar"], drop_nulls=True)),
        case("every_column", DF, dum()),
        case("ordered_by_name", pl.DataFrame({"a": [10, 2, 1, 2, None]}), dum(), note="by name, so 10 before 2"),
        case("ordered_by_name_first_dropped", pl.DataFrame({"a": [10, 2, 1, 2, None]}), dum(drop_first=True),
             note="the first to appear dropped, not the least"),
        case("a_null_first", pl.DataFrame({"c": [None, 5, 3]}), dum(drop_first=True), note="the first value that is not null is dropped"),
        case("only_nulls_none_dropped_first", pl.DataFrame({"c": pl.Series([None, None], dtype=pl.Int64)}), dum(drop_first=True)),
        case("text_by_its_bytes", pl.DataFrame({"c": ["zebra", None, "apple", "zebra", "Banana"]}), dum(drop_first=True)),
        case("floats", pl.DataFrame({"f": [1.0, -0.0, 0.0, nan, 2.5]}), dum(), note="-0.0 is 0.0, named as it first appears; NaN is one value"),
        case("a_series", pl.DataFrame({"s": ["b", "a", None, "b"]}), lambda df: df["s"].to_dummies(drop_first=True)),
        case("dummies_gen", gen, dum(), note="a dozen short strings, six small integers, 120 floats"),
        case("dummies_gen_dropped", gen, dum(columns=["s", "i"], drop_first=True, drop_nulls=True, separator=":")),
    ]
    out += [case(f"names_{name}", frame, dum(), note="each dummy named by Polars' display of the value") for name, frame in names.items()]
    return out
