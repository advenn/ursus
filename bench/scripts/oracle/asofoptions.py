"""The asofoptions group: an as-of join's tolerance, a number on a numeric key and an
interval of zero admitting an exact match alone; its keys kept apart with
coalesce=False; and join_where with a suffix (asofoptions_test.go, J1 of
v0.6.7-scope.md, step 315). Not here: the plan, the schema names and the refusals,
which feature-gaps.md §9 documents (a negative tolerance, a number on an instant).
"""

from __future__ import annotations

from datetime import datetime, timedelta

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
L = pl.DataFrame({"k": [1, 5, 10, 20]})
R = pl.DataFrame({"k": [0, 4, 9, 30], "w": ["a", "b", "c", "d"]})
LF = pl.DataFrame({"k": [1.0, 5.5, 10.0]})
RF = pl.DataFrame({"k": [0.9, 4.0, 9.95], "w": ["a", "b", "c"]})
TEN = datetime(2024, 1, 1, 10)
LT = pl.DataFrame({"t": [TEN]})
RT_MET = pl.DataFrame({"t": [TEN], "w": ["x"]})
RT_MISSED = pl.DataFrame({"t": [TEN - timedelta(minutes=1)], "w": ["x"]})


def asof(**opts):
    return lambda left, right: left.join_asof(right, on="k", **opts).select("w")


def cases() -> list[Case]:
    g = Gen(5013)
    gl = g.frame(g.ints(150, pl.Int64, lo=0, hi=2000, null_rate=0, edges=False, name="k")).sort("k")
    gr = g.frame(g.ints(100, pl.Int64, lo=0, hi=2000, null_rate=0, edges=False, name="k")).sort("k").with_row_index("w")
    gr = gr.with_columns(c("w").cast(pl.Int64))
    ga = g.frame(g.ints(60, pl.Int64, lo=0, hi=100, edges=False, name="a"), g.ints(60, pl.Int64, lo=-50, hi=50, edges=False, name="x"))
    gb = g.frame(g.ints(50, pl.Int64, lo=0, hi=100, edges=False, name="b"), g.ints(50, pl.Int64, lo=-50, hi=50, edges=False, name="x"))
    return [
        case("tolerance_1", {"left": L, "right": R}, asof(tolerance=1)),
        case("tolerance_0_exact", {"left": L, "right": R}, asof(tolerance=0)),
        case("forward_1", {"left": L, "right": R}, asof(tolerance=1, strategy="forward")),
        case("nearest_2", {"left": L, "right": R}, asof(tolerance=2, strategy="nearest")),
        case("tolerance_1_5", {"left": L, "right": R}, asof(tolerance=1.5), note="a distance of integers is whole: 1.5 admits 1"),
        case("tolerance_int64_1", {"left": L, "right": R}, asof(tolerance=1), note="the same tolerance as a Go int64"),
        case("float_0_1", {"left": LF, "right": RF}, asof(tolerance=0.1), note="inclusive: 1.0 to 0.9 is 0.1"),
        case("float_1", {"left": LF, "right": RF}, asof(tolerance=1)),
        case("zero_interval_met", {"left": LT, "right": RT_MET},
             lambda left, right: left.join_asof(right, on="t", tolerance="0s").select("w")),
        case("zero_interval_missed", {"left": LT, "right": RT_MISSED},
             lambda left, right: left.join_asof(right, on="t", tolerance="0s").select("w")),
        case("zero_duration_missed", {"left": LT, "right": RT_MISSED},
             lambda left, right: left.join_asof(right, on="t", tolerance=timedelta(0)).select("w"),
             note="the zero as a Go time.Duration, FromDuration(0)"),
        case("no_tolerance_met", {"left": LT, "right": RT_MISSED}, lambda left, right: left.join_asof(right, on="t").select("w")),
        case("coalesce_false", {"left": L, "right": R}, lambda left, right: left.join_asof(right, on="k", coalesce=False)),
        case("join_where_suffix", {"a": pl.DataFrame({"a": [1, 2], "x": [10, 20]}), "b": pl.DataFrame({"b": [1, 2], "x": [5, 25]})},
             lambda a, b: a.join_where(b, c("x") < c("x_r"), suffix="_r"), order="set"),
        case("tolerance_gen", {"left": gl, "right": gr},
             lambda left, right: left.join_asof(right, on="k", tolerance=5).rename({"w": "back"}).join_asof(
                 right, on="k", tolerance=5, strategy="forward").rename({"w": "fwd"}).select("back", "fwd"),
             note="backward and forward under one tolerance over sorted keys with repeats"),
        case("nearest_tie_gen", {"left": gl, "right": gr},
             lambda left, right: left.join_asof(right, on="k", tolerance=5, strategy="nearest").select("w"),
             note="a finding: where the backward and forward rows are equally far, Polars 2.1 takes the forward one, ursus the "
                  "backward (asof.go: ties going backward)"),
        case("join_where_gen", {"a": ga, "b": gb}, lambda a, b: a.join_where(b, c("x") < c("x_r"), suffix="_r"), order="set"),
    ]
