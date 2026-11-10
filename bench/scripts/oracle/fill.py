"""The fill group: the fills' values (fill_test.go): fill_null of a literal, fill_nan,
forward_fill and backward_fill with a limit, the seven strategies, a frame's
fill_null, clip, is_close, diff and pct_change. A frame's FillZero and FillOne leave a
String or a Bool column alone here, where Polars' fill_null(strategy="zero") fills
them with "" and false and its "one" is refused at frame level: those cases ask Polars
of the numeric columns alone. Two findings are recorded and not asserted: a literal
past the column's type widens to Int64 here and to the narrowest type that holds it
in Polars, and the mean strategy keeps an integer column's type in Polars, the mean
truncated, where it is a Float64 here. Not here: the types and plans the weak-literal
machinery promises, which stay structural, and the differentials.
"""

from __future__ import annotations

import math

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
nan = float("nan")
V = pl.DataFrame({"v": pl.Series([None, 1, None, None, 4, None], dtype=pl.Int64)})
V32 = pl.DataFrame({"v": pl.Series([None, 2, None, 8], dtype=pl.Int32)})
FRAME = pl.DataFrame({"i": [1, None, 3], "f": [1.5, None, None], "s": ["a", None, "c"], "b": [True, None, False]})
STRATEGIES = ["zero", "one", "forward", "backward", "min", "max"]


def sel(*exprs):
    return lambda df: df.select(*exprs)


def cases() -> list[Case]:
    g = Gen(5027)
    gen = g.frame(g.ints(150, pl.Int32, lo=-500, hi=500, null_rate=0.3, edges=False, name="v"),
                  g.floats(150, lo=-20, hi=20, null_rate=0.2, name="f"), g.floats(150, lo=-20, hi=20, null_rate=0.2, name="lo"),
                  g.floats(150, lo=-20, hi=20, null_rate=0.2, name="hi"))
    return [
        case("fill_null_with_minus_1_int32", pl.DataFrame({"i32": pl.Series([None, 3], dtype=pl.Int32)}), sel(c("i32").fill_null(-1)),
             note="a weak literal keeps the column's type"),
        case("fill_past_the_type", pl.DataFrame({"i8": pl.Series([None, 3], dtype=pl.Int8)}), sel(c("i8").fill_null(5000)),
             note="a finding: Polars widens to the narrowest type holding 5000, an Int16; ursus falls back to Int64"),
        case("fill_u32_with_minus_1", pl.DataFrame({"u32": pl.Series([None, 3], dtype=pl.UInt32)}), sel(c("u32").fill_null(-1)),
             note="a Uint32 cannot hold -1: an Int64, not 4294967295"),
        case("fill_nan_leaves_nulls", pl.DataFrame({"f": [1.0, 0.0, None]}), sel((c("f") / c("f")).fill_nan(-1.0).alias("v")),
             note="0/0 is NaN, a value; the null is not, and keeps its null"),
        case("forward_and_backward", V, sel(c("v").forward_fill().alias("ff"), c("v").forward_fill(limit=1).alias("ff1"),
                                            c("v").backward_fill().alias("bf"), c("v").fill_null(strategy="forward").alias("strategy"))),
        case("forward_fill_limits_1_and_3", pl.DataFrame({"v": pl.Series([None, 1, None, None, None, None], dtype=pl.Int64)}),
             sel(c("v").forward_fill(limit=1).alias("a"), c("v").forward_fill(limit=3).alias("b")),
             note="row 3 is two nulls past the value: limit 1 gives up, limit 3 does not"),
        case("fill_strategies", V32, sel(*[c("v").fill_null(strategy=s).alias(s) for s in STRATEGIES]), note="the type kept for all six"),
        case("fill_strategy_mean", V32, sel(c("v").fill_null(strategy="mean").alias("mean")),
             note="a finding: Polars keeps the Int32 and fills the mean truncated, ursus a Float64"),
        case("frame_fill_null_0", pl.DataFrame({"i": pl.Series([None, 2], dtype=pl.Int32), "f": [None, 1.5], "s": [None, "b"]}),
             lambda df: df.fill_null(0), note="the String column is left alone by an integer fill, the types unchanged"),
        case("clip_floats", pl.DataFrame({"v": [-5.0, 0.0, 5.0, 50.0, nan]}), sel(c("v").clip(0.0, 10.0).alias("c"))),
        case("clip_null", pl.DataFrame({"v": pl.Series([None], dtype=pl.Float64)}), sel(c("v").clip(0.0, 10.0).alias("c"))),
        case("clip_column_bounds", pl.DataFrame({"v": [5, 5], "lo": [0, 6], "hi": [10, 10]}), sel(c("v").clip(c("lo"), c("hi")).alias("c"))),
        case("is_close_infinities", pl.DataFrame({"a": [1.0, 1.0, math.inf, math.inf, nan, 100.0], "b": [1.0 + 1e-12, 2.0, math.inf, -math.inf, nan, 100.0000001]}),
             sel(c("a").is_close(c("b"), rel_tol=1e-9, abs_tol=0).alias("c")), note="the equality prefix makes IsClose(Inf, Inf) true"),
        case("diff_and_pct_change", pl.DataFrame({"v": [10, 20, 40, 40]}), sel(c("v").diff(1).alias("d"), c("v").pct_change(1).alias("p"))),
        case("clip_int32_values", pl.DataFrame({"v": pl.Series([-5, 5, 500], dtype=pl.Int32)}), sel(c("v").clip(0, 100).alias("c")),
             note="the values still clip, and the Int32 stays one"),
        case("frame_fill_forward", FRAME, lambda df: df.fill_null(strategy="forward")),
        case("frame_fill_backward", FRAME, lambda df: df.fill_null(strategy="backward")),
        case("frame_fill_min", FRAME, lambda df: df.fill_null(strategy="min")),
        case("frame_fill_max", FRAME, lambda df: df.fill_null(strategy="max")),
        case("frame_fill_zero", FRAME, lambda df: df.with_columns(c("i", "f").fill_null(strategy="zero")),
             note="Polars fills the String with \"\" and the Bool with false, which this leaves alone: asked of the numbers"),
        case("frame_fill_one", FRAME, lambda df: df.with_columns(c("i", "f").fill_null(strategy="one")),
             note="Polars refuses the strategy at frame level: asked of the numbers"),
        case("frame_fill_mean", FRAME, lambda df: df.with_columns(c("i", "f").fill_null(strategy="mean")),
             note="a finding: Polars keeps the Int64 and fills the mean truncated, ursus a Float64"),
        case("frame_fill_subset_and_expression", FRAME,
             lambda df: df.with_columns(c("i").fill_null(strategy="forward")).with_columns(c("f").fill_null(strategy="forward")).select("i", "f", "s")),
        case("fill_gen", gen, sel(*[c("v").fill_null(strategy=s).alias(s) for s in STRATEGIES], c("v").forward_fill(limit=2).alias("ff2"),
                                  c("v").backward_fill(limit=2).alias("bf2"))),
        case("clip_gen", gen, sel(c("f").clip(-5.0, 5.0).alias("c"), c("f").clip(c("lo"), c("hi")).alias("cols"), c("f").fill_nan(0.0).alias("nn"),
                                  c("f").diff(1).alias("d"), c("f").is_close(c("lo"), rel_tol=0.1, abs_tol=0.5).alias("close"))),
    ]
