"""The aggstats group: skew and kurtosis, the bitwise and small aggregates, entropy,
mode and Spearman's correlation (aggstats_test.go, M1 of v0.6.6-scope.md). The moments
and the entropies are held to rel:1e-12: Polars takes two passes where ursus merges
one, so their last bits part. A mode's order over the frame is Polars' hash table's,
so those cases are sets; by group, Polars' maintain_order gives the order the values
first appear, and its list.sort the sorted order ursus gives without it.

Not here: a one-value group's raw entropy, 0.0 in Polars 2.1 and −x·ln x here, which
the Go registry skips as a finding; the four-thread merge and the refusals.
"""

from __future__ import annotations

import math

import polars as pl

from . import Case, case
from .gen import Gen

nan, negz = float("nan"), -0.0
c = pl.col
x, i, y, g = c("x"), c("i"), c("y"), c("g")

OK = [True, True, True, False, True, True, True]
STATS = pl.DataFrame({
    "g": pl.Series([1, 1, 1, 1, 2, 2, 3], dtype=pl.Int64),
    "x": pl.Series([1.0, 2, 4, None, 3, 3, 5], dtype=pl.Float64),
    "i": pl.Series([6, 3, 5, None, 12, 10, 7], dtype=pl.Int64),
    "y": pl.Series([2.0, 1, 5, 7, None, 2, 1], dtype=pl.Float64),
})


def typed(vals: list, dtype: pl.DataType, name: str = "x") -> pl.DataFrame:
    return pl.DataFrame({name: pl.Series(name, vals, dtype=dtype)})


def f64(*vals: float) -> pl.DataFrame:
    return typed(list(vals), pl.Float64)


def sel(*exprs: pl.Expr):
    return lambda df: df.select(*exprs)


def by_g(*exprs: pl.Expr):
    return lambda df: df.group_by("g").agg(*exprs).sort("g")


def far2000() -> pl.DataFrame:
    """Two thousand values near 1e9: unshifted, a running mean rounds and the skew is
    wrong in its ninth digit."""
    return f64(*[1e9 + float((k * k) % 7) + float(k % 3) / 4 for k in range(2000)])


MOMENTS = {
    "skew": x.skew(bias=True), "skew_unbiased": x.skew(bias=False), "kurtosis": x.kurtosis(fisher=True, bias=True),
    "kurtosis_pearson": x.kurtosis(fisher=False, bias=True), "kurtosis_unbiased": x.kurtosis(fisher=True, bias=False),
}
SMALL = {
    "and": i.bitwise_and(), "or": i.bitwise_or(), "xor": i.bitwise_xor(), "has_nulls": x.has_nulls(), "is_empty": x.is_empty(),
    "nan_min": x.nan_min(), "approx_n_unique": i.approx_n_unique().cast(pl.Int64), "dot": x.dot(y),
}
ZERO_SKEW = dict(float="rel:1e-12 abs:1e-15", note="symmetric values: exactly 0 in Polars' two passes, a rounding's 1e-16 in ursus's one")
INEXACT = dict(float="rel:1e-12", note="Polars 2.1 takes a float mean at 1e9 and loses seven digits of the skew; ursus shifts by the first value: "
                                      "a finding, skipped, held to an exact big.Float in the Go test")
ENTROPIES = {"entropy": x.entropy(math.e, normalize=True), "entropy_bits": x.entropy(2, normalize=True),
             "spearman": pl.corr(x, y, method="spearman")}
TOL = dict(float="rel:1e-12", note="Polars' two passes against ursus's one, merged: the last bits part")


