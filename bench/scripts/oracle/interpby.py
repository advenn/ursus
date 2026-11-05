"""The interpby group: interpolate_by (interpby_test.go, step 329): on the line, by
unsorted, runs at the ends null, a NaN a value, a zero span NaN, integers to a Float64,
a Float32 computed in float32, a Date and a Datetime(ns) by, a NaN by sorting last, and
per group.

Not here: the refusals (a null by, text, a Boolean by), which the Go test keeps, and
the spill differential.
"""

from __future__ import annotations

import math
from datetime import date

import numpy as np
import polars as pl

from . import Case, case
from .dtmore import sel
from .gen import Gen

x, t = pl.col("x"), pl.col("t")
NAN = math.nan
BY = [84, 214, 353, 325, 144, 245, 110, 243, 262, 93, 258, 270, 120, 1, 7, 189, 299, 219, 35, 74,
      384, 118, 119, 355, 21, 223, 378, 394, 208, 315, 226, 17, 168, 276, 254, 59, 332, 188, 11, 79]
FORTY = [-82.307221, -96.090097, None, -19.731351, None, -6.011597, -85.075333, None, 60.582717, 89.861281,
         97.76383, None, 11.788518, None, None, None, -41.602541, 0.287819, None, -46.859072, -68.211253, 96.47836, None,
         None, 4.434568, -36.271769, -77.458073, 25.299414, -33.524361, 85.94073, None, None, -65.196377, None, None,
         None, 31.596048, None, -67.382759, -97.147446]
F32 = [-48.77217483520508, None, -38.76011276245117, -10.716263771057129, None, 18.38642692565918, -36.13090515136719, None,
       -38.75564193725586, -26.819765090942383, None, 25.759248733520508, -35.2598762512207, None]


def xt(xs: list, ts: list, xdtype: pl.DataType = pl.Float64, tdtype: pl.DataType = pl.Int64, **more: pl.Series) -> pl.DataFrame:
    return pl.DataFrame({"x": pl.Series("x", xs, dtype=xdtype), "t": pl.Series("t", ts, dtype=tdtype), **more})


def cases() -> list[Case]:
    g = Gen(4993)
    n = 200
    by = pl.Series("t", [int(v) for v in g.rng.permutation(n) * 3], dtype=pl.Int64)
    gen = g.frame(g.floats(n, null_rate=0.3, name="x"), by)
    gen_f32 = g.frame(g.floats(n, null_rate=0.3, dtype=pl.Float32, name="x"),
                      pl.Series("t", sorted(float(v) for v in g.rng.uniform(0, 1000, n)), dtype=pl.Float64))
    gen_over = g.frame(g.ints(n, lo=1, hi=4, null_rate=0, edges=False, name="g"), g.floats(n, null_rate=0.4, edges=False, name="x"),
                       pl.Series("t", [int(v) for v in g.rng.integers(0, 10_000, n)], dtype=pl.Int64))
    gen_dates = g.frame(g.floats(n, null_rate=0.3, edges=False, name="x"),
                        pl.Series("t", sorted(set(int(v) for v in g.rng.integers(0, 20_000, n + 40)))[:n], dtype=pl.Int32).cast(pl.Date))
    return [
        case("on_the_line", xt([1.0, None, None, 4.0, None, 10.0], [0, 1, 3, 4, 7, 10]), sel(x.interpolate_by(t))),
        case("by_unsorted", xt([1.0, None, None, 4.0, None, 10.0], [0, 3, 1, 4, 7, 10]), sel(x.interpolate_by(t)),
             note="by need not be sorted: the answers go back to the rows"),
        case("ends_stay_null", xt([None, 1.0, None, 3.0, None], [0, 1, 2, 4, 5]), sel(x.interpolate_by(t))),
        case("nan_is_a_value", xt([1.0, NAN, None, 3.0], [0, 1, 2, 4]), sel(x.interpolate_by(t))),
        case("zero_span_is_nan", xt([1.0, None, 3.0], [0, 0, 0]), sel(x.interpolate_by(t)), note="equal by on both sides is a zero span"),
        case("duplicate_by", xt([1.0, None, 3.0], [0, 0, 4]), sel(x.interpolate_by(t))),
        case("forty_rows_by_unsorted", xt(FORTY, BY), sel(x.interpolate_by(t))),
        case("integers_to_float", xt([1, None, 3], [0, 1, 4], xdtype=pl.Int64), sel(x.interpolate_by(t))),
        case("float32", xt([1.1, None, None, 7.3, None, 2.9], [0, 0.3, 1.7, 2.2, 5, 6.1], xdtype=pl.Float32, tdtype=pl.Float64),
             sel(x.interpolate_by(t)), note="a Float32 stays one, computed in float32"),
        case("float32_fourteen", xt(F32, [47, 2, 42, 3, 8, 14, 34, 46, 28, 33, 26, 13, 37, 5], xdtype=pl.Float32), sel(x.interpolate_by(t)),
             note="rows 4 and 7 are not what float64 rounded to float32 gives"),
        case("date_by", pl.DataFrame({"x": pl.Series([1.0, None, 3.0]), "t": pl.Series([date(2020, 1, 1), date(2020, 1, 2), date(2020, 1, 5)], dtype=pl.Date)}),
             sel(x.interpolate_by(t)), note="a Date by counts days"),
        case("datetime_ns_big_ticks", pl.DataFrame({
            "x": pl.Series([3.25, None, None, -7.5]),
            "t": pl.Series([1_700_000_000_123_456_789, 1_700_000_000_123_580_246, 1_700_000_001_111_111_110, 1_700_000_001_123_456_796],
                           dtype=pl.Int64).cast(pl.Datetime("ns"))}),
             sel(x.interpolate_by(t)), note="a Datetime of big ticks is subtracted in integers"),
        case("nan_by_sorts_last", xt([1.0, None, 3.0, None], [0.0, NAN, 2.0, 1.0], tdtype=pl.Float64), sel(x.interpolate_by(t))),
        case("nan_by_first", xt([5.0, None, 3.0], [NAN, 0.0, 1.0], tdtype=pl.Float64), sel(x.interpolate_by(t))),
        case("per_group", xt([1.0, None, 3.0, 10.0, None, 30.0], [0, 1, 4, 0, 3, 4], g=pl.Series("g", [1, 1, 1, 2, 2, 2], dtype=pl.Int64)),
             sel(x.interpolate_by(t).over("g"))),
        case("per_group_by_mixed", xt([3.0, 1.0, None, None, None, 30.0, 10.0, 20.0], [4, 0, 1, 2, 3, 4, 0, 1],
                                      g=pl.Series("g", [1, 1, 1, 1, 2, 2, 2, 2], dtype=pl.Int64)),
             sel(x.interpolate_by(t).over("g")), note="by out of order within each group"),
        # --- generated ---------------------------------------------------------------------
        case("interpolate_by_gen", gen, sel(x.interpolate_by(t))),
        case("interpolate_by_f32_gen", gen_f32, sel(x.interpolate_by(t))),
        case("interpolate_by_over_gen", gen_over, sel(x.interpolate_by(t).over("g"))),
        case("interpolate_by_dates_gen", gen_dates, sel(x.interpolate_by(t))),
    ]
