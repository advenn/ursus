"""The windownest group: a window inside a window, and an aggregate inside an
aggregate's input, computed beneath it in its scope (windownest_test.go, step 336).

A sum of only a null is null here and 0 in Polars (feature-gaps.md §9):
sum_over_g_within_h and sum_over_g_of_sum_over_g are in oracleDifferences. Spelled
around: an integer Sum is an Int128 here and a count a Uint64, so those are cast to
Int64 in the spelling.

Not here: the refusals of an ordering over an aggregate, and the spill past a limit.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
x, t = c("x"), c("t")

NEST = pl.DataFrame({
    "g": ["a", "a", "b", "a", "b", "b"],
    "h": pl.Series([1, 1, 1, 2, 2, 2], dtype=pl.Int64),
    "x": pl.Series([1.0, 4, 2, None, 8, 3], dtype=pl.Float64),
    "t": pl.Series([3, 1, 2, 6, 5, 4], dtype=pl.Int64),
})
WF = pl.DataFrame({"g": ["a", "b", "a", "b", "a", "c"], "v": pl.Series([1, 10, 3, 20, 3, None], dtype=pl.Int64),
                   "t": pl.Series([0, 1, 2, 3, 4, 5], dtype=pl.Int64)})
KEYED = pl.DataFrame({"k": pl.Series([0, None, 0, None, 5], dtype=pl.Int32), "v": pl.Series([1, 2, 3, 4, 5], dtype=pl.Int64)})


def sel(e: pl.Expr):
    return lambda df: df.select(e.alias("r"))


def by_g(e: pl.Expr):
    return lambda df: df.group_by("g").agg(e.alias("r")).sort("g")


WINDOWS = {
    "shifted_then_summed": x.shift(1).cum_sum(),
    "summed_then_shifted": x.cum_sum().shift(1),
    "within_g": x.shift(1).cum_sum().over("g"),
    "diff_within_g": x.diff(1).cum_sum().over("g"),
    "agg_of_shift_within_g": x.shift(1).sum().over("g"),
    "within_g_by_t": x.shift(1).cum_sum().over("g", order_by="t"),
    "over_g_within_h": x.shift(1).over("g").cum_sum().over("h"),
    "over_g_within_h_by_t": x.shift(1).over("g").cum_sum().over("h", order_by="t"),
    "over_g_by_t_within_h": x.shift(1).over("g", order_by="t").cum_sum().over("h"),
    "shift_within_h_by_t": x.shift(1).cum_sum().over("h", order_by="t"),
    "over_g_then_frame": x.shift(1).over("g").cum_sum(),
    "sum_over_g_then_frame": x.sum().over("g").cum_sum(),
    "sum_over_g_within_h": x.sum().over("g").cum_sum().over("h"),
    "mean_of_ranks_within_g": x.rank("average").mean().over("g"),
    "mean_in_running_sum_within_g": (x - x.mean()).cum_sum().over("g"),
    "mean_in_running_sum_within_g_by_t": (x - x.mean()).cum_sum().over("g", order_by="t"),
}
AGGS = {
    "dist_sum": (x - x.mean()).abs().sum(),
    "max_share": (x / x.sum()).max(),
    "count_over_mean": x.filter(x > x.mean()).count().cast(pl.Int64),
    "mean_of_shift": x.shift(1).mean(),
    "mean_of_sum": x.sum().mean(),
    "var_by_hand": (x - x.mean()).pow(2).mean(),
    "max_dist": (x - x.mean()).max(),
    "t_of_least_dist": t.sort_by(x - x.mean()).first(),
}


def cases() -> list[Case]:
    gen = Gen(5012)
    n = 150
    order = [int(k) for k in gen.rng.permutation(n)]
    gen_nest = gen.frame(gen.strings(n, null_rate=0, edges=False, alphabet="abc", maxlen=1, name="g"),
                         gen.ints(n, pl.Int64, lo=0, hi=1, null_rate=0, edges=False, name="h"),
                         gen.floats(n, lo=-20, hi=20, edges=False, name="x"), pl.Series("t", order, dtype=pl.Int64))
    return [
        *[case(name, NEST, sel(e), **({"note": "Polars sums h = 2's g = a, only a null, to 0; null here (§9): oracleDifferences"}
                                      if name == "sum_over_g_within_h" else {})) for name, e in WINDOWS.items()],
        case("sum_over_g_of_sum_over_g", WF, sel(c("v").sum().over("g").sum().over("g")),
             note="each group's sum times its rows; c's sum of only a null is null here (§9): oracleDifferences; ursus's Int128 cast"),
        *[case(f"{name}_frame", NEST, sel(e)) for name, e in AGGS.items()],
        *[case(f"{name}_by_g", NEST, by_g(e)) for name, e in AGGS.items()],
        case("dist_sum_with_columns", NEST, lambda df: df.with_columns(AGGS["dist_sum"].alias("r")), note="broadcast beside the frame"),
        case("dist_sum_select_beside", NEST, lambda df: df.select(x, AGGS["dist_sum"].alias("r"))),
        case("mix_sub_mean_by_g", NEST, by_g(x - x.mean()), note="a mix of an aggregate and a row's value, a list a group"),
        case("mix_sum_add_by_g", NEST, by_g(x.sum() + x)),
        case("dist_sum_over_g", NEST, lambda df: df.with_columns(AGGS["dist_sum"].over("g").alias("r"))),
        case("int_key_null_beside_zero", KEYED, lambda df: df.select(c("v").sum().over("k").alias("s"), c("v").cum_sum().over("k").alias("c")),
             note="a null key is its own partition, not 0's; ursus's Int128 sums cast to Int64"),
        # --- generated -----------------------------------------------------------------------------
        case("windows_gen", gen_nest, lambda df: df.select(*[e.alias(name) for name, e in WINDOWS.items()]), float="rel:1e-12 abs:1e-12",
             note="a running sum of 150 values: an ulp apart in its order; a running sum of deviations from the mean ends near 0, its residual the whole value"),
        case("aggregates_gen", gen_nest, lambda df: df.group_by("g").agg(*[e.alias(name) for name, e in AGGS.items()]).sort("g"), float="rel:1e-12",
             note="a sum of 50 values a group: an ulp apart in its order"),
        case("aggregates_over_gen", gen_nest, lambda df: df.select(
            AGGS["dist_sum"].over("g").alias("dist_sum"), AGGS["var_by_hand"].over("h").alias("var_h"), AGGS["max_dist"].over("g", "h").alias("max_dist_gh"))),
    ]
