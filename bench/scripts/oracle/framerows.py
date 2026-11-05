"""The framerows group: a frame's shift, gather_every, clear, first and last
(framerows_test.go, R5 of v0.6.7-scope.md, step 293), and equals, each a frame of one
Boolean. Not here: the refusals, is_empty and the eager shift.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
nan = float("nan")
LF = pl.DataFrame({"a": [1, 2, 3, 4, 5], "s": ["p", "q", "r", "s", "t"], "f": [1.5, 2.5, None, 4.5, 5.5]})
E1 = pl.DataFrame({"a": [1, None], "f": [nan, -0.0]})
E2 = pl.DataFrame({"a": [1, None], "f": [nan, 0.0]})
D = pl.DataFrame({"a": [1, 2], "s": ["x", "y"]})
X = pl.DataFrame({"x": [1, 2]})


def nested(df: pl.DataFrame) -> pl.DataFrame:
    return df.with_columns(pl.concat_list(c("x"), c("x")).alias("l"), pl.struct(c("x")).alias("st"))


def eq(name: str, a: pl.DataFrame, b, null_equal: bool = True, both=None, **opts) -> Case:
    """A frame of one Boolean, equals, b a frame or a function of a; both, when
    given, is applied to each side first."""
    def q(a: pl.DataFrame, **other) -> pl.DataFrame:
        bb = other["b"] if "b" in other else b(a)
        aa = a if both is None else both(a)
        return pl.DataFrame({"equals": [aa.equals(bb, null_equal=null_equal)]})
    inputs = {"a": a} if callable(b) else {"a": a, "b": b}
    return case("equals_" + name, inputs, q, **opts)


def cases() -> list[Case]:
    g = Gen(5024)
    gen = g.frame(g.ints(90, pl.Int64, name="a"), g.strings(90, name="s"), g.floats(90, name="f"))
    return [
        case("shift_2", LF, lambda df: df.shift(2)),
        case("shift_minus_1", LF, lambda df: df.shift(-1)),
        case("shift_0", LF, lambda df: df.shift(0)),
        case("shift_9", LF, lambda df: df.shift(9)),
        case("shift_2_fill_0", LF, lambda df: df.shift(2, fill_value=0), note="a text column takes the fill's text"),
        case("shift_1_fill_1_5", LF, lambda df: df.shift(1, fill_value=1.5), note="a fill of 1.5 makes the Int64 column a Float64"),
        case("every_2", LF, lambda df: df.gather_every(2, 0)),
        case("every_2_from_1", LF, lambda df: df.gather_every(2, 1)),
        case("every_3_from_9", LF, lambda df: df.gather_every(3, 9)),
        case("clear_0", LF, lambda df: df.clear(0)),
        case("clear_2", LF, lambda df: df.clear(2)),
        case("first", LF, lambda df: df.head(1)),
        case("last", LF, lambda df: df.tail(1)),
        case("first_of_none", LF.head(0), lambda df: df.head(1)),
        eq("nan_and_zeros", E1, E2),
        eq("itself", E1, lambda a: a),
        eq("nulls_unequal", E1, E2, null_equal=False),
        eq("itself_nulls_unequal", E1, lambda a: a, null_equal=False),
        eq("another_name", D, lambda a: a.rename({"a": "b"})),
        eq("int32_and_int64", D, lambda a: a.with_columns(c("a").cast(pl.Int32))),
        eq("int64_and_float64", D, lambda a: a.with_columns(c("a").cast(pl.Float64))),
        eq("text_and_a_number", D, lambda a: a.with_columns(c("a").cast(pl.String))),
        eq("another_order", D, lambda a: a.select("s", "a")),
        eq("fewer_rows", D, lambda a: a.head(1)),
        eq("another_value", D, lambda a: a.with_columns(c("a") + 1)),
        eq("lists_and_structs", X, nested, both=nested, note="both sides are the nested frame built from x"),
        eq("lists_and_structs_nulls_unequal", X, nested, null_equal=False, both=nested),
        eq("none_of_none", D.head(0), lambda a: a),
        case("shift_gen", gen, lambda df: df.shift(3)),
        case("shift_gen_fill", gen, lambda df: df.shift(-2, fill_value=0.5)),
        case("every_gen", gen, lambda df: df.gather_every(3, 1)),
    ]
