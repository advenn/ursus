"""The dtype group: the type two types meet at (dtype_test.go's Promote and
PromoteExact tables, step 503). Promote is what When/Then/Otherwise, the arithmetic
and the comparisons meet their operands at, asked of Polars as the type its
when/then/otherwise gives the branches; PromoteExact is what Unpivot melts two
columns to, asked as the type Polars' unpivot gives the value column. Each literal
pair is the Go test's; the generated pairs carry values through the meet.

Polars has no Datetime(s) and no Time(us), so those pairs stay in the Go test, and a
pair ursus refuses on purpose (feature-gaps §9) stays there as a refusal.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

TYPES: dict[str, pl.DataType] = {
    "int8": pl.Int8, "int16": pl.Int16, "int32": pl.Int32, "int64": pl.Int64, "int128": pl.Int128,
    "uint8": pl.UInt8, "uint16": pl.UInt16, "uint32": pl.UInt32, "uint64": pl.UInt64,
    "float32": pl.Float32, "float64": pl.Float64, "null": pl.Null, "string": pl.String, "bool": pl.Boolean,
    "enum_a": pl.Enum(["a"]), "enum_b": pl.Enum(["b"]), "date": pl.Date, "time": pl.Time,
    "dt_ms": pl.Datetime("ms"), "dt_us": pl.Datetime("us"), "dt_ns": pl.Datetime("ns"),
    "dt_us_utc": pl.Datetime("us", "UTC"), "dt_ns_utc": pl.Datetime("ns", "UTC"),
    "dt_us_paris": pl.Datetime("us", "Europe/Paris"), "dt_ns_paris": pl.Datetime("ns", "Europe/Paris"),
    "dur_ms": pl.Duration("ms"), "dur_ns": pl.Duration("ns"),
}


def column(name: str, dt: pl.DataType, vals: list) -> pl.Series:
    """vals in dt: ticks for a temporal type, the text of an Enum, nothing for Null."""
    if dt == pl.Null:
        return pl.Series(name, [None] * len(vals), dtype=pl.Null)
    if isinstance(dt, pl.Datetime):
        s = pl.Series(name, vals, dtype=pl.Int64).cast(pl.Datetime(dt.time_unit))
        return s.dt.replace_time_zone(dt.time_zone) if dt.time_zone else s
    if isinstance(dt, pl.Duration) or dt in (pl.Date, pl.Time):
        return pl.Series(name, vals, dtype=pl.Int64).cast(dt)
    return pl.Series(name, vals, dtype=dt)


def two(dt: pl.DataType) -> list:
    """Two values of dt."""
    if dt == pl.Boolean:
        return [True, False]
    if dt == pl.String:
        return ["x", "y"]
    if isinstance(dt, pl.Enum):
        return [dt.categories[0]] * 2
    return [1, 2]


def pair(a: str, b: str) -> pl.DataFrame:
    """c, a and b: the condition takes a's first row and b's second."""
    ta, tb = TYPES[a], TYPES[b]
    return pl.DataFrame([pl.Series("c", [True, False]), column("a", ta, two(ta)), column("b", tb, two(tb))])


def when(df: pl.DataFrame) -> pl.DataFrame:
    return df.select(pl.when(pl.col("c")).then(pl.col("a")).otherwise(pl.col("b")).alias("x"))


def melt(df: pl.DataFrame) -> pl.DataFrame:
    return df.unpivot(on=["a", "b"], index="k")


def unpivot_pair(a: str, b: str) -> pl.DataFrame:
    ta, tb = TYPES[a], TYPES[b]
    return pl.DataFrame([pl.Series("k", [1, 2]), column("a", ta, two(ta)), column("b", tb, two(tb))])


# Promote's table: the pairs the Go test held, each by name.
PROMOTE = [
    ("int64", "int64"), ("int8", "int64"), ("uint8", "uint32"), ("float32", "float64"),
    ("null", "int32"), ("int32", "null"),
    ("int8", "uint8"), ("int64", "uint8"), ("int16", "uint32"),
    ("float32", "int16"), ("float32", "int32"), ("float64", "int64"),
    ("uint64", "int64"), ("int8", "uint64"),
    ("int128", "int64"), ("int128", "uint64"), ("int128", "int8"),
    ("string", "int64"), ("bool", "int64"), ("string", "string"),
    ("enum_a", "string"), ("enum_a", "enum_b"), ("enum_a", "int64"),
    ("date", "dt_us"),
    # The temporal table: units, zones and kinds.
    ("dt_us_utc", "dt_ns_utc"), ("dt_ms", "dt_ns"), ("dur_ms", "dur_ns"),
    ("dt_us_utc", "dt_us"), ("dt_us_utc", "dt_us_paris"), ("dt_us_utc", "dt_ns_paris"),
    ("dur_ns", "dt_ns"), ("time", "dt_ns"), ("dt_ns", "int64"),
]

# PromoteExact's table, the pairs it resolves; the three it refuses are §9's and stay
# in the Go test.
PROMOTE_EXACT = [
    ("int32", "float32"), ("uint32", "float64"), ("int16", "float32"), ("int64", "uint64"),
    ("float32", "float64"), ("dt_ms", "dt_ns"), ("int32", "int64"),
]


def cases() -> list[Case]:
    g = Gen(5031)
    n = 120
    out: list[Case] = []
    for a, b in PROMOTE:
        out.append(case(f"when_{a}_{b}", pair(a, b), when))
    for a, b in PROMOTE_EXACT:
        out.append(case(f"unpivot_{a}_{b}", unpivot_pair(a, b), melt, order="set"))

    def gen_when(name: str, a: pl.Series, b: pl.Series) -> Case:
        return case(name, pl.DataFrame([g.bools(n, name="c"), a.alias("a"), b.alias("b")]), when)

    out += [
        gen_when("when_gen_int8_uint8", g.ints(n, pl.Int8), g.ints(n, pl.UInt8)),
        gen_when("when_gen_float32_int16", g.floats(n, dtype=pl.Float32), g.ints(n, pl.Int16)),
        gen_when("when_gen_uint64_int64", g.ints(n, pl.UInt64), g.ints(n, pl.Int64)),
        gen_when("when_gen_float32_int32", g.floats(n, dtype=pl.Float32), g.ints(n, pl.Int32)),
        case("unpivot_gen_int32_float32",
             pl.DataFrame([pl.Series("k", list(range(n)), dtype=pl.Int64),
                           g.ints(n, pl.Int32).alias("a"), g.floats(n, dtype=pl.Float32).alias("b")]),
             melt, order="set"),
    ]
    return out
