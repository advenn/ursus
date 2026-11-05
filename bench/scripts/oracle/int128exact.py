"""The int128exact group: the Int128 boundary, where a 128-bit total becomes a
number a caller reads (int128exact_test.go): a sum cast to an Int64 or a Uint64, a
float cast to an Int128, an Int128 column's own sum and running sum. Each is asked of
an Int128 column, which Polars 2.1 has; a generated case follows.

Not here, with the reason: an Int64 column's Sum, which Polars totals as an Int64 and
wraps past 2^63 where ursus widens it to an Int128 (feature-gaps.md §9; the Go test
holds it to math/big over seeded values); a float with a fraction cast to an Int128,
which Polars truncates and ursus refuses (§9; the refusal in the Go test, the lossy
null recorded as the difference); a sum or a running sum past 2^127, refused here and
wrapped in Polars (the Go test keeps the refusals).
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

v, f = pl.col("v"), pl.col("f")
MAX = (1 << 127) - 1


def i128(vals: list[int], name: str = "v") -> pl.DataFrame:
    return pl.DataFrame({name: pl.Series(name, vals, dtype=pl.Int128)})


def sel(*e: pl.Expr):
    return lambda df: df.select(*e)


def cases() -> list[Case]:
    g = Gen(4983)
    gen_i128 = g.frame(g.ints(120, pl.Int128, lo=-(1 << 120), hi=1 << 120, name="v"))
    gen_whole = g.frame(pl.Series("f", [None if x is None else float(int(x)) for x in g.floats(120, lo=-1e20, hi=1e20, edges=False).to_list()], dtype=pl.Float64))
    return [
        case("sum_cast_i64", pl.DataFrame({"v": pl.Series([1 << 53, 1], dtype=pl.Int64)}), sel(v.sum().cast(pl.Int64)),
             note="2^53 + 1 exactly, not through a float"),
        case("i128_sum_cast_u64", i128([(1 << 63) - 1, (1 << 63) - 1]), sel(v.sum().cast(pl.UInt64))),
        case("float_to_i128_lossy", pl.DataFrame({"f": [3.7]}), sel(f.cast(pl.Int128, strict=False)),
             note="Polars truncates 3.7 to 3; ursus's casts are exact or refused, null when lossy"),
        case("float_to_i128_whole", pl.DataFrame({"f": [-3e20]}), sel(f.cast(pl.Int128))),
        case("i128_cum_sum_to_max", i128([MAX - 1, 1]), sel(v.cum_sum())),
        case("i128_sum_max_1_minus_1", i128([MAX, 1, -1]), sel(v.sum()), note="the total is in range; only a prefix is not"),
        case("i128_gen", gen_i128, sel(v.cast(pl.Float64).alias("f64"), v.cast(pl.String).alias("text"), v.abs().alias("abs")),
             note="neg of an Int128 is not in Polars 2.1"),
        case("i128_sums_gen", gen_i128, sel(v.sum().alias("sum"), v.cum_sum().alias("cum_sum"))),
        case("float_to_i128_gen", gen_whole, sel(f.cast(pl.Int128))),
    ]
