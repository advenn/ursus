"""The rollmap group: rolling_rank_by and rolling_map (rollmap_test.go, step 338). A
rolling_map's Python function is the Go function's twin: a sum of the window's values,
its length with its nulls, the greater of two.

Not here: an ordinal rank's refusal, the names of two functions, a panic and an error
in the function, and the spill past a limit.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
x = c("x")

RM = pl.DataFrame({"t": pl.Series([1, 2, 3, 5, 6, 9, 9], dtype=pl.Int64), "x": pl.Series([3.0, 1, None, 3, 5, 2, 2], dtype=pl.Float64)})
G = pl.DataFrame({"g": ["a", "b", "a", "b", "a"], "o": pl.Series([5, 4, 3, 2, 1], dtype=pl.Int64), "x": pl.Series([1.0, 2, 3, 4, 5], dtype=pl.Float64)})


def sel(e: pl.Expr):
    return lambda df: df.select(e.alias("r"))


def wsum(s: pl.Series) -> float:
    return float(s.sum())


def wlen(s: pl.Series) -> float:
    return float(s.len())


def wmax(s: pl.Series) -> int:
    return int(s.max())


def cases() -> list[Case]:
    gen = Gen(5016)
    n = 150
    ts = [int(k) for k in gen.rng.integers(0, 3, n).cumsum()]
    gen_rm = pl.DataFrame({"t": pl.Series(ts, dtype=pl.Int64), "x": gen.floats(n, lo=-10, hi=10, edges=False, ties=True, name="x"),
                           "g": gen.ints(n, pl.Int64, lo=0, hi=2, null_rate=0, edges=False, name="g")})
    return [
        case("rank_by_average", RM, sel(x.rolling_rank_by("t", "3i", method="average"))),
        case("rank_by_min", RM, sel(x.rolling_rank_by("t", "3i", method="min"))),
        case("rank_by_max", RM, sel(x.rolling_rank_by("t", "3i", method="max"))),
        case("rank_by_dense", RM, sel(x.rolling_rank_by("t", "3i", method="dense"))),
        case("rank_by_min_samples_2", RM, sel(x.rolling_rank_by("t", "3i", method="average", min_samples=2))),
        case("rank_by_closed_both", RM, sel(x.rolling_rank_by("t", "3i", method="average", closed="both"))),
        case("map_sum_3", RM, sel(x.rolling_map(wsum, 3))),
        case("map_sum_3_min_samples_1", RM, sel(x.rolling_map(wsum, 3, min_samples=1))),
        case("map_sum_3_center", RM, sel(x.rolling_map(wsum, 3, center=True))),
        case("map_len_3", RM, sel(x.rolling_map(wlen, 3, min_samples=1)), note="a window holds its nulls: a length counts them"),
        case("map_max_ints", pl.DataFrame({"x": pl.Series([1, 2, 3, 4], dtype=pl.Int64)}), sel(x.rolling_map(wmax, 2))),
        case("map_sum_over_g_by_o", G, sel(x.rolling_map(wsum, 2, min_samples=1).over("g", order_by="o"))),
        # --- generated -----------------------------------------------------------------------------
        case("rank_by_gen", gen_rm, lambda df: df.select(
            x.rolling_rank_by("t", "4i", method="average").alias("avg"), x.rolling_rank_by("t", "4i", method="min").alias("min"),
            x.rolling_rank_by("t", "4i", method="max").alias("max"), x.rolling_rank_by("t", "4i", method="dense").alias("dense"),
            x.rolling_rank_by("t", "6i", method="average", min_samples=3).alias("avg6_3"),
            x.rolling_rank_by("t", "4i", method="min", closed="both").alias("min_both"),
            x.rolling_rank_by("t", "5i", method="average").over("g").alias("avg5_g"))),
        case("map_gen", gen_rm, lambda df: df.select(x.rolling_map(wsum, 4, min_samples=2).alias("sum4_2"), x.rolling_map(wlen, 3, min_samples=1).alias("len3"),
                                                     x.rolling_map(wsum, 3, min_samples=1, center=True).alias("sum3c"))),
    ]
