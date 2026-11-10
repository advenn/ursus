"""The matchschema group: Polars' match_to_schema (matchschema_test.go, C2 of
v0.6.7-scope.md, step 299): columns inserted, ignored, reordered, integers and floats
upcast, a fill, a Null column, a List's element, and a Struct's fields. Not here: the
refusals, the plan, the eager and spilled differentials.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
LF = pl.DataFrame({"b": pl.Series([1, 2], dtype=pl.Int32), "a": ["x", "y"], "e": pl.Series([1.5, 2.5], dtype=pl.Float32)})
S = {"a": pl.String, "b": pl.Int64, "c": pl.Float64}
STRUCTS = pl.DataFrame({"j": ['{"x": 1, "y": "p"}', '{"x": 2, "y": "q"}', "null"]}).select(
    c("j").str.json_decode(pl.Struct({"x": pl.Int32, "y": pl.String})).alias("s"))


def mts(schema, **o):
    return lambda df: df.lazy().match_to_schema(schema, **o)


def one(from_: pl.DataType, to: pl.DataType) -> Case:
    name = f"{str(from_).lower()}_as_{str(to).lower()}"
    return case(name, pl.DataFrame({"u": pl.Series([1], dtype=from_)}), mts({"u": to}, integer_cast="upcast", float_cast="upcast"))


def cases() -> list[Case]:
    g = Gen(5021)
    gen = g.frame(g.ints(100, pl.Int32, name="b"), g.strings(100, name="a"), g.floats(100, dtype=pl.Float32, name="e"))
    st = lambda **fs: pl.Struct(fs)  # noqa: E731
    return [
        case("insert_ignore_upcast", LF, mts(S, missing_columns="insert", extra_columns="ignore", integer_cast="upcast")),
        case("a_fill", LF, mts(S, missing_columns={"c": c("e").cast(pl.Float64) * 2}, extra_columns="ignore", integer_cast="upcast"),
             note="the fill cast to the schema's type on both sides, which Polars does not require (§9)"),
        case("reordered", LF, mts({"a": pl.String, "e": pl.Float32, "b": pl.Int32})),
        case("a_float_upcast", LF, mts({"b": pl.Int32, "a": pl.String, "e": pl.Float64}, float_cast="upcast")),
        case("an_upcast_for_one_column", LF, mts({"b": pl.Int64, "a": pl.String, "e": pl.Float32}, integer_cast={"b": "upcast"})),
        case("insert_for_one_column", LF, mts(S, extra_columns="ignore", integer_cast="upcast", missing_columns={"c": "insert"})),
        one(pl.UInt8, pl.Int16),
        one(pl.UInt32, pl.Int64),
        one(pl.Int64, pl.Int128),
        case("a_null_column", pl.DataFrame({"n": pl.Series([None, None], dtype=pl.Null)}), mts({"n": pl.Int64})),
        case("a_lists_element", pl.DataFrame({"l": pl.Series([[1, 2], [3]], dtype=pl.List(pl.Int32))}),
             mts({"l": pl.List(pl.Int64)}, integer_cast="upcast")),
        case("struct_same", STRUCTS, mts({"s": st(x=pl.Int32, y=pl.String)})),
        case("struct_reordered", STRUCTS, mts({"s": st(y=pl.String, x=pl.Int32)})),
        case("struct_field_inserted", STRUCTS, mts({"s": st(x=pl.Int32, y=pl.String, z=pl.Float64)}, missing_struct_fields="insert"),
             note="the third row is a null struct, and stays one"),
        case("struct_field_dropped", STRUCTS, mts({"s": st(x=pl.Int32)}, extra_struct_fields="ignore")),
        case("struct_field_widened", STRUCTS, mts({"s": st(x=pl.Int64, y=pl.String)}, integer_cast="upcast")),
        case("match_gen", gen, mts({"a": pl.String, "b": pl.Int64, "c": pl.Float64, "e": pl.Float64}, missing_columns="insert",
                                   integer_cast="upcast", float_cast="upcast"),
             note="an Int32 and a Float32 widened, a column inserted, the order the schema's"),
    ]
