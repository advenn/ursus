"""The search group: search_sorted and index_of, peak_max and peak_min, unique_counts
and value_counts (search_test.go, M3 of v0.6.6-scope.md). A value_counts' row order
is Polars' hash table's where ursus gives the values as they first appear, so those
cases are sets; a group's value_counts is exploded and unnested so its rows compare as
a set too.

Not here: Hash, whose numbers are ursus's own (feature-gaps.md §9), held to its
properties in the Go test.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

nan, negz = float("nan"), -0.0
c = pl.col
a, s, x, f = c("a"), c("s"), c("x"), c("f")

SEARCH = pl.DataFrame({
    "g": pl.Series([1, 1, 1, 2, 2, 2], dtype=pl.Int64),
    "a": pl.Series([1, 3, 3, 5, None, 9], dtype=pl.Int64),
    "s": pl.Series(["x", "y", "x", None, "x", "z"], dtype=pl.String),
})
BA = pl.DataFrame({"g": pl.Series([1, 1, 1], dtype=pl.Int64), "s": ["b", "a", "a"]})


def ints(vals: list, name: str = "a") -> pl.DataFrame:
    return pl.DataFrame({name: pl.Series(name, vals, dtype=pl.Int64)})


def f64(vals: list, name: str = "x") -> pl.DataFrame:
    return pl.DataFrame({name: pl.Series(name, vals, dtype=pl.Float64)})


def sel(*exprs: pl.Expr):
    return lambda df: df.select(*exprs)


def by_g(*exprs: pl.Expr):
    return lambda df: df.group_by("g").agg(*exprs).sort("g")


PEAKS = sel(x.peak_max().alias("max"), x.peak_min().alias("min"))
NULLS = sel(a.search_sorted(None, "any").alias("na"), a.search_sorted(None, "left").alias("nl"), a.search_sorted(None, "right").alias("nr"),
            a.search_sorted(2, "left").alias("l"), a.search_sorted(2, "right").alias("r"))


def counts(e: pl.Expr):
    """A value_counts struct unnested into its value and count columns."""
    return lambda df: df.select(e.alias("v")).unnest("v")


def group_counts(e: pl.Expr):
    return lambda df: df.group_by("g").agg(e).explode("s").unnest("s")


def cases() -> list[Case]:
    gen = Gen(5007)
    n = 150
    sorted_a = pl.DataFrame({"a": gen.ints(n, pl.Int64, lo=-50, hi=50, null_rate=0.05, edges=False, name="a").sort()})
    gen_peaks = gen.frame(gen.floats(n, lo=-10, hi=10, edges=False, ties=True, name="x"))
    gen_counts = gen.frame(gen.ints(n, pl.Int64, lo=0, hi=3, null_rate=0, edges=False, name="g"),
                           gen.strings(n, alphabet="abcd", maxlen=1, name="s"), gen.ints(n, pl.Int64, lo=0, hi=5, name="a"))
    return [
        # --- search_sorted and index_of -------------------------------------------------------
        case("search_sorted_any", SEARCH, sel(a.search_sorted(3, "any"))),
        case("search_sorted_right", SEARCH, sel(a.search_sorted(3, "right"))),
        case("search_sorted_by_group", SEARCH, by_g(a.search_sorted(3, "left").first()), note="one position a group; Polars 2.1 gives it as a list of one in an aggregation, so its first"),
        case("search_sorted_descending", ints([5, 3, 3, 1]), sel(a.search_sorted(3, "left", descending=True).alias("l"),
                                                                 a.search_sorted(3, "right", descending=True).alias("r"))),
        case("search_sorted_descending_more_above", ints([9, 8, 7, 3, 1]), sel(a.search_sorted(4, "left", descending=True).alias("l"),
                                                                               a.search_sorted(7, "right", descending=True).alias("r"))),
        case("search_sorted_descending_nulls_first", ints([None, 3, 2, 1]), sel(a.search_sorted(2, "left", descending=True).alias("l"),
                                                                                a.search_sorted(2, "right", descending=True).alias("r"))),
        case("search_sorted_nan_last", f64([1, 2, nan], "a"), sel(a.search_sorted(nan, "left").alias("nan"), a.search_sorted(3.0, "left").alias("three"))),
        case("search_sorted_text", pl.DataFrame({"a": ["a", "c", "e"]}), sel(a.search_sorted("d", "left"))),
        case("search_sorted_no_rows", ints([]), sel(a.search_sorted(3, "left"))),
        case("search_sorted_all_null", ints([None, None]), sel(a.search_sorted(3, "left"))),
        case("search_sorted_nulls_first_1", ints([None, 1, 2, 3]), NULLS),
        case("search_sorted_nulls_first_2", ints([None, None, 1, 2]), NULLS),
        case("search_sorted_nulls_last_1", ints([1, 2, 3, None]), NULLS),
        case("search_sorted_nulls_last_2", ints([1, 2, None, None]), NULLS),
        case("search_sorted_no_nulls", ints([1, 2, 3]), NULLS),
        case("index_of", SEARCH, sel(a.index_of(3), s.index_of("x").alias("s"), a.index_of(None).alias("n"), a.index_of(100).alias("m"))),
        case("index_of_by_group", SEARCH, by_g(a.index_of(3))),
        case("index_of_nan_and_zero", f64([1, nan, negz], "a"), sel(a.index_of(nan).alias("nan"), a.index_of(0.0).alias("zero"))),
        case("index_of_null_where_none", ints([1, 2]), sel(a.index_of(None))),
        # --- peaks ----------------------------------------------------------------------------
        case("peaks_polars", ints([1, 3, 2, 4, 4, 1, 5, None, 2], "x"), PEAKS),
        case("peaks_nan", f64([1, nan, 2, 0.5, 3]), PEAKS, note="NaN above every number"),
        case("peaks_negatives", ints([-1, -2, -3], "x"), PEAKS),
        case("peaks_falling", ints([3, 2, 1], "x"), PEAKS),
        case("peaks_one", ints([5], "x"), PEAKS),
        case("peaks_below_zero", f64([-0.5, -1]), PEAKS, note="an end compared with its type's zero, as Polars fills its shifts with 0"),
        case("peaks_bools", pl.DataFrame({"x": [True, False]}), PEAKS),
        case("peaks_dates", ints([1, 3, 2], "x").select(x.cast(pl.Date)), PEAKS, note="against the epoch"),
        case("peak_max_over_g", pl.DataFrame({"g": pl.Series([1, 1, 1, 2, 2, 2], dtype=pl.Int64), "x": pl.Series([1, 3, 2, 3, 1, 2], dtype=pl.Int64)}),
             sel(x.peak_max().over("g"))),
        # --- unique_counts and value_counts -------------------------------------------------------
        case("unique_counts", SEARCH, sel(a.unique_counts())),
        case("unique_counts_text", SEARCH, sel(s.unique_counts())),
        case("unique_counts_nan_zeros", f64([1, nan, nan, negz, 0], "f"), sel(f.unique_counts())),
        case("unique_counts_by_group", SEARCH, by_g(s.unique_counts())),
        case("value_counts", SEARCH, counts(s.value_counts()), order="set", note="Polars' order is its hash table's"),
        case("value_counts_sorted", SEARCH, counts(s.value_counts(sort=True)), order="set", note="ties' order unspecified"),
        case("value_counts_ties", pl.DataFrame({"s": ["b", "a", "c", "a", "c"]}), counts(s.value_counts(sort=True)), order="set"),
        case("value_counts_shares_named", SEARCH, counts(s.value_counts(sort=True, normalize=True, name="n")), order="set"),
        case("value_counts_by_group_sorted", SEARCH, group_counts(s.value_counts(sort=True)), order="set"),
        case("value_counts_group_sorted", BA, group_counts(s.value_counts(sort=True)), order="set"),
        case("value_counts_group_shares", BA, group_counts(s.value_counts(sort=True, normalize=True)), order="set"),
        # --- generated -----------------------------------------------------------------------------
        case("search_sorted_gen", sorted_a, sel(*[a.search_sorted(v, side).alias(f"{side}_{v}".replace("-", "m")) for v in (-50, -7, 0, 13, 50, 99)
                                                for side in ("left", "right")], a.search_sorted(None, "left").alias("null_left"),
                                              a.search_sorted(None, "right").alias("null_right"), a.index_of(13).alias("idx13"),
                                              a.index_of(None).alias("idx_null"), a.index_of(99).alias("idx99")),
             note="a sorted column, nulls first"),
        case("peaks_gen", gen_peaks, PEAKS),
        case("unique_counts_gen", gen_counts, sel(s.unique_counts())),
        case("value_counts_gen", gen_counts, counts(a.value_counts()), order="set"),
        case("value_counts_by_group_gen", gen_counts, group_counts(s.value_counts(sort=True)), order="set"),
    ]
