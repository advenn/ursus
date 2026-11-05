"""The frameagg group: a frame's sum, mean, median, min, max, std, var, quantile,
product and null_count over every type (frameagg_test.go, C3 of v0.6.7-scope.md, step
300). Where feature-gaps.md §9 says ursus's aggregate answers in another type (an
Int128 sum, a Float64 product, a Uint64 count) or value (an empty sum or product null,
not 0 or 1), the Go side names the difference; a float var or std is Welford's here
and two passes in Polars, an ulp apart, so those are held to rel:1e-15 with the reason.
Not here: the eager and spilled differentials.
"""

from __future__ import annotations

from datetime import date, datetime

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
DF = pl.DataFrame({
    "i": [1, 2, None, 4], "i8": pl.Series([1, 2, 3, 4], dtype=pl.Int8), "f": [1.5, None, 2.5, 4.0], "b": [True, False, True, None],
    "s": ["p", "q", None, "r"], "d": [date(2026, 1, 1), date(2026, 1, 3), None, date(2026, 1, 8)],
    "t": pl.Series([datetime(2026, 1, 1, 6), None, datetime(2026, 1, 2, 6), datetime(2026, 1, 3, 6)], dtype=pl.Datetime("us")),
    "u": pl.Series([3_600_000_000_000, 7_200_000_000_000, None, 14_400_000_000_000], dtype=pl.Int64).cast(pl.Duration("ns")),
    "l": pl.Series([["1"], ["2", "3"], ["4"], ["5"]], dtype=pl.List(pl.String)),
})
EMPTY = DF.select("i", "s").head(0)
ULP = "a float var or std is Welford's one pass here, two passes in Polars: an ulp apart (§9)"


def agg(name: str, *args):
    return lambda df: getattr(df, name)(*args)


def cases() -> list[Case]:
    g = Gen(5022)
    gen = g.frame(g.ints(200, pl.Int64, lo=-10_000, hi=10_000, name="i"), g.floats(200, edges=False, name="f"),
                  g.ints(200, pl.Int32, lo=-100, hi=100, name="i32"))
    return [
        case("sum", DF, agg("sum")),
        case("mean", DF, agg("mean")),
        case("median", DF, agg("median")),
        case("min", DF, agg("min")),
        case("max", DF, agg("max")),
        case("std", DF, agg("std", 1), float="rel:1e-15", note=ULP),
        case("var", DF, agg("var", 1), float="rel:1e-15", note=ULP),
        case("var_ddof0", DF.select("i"), agg("var", 0), float="rel:1e-15", note=ULP),
        case("product", DF, agg("product"), note="a column a product does not take is of the Null type"),
        case("quantile_nearest", DF, agg("quantile", 0.5, "nearest"), note="a Boolean's quantile is null, a Date's a Datetime"),
        case("null_count", DF, agg("null_count")),
        case("empty_sum", EMPTY, agg("sum")),
        case("empty_mean", EMPTY, agg("mean")),
        case("empty_min", EMPTY, agg("min")),
        case("empty_null_count", EMPTY, agg("null_count")),
        case("empty_product", EMPTY, agg("product")),
        case("gen_sum", gen, lambda df: df.sum().with_columns(c("i32").cast(pl.Int64)), float="rel:1e-12",
             note="the Int128 sums here cast to Int64 on the Go side; the float sum is exact here, Polars' a few ulps off (§9)"),
        case("gen_min", gen, agg("min")),
        case("gen_max", gen, agg("max")),
        case("gen_mean", gen, agg("mean"), float="rel:1e-12", note="ursus's float sum is exact, Polars' a few ulps off (§9)"),
        case("gen_median", gen, agg("median")),
        case("gen_null_count", gen, agg("null_count")),
        case("gen_var", gen, agg("var", 1), float="rel:1e-12", note=ULP),
    ]
