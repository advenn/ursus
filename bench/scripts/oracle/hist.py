"""The hist group: Polars' hist (hist_test.go, tier 2 of v0.6.6-scope.md): the counts
alone, and with the breakpoints and the categories, which both sides unnest so the
Categorical labels compare as text. The edges are compared by their bits.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

nan = float("nan")
x = pl.col("x")

H = pl.DataFrame({"x": pl.Series([1.0, 2, 2.5, 4, 8, None, nan], dtype=pl.Float64)})
LABEL_BINS = [-1234567.891, -2, 1e-7, 0.000000123456, 0.1, 12.0000000001, 1234567, 1234567.891, 1e7, 1.5e300]


def f64(*vals: float) -> pl.DataFrame:
    return pl.DataFrame({"x": pl.Series("x", list(vals), dtype=pl.Float64)})


def counts(e: pl.Expr):
    return lambda df: df.select(e)


def parts(e: pl.Expr):
    """The hist's struct unnested: breakpoint, category and count as columns."""
    return lambda df: df.select(e).unnest("x")


BP, CAT = dict(include_breakpoint=True), dict(include_breakpoint=True, include_category=True)


def cases() -> list[Case]:
    gen = Gen(5006)
    gen_floats = gen.frame(gen.floats(200, lo=-500, hi=500, edges=False))
    gen_ints = gen.frame(gen.ints(120, pl.Int64, lo=-40, hi=40))
    return [
        case("counts_10", H, counts(x.hist())),
        case("counts_3", H, counts(x.hist(bin_count=3))),
        case("counts_0", H, counts(x.hist(bin_count=0)), note="no bin"),
        case("counts_edges", H, counts(x.hist(bins=[0, 2, 5]))),
        case("counts_one_edge", H, counts(x.hist(bins=[3])), note="one edge, no bin"),
        case("both_3", H, parts(x.hist(bin_count=3, **CAT))),
        case("both_4", H, parts(x.hist(bin_count=4, **CAT))),
        case("both_edges", H, parts(x.hist(bins=[0, 2, 5], **CAT))),
        case("breakpoint_edges", H, parts(x.hist(bins=[0, 2, 5], **BP))),
        case("on_the_edges", f64(0, 2, 5, 5.0001, -1), parts(x.hist(bins=[0, 2, 5], **BP))),
        case("integers", pl.DataFrame({"x": pl.Series([1, 2, 2, 3, 10], dtype=pl.Int64)}), parts(x.hist(bin_count=4, **BP))),
        case("alike", f64(5, 5), parts(x.hist(bin_count=2, **BP))),
        case("no_rows", f64(), parts(x.hist(bin_count=2, **BP))),
        case("one_bin", H, parts(x.hist(bin_count=1, **BP))),
        case("last_edge_greatest", f64(-5.7, -1, 3), parts(x.hist(bin_count=5, **BP)),
             note="the last edge is the greatest value itself, where 5 widths from -5.7 would be 2.999999999999999"),
        case("f32", H, parts(x.cast(pl.Float32).hist(bin_count=2, **BP))),
        case("nan_alone", f64(nan, nan), parts(x.hist(bin_count=2, **BP))),
        case("nan_once", f64(nan), parts(x.hist(bin_count=2, **BP))),
        case("nan_beside", f64(nan, 1, 3), parts(x.hist(bin_count=2, **BP))),
        case("labels", f64(0.5), parts(x.hist(bins=LABEL_BINS, include_category=True)),
             note="Polars' float display in the labels: one decimal for a whole number, scientific notation for a long one"),
        case("twenty_in_10", f64(*[float(k) for k in range(20)]), parts(x.hist(**BP))),
        case("by_group", pl.DataFrame({"x": [1.0, 2, 3, 4], "g": pl.Series([1, 1, 2, 2], dtype=pl.Int64)}),
             lambda df: df.group_by("g").agg(x.hist(bin_count=2)).sort("g"), note="a list a group"),
        # --- generated -------------------------------------------------------------------------
        case("both_gen", gen_floats, parts(x.hist(bin_count=7, **CAT))),
        case("edges_gen", gen_floats, parts(x.hist(bins=[-500.0, -100.0, -0.5, 0.0, 0.5, 100.0, 500.0], **CAT))),
        case("counts_gen", gen_floats, counts(x.hist())),
        case("ints_gen", gen_ints, parts(x.hist(bin_count=5, **BP))),
    ]