def cases() -> list[Case]:
    gen = Gen(5002)
    n = 180
    keys = gen.ints(n, pl.Int64, lo=0, hi=2, null_rate=0, edges=False, name="g")
    gen_moments = gen.frame(keys, gen.floats(n, lo=-100, hi=100, edges=False, name="x"),
                            gen.ints(n, pl.Int64, lo=0, hi=30, name="i"), gen.floats(n, lo=-100, hi=100, edges=False, name="y"))
    gen_entropy = gen.frame(keys, gen.floats(n, lo=0.5, hi=100, edges=False, name="x"), gen.ints(n, pl.Int64, lo=1, hi=30, name="i"),
                            gen.floats(n, lo=-100, hi=100, edges=False, name="y"))
    gen_mode = gen.frame(keys, gen.ints(n, pl.Int64, lo=0, hi=5, name="i"), gen.strings(n, alphabet="abc", maxlen=1, name="s"),
                         gen.floats(n, lo=0, hi=3, edges=False, ties=True, name="f"))
    out = [
        # --- skew and kurtosis -----------------------------------------------------------
        *[case(f"{name}_all", STATS, sel(e), **(ZERO_SKEW if name.startswith("skew") else TOL)) for name, e in MOMENTS.items()],
        *[case(f"{name}_by_g", STATS, by_g(e), **TOL) for name, e in MOMENTS.items()],
        case("skew_unbiased_nulls_by_g", STATS, by_g(x.skew(bias=False).is_null()), note="the unbiased skew below 3 values is null"),
        case("kurtosis_unbiased_nulls_by_g", STATS, by_g(x.kurtosis(fisher=True, bias=False).is_null()),
             note="the unbiased kurtosis below 4 values is null"),
        case("skew_one_value_not_null_by_g", STATS, by_g(x.skew(bias=True).is_null()), note="the skew of one value is NaN, not null"),
        case("skew_ints", STATS, sel(i.skew(bias=True)), **TOL),
        case("skew_near_1e9", f64(1e9 + 1, 1e9 + 2, 1e9 + 4), sel(x.skew(bias=True)), **INEXACT),
        case("moments_far_2000", far2000(), sel(x.skew(bias=True), x.kurtosis(fisher=False, bias=True).alias("k")), **INEXACT),
        case("moments_all_null", typed([None, None], pl.Float64), sel(x.skew(bias=True), x.kurtosis(fisher=True, bias=True).alias("k"))),
        case("skew_nan", f64(1, nan, 3), sel(x.skew(bias=True))),
        # --- the bitwise and small aggregates ----------------------------------------------
        case("small_all", STATS, sel(*[e.alias(name) for name, e in SMALL.items()])),
        case("small_by_g", STATS, by_g(*[e.alias(name) for name, e in SMALL.items()])),
        case("bitwise_bools", typed([True, False, True, None], pl.Boolean, "b"),
             sel(c("b").bitwise_and().alias("a"), c("b").bitwise_or().alias("o"), c("b").bitwise_xor().alias("x"))),
        case("bitwise_narrow", pl.DataFrame({"a": pl.Series([1, 2, 3], dtype=pl.Int8), "u": pl.Series([1, 2, 3], dtype=pl.UInt16)}),
             sel(c("a").bitwise_or(), c("u").bitwise_xor(), c("a").bitwise_and().alias("and8")), note="the input's type kept"),
        case("small_all_null", typed([None, None], pl.Int64, "i"),
             sel(i.bitwise_and().alias("a"), i.has_nulls().alias("h"), i.approx_n_unique().cast(pl.Int64).alias("n"), i.is_empty().alias("e")),
             note="a count is a Uint64 here, Polars' a UInt32 (§9): both cast to Int64"),
        case("small_no_rows", typed([], pl.Int64, "i"),
             sel(i.bitwise_or().alias("o"), i.is_empty().alias("e"), i.approx_n_unique().cast(pl.Int64).alias("n"))),
        case("nan_min_max", f64(1, nan, 3), sel(x.nan_min().alias("nmin"), x.min().alias("min"), x.nan_max().alias("nmax"))),
        case("dot_ints", pl.DataFrame({"a": pl.Series([1, 2], dtype=pl.Int64), "b": pl.Series([3, 4], dtype=pl.Int64)}),
             sel(c("a").dot(c("b")).cast(pl.Int64)), note="an integer dot is a Sum, an Int128 here: both cast to Int64"),
        # --- entropy and Spearman -----------------------------------------------------------
        *[case(f"{name}_all", STATS, sel(e.alias("r")), **TOL) for name, e in ENTROPIES.items()],
        *[case(f"{name}_by_g", STATS, by_g(e.alias("r")), **TOL) for name, e in ENTROPIES.items()],
        case("entropy_raw_all", STATS, sel(x.entropy(math.e, normalize=False)), **TOL),
        case("entropy_raw_by_g", STATS, by_g(x.entropy(math.e, normalize=False)), float="rel:1e-12",
             note="Polars 2.1's raw entropy of a one-value group is 0.0, where -x ln x is -8.05: a finding, skipped"),
        case("entropy_ints", STATS, sel(i.entropy(math.e, normalize=True)), **TOL),
        case("entropy_zero", f64(0, 1, 3), sel(x.entropy(math.e, normalize=True)), note="0 ln 0 is NaN"),
        case("entropy_negative", f64(-1, 1, 3), sel(x.entropy(math.e, normalize=True))),
        case("entropy_base_1", STATS, sel(x.entropy(1, normalize=True)), note="ln 1 is 0: infinite"),
        case("entropy_f32", STATS, sel(x.cast(pl.Float32).entropy(2, normalize=True)), float="rel:1e-6",
             note="a Float32's entropy is a Float32, within its own ulps"),
        case("entropy_none", typed([None, None], pl.Float64), sel(x.entropy(math.e, normalize=True)),
             note="Polars' entropy of none is -0.0; null here, as Sum's is (§9): oracleDifferences"),
        case("spearman_ties", pl.DataFrame({"a": pl.Series([1, 1, 2, 3, 3, 3], dtype=pl.Int64), "b": pl.Series([5, 6, 6, 7, 1, 1], dtype=pl.Int64)}),
             sel(pl.corr("a", "b", method="spearman")), **TOL),
        case("spearman_ints", pl.DataFrame({"a": pl.Series([1, 2, 3, 4], dtype=pl.Int64), "b": pl.Series([1, 3, 2, 4], dtype=pl.Int64)}),
             sel(pl.corr("a", "b", method="spearman")), **TOL),
        case("spearman_constant", pl.DataFrame({"a": pl.Series([1, 1, 1], dtype=pl.Int64), "b": pl.Series([1, 3, 2], dtype=pl.Int64)}),
             sel(pl.corr("a", "b", method="spearman"))),
        case("spearman_nan", pl.DataFrame({"a": [1.0, nan, 2, 3], "b": [5.0, 6, 1, 7]}), sel(pl.corr("a", "b", method="spearman")), **TOL),
        case("spearman_nulls", pl.DataFrame({"a": [1.0, None, 2, 3], "b": [5.0, 6, None, 7]}), sel(pl.corr("a", "b", method="spearman")), **TOL),
        # --- mode -----------------------------------------------------------------------------
        case("mode", STATS, sel(x.mode()), order="set"),
        case("mode_ties_sorted", typed([3, 1, 3, 1, 2], pl.Int64, "a"), sel(c("a").mode()), order="set",
             note="Polars' order is its hash table's; ursus's Mode(false) is sorted"),
        case("mode_ties_first_seen", typed([3, 1, 3, 1, 2], pl.Int64, "a"), sel(c("a").mode(maintain_order=True))),
        case("mode_null_among", typed(["b", "a", "b", None, None], pl.String, "a"), sel(c("a").mode()), order="set"),
        case("mode_nan", f64(1, nan, nan).rename({"x": "a"}), sel(c("a").mode())),
        case("mode_zeros", typed([0.0, negz, 1.0], pl.Float64, "a"), sel(c("a").mode()), note="-0 and 0 are one value: the first seen"),
        case("mode_bools", typed([True, False, True], pl.Boolean, "a"), sel(c("a").mode())),
        case("mode_no_rows", typed([], pl.Int64, "a"), sel(c("a").mode())),
        case("mode_by_g_first_seen", STATS, by_g(i.mode(maintain_order=True))),
        case("mode_by_g_sorted", STATS, by_g(x.mode().sort()), note="ursus's Mode(false) is sorted, nulls first, as list.sort sorts"),
        # --- generated -----------------------------------------------------------------------
        case("moments_gen", gen_moments, by_g(*[e.alias(name) for name, e in MOMENTS.items()]), **TOL),
        case("small_gen", gen_moments, by_g(*[e.alias(name) for name, e in SMALL.items()]), float="rel:1e-12",
             note="approx_n_unique is exact in both under 64 distinct values; a dot's sum of products is an ulp apart in its order"),
        case("entropy_spearman_gen", gen_entropy, by_g(*[e.alias(name) for name, e in ENTROPIES.items()], i.entropy(2, normalize=True).alias("ient")),
             **TOL),
        case("mode_gen_by_g", gen_mode, by_g(i.mode(maintain_order=True).alias("i_first"), i.mode().sort().alias("i_sorted"),
                                             c("s").mode(maintain_order=True).alias("s_first"), c("s").mode().sort().alias("s_sorted"),
                                             c("f").mode().sort().alias("f_sorted"))),
        case("mode_gen_frame", gen_mode, sel(i.mode()), order="set"),
    ]
    return out
