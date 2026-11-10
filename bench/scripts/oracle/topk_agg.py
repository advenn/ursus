"""The topk_agg group: TopK and BottomK inside a group-by (topk_agg_test.go, step
106). Polars leaves a top_k's order open, so each list is sorted on Polars' side as
ursus orders it, the largest first for top_k and the smallest for bottom_k.

Polars 2.1 keeps nulls in a top_k as the least values, [7, null] of [7, null] and
[null, null] of three nulls, where 1.44.1 skipped them, as ursus does (TopK's doc: a
shorter list, null when none): the three cases over the column with nulls are
findings, skipped; the generated case has none.

Not here: the refusals and the rank-window differential over 20,000 rows.
"""

from __future__ import annotations

from datetime import date

import polars as pl

from . import Case, case
from .gen import Gen

nan = float("nan")
c = pl.col


def day(d: int) -> date:
    return date(2024, 1, d)


TK = pl.DataFrame({
    "g": ["a", "a", "a", "a", "b", "b", "c", "c"],
    "i": pl.Series([5, 9, 1, None, 7, None, None, None], dtype=pl.Int64),
    "f": pl.Series([1.5, nan, -2, 3, 0.5, 0.25, 1, 2], dtype=pl.Float64),
    "s": ["pear", "fig", "apple", "kiwi", "plum", "date", "lime", "yuzu"],
    "d": pl.Series([day(3), day(1), day(9), day(4), day(2), day(8), day(5), day(6)], dtype=pl.Date),
}).with_columns(c("i").cast(pl.Decimal(10, 2)).alias("dec"))


def top(col: str, k: int):
    return lambda df: df.group_by("g").agg(c(col).top_k(k).sort(descending=True, nulls_last=True).alias("top")).sort("g")


def bottom(col: str, k: int):
    return lambda df: df.group_by("g").agg(c(col).bottom_k(k).sort(nulls_last=True).alias("top")).sort("g")


def cases() -> list[Case]:
    gen = Gen(5019)
    n = 160
    gen_tk = gen.frame(gen.ints(n, pl.Int64, lo=0, hi=4, null_rate=0, edges=False, name="g"), gen.floats(n, lo=-100, hi=100, null_rate=0, ties=True, name="f"),
                       gen.strings(n, null_rate=0, edges=False, alphabet="abcxyz", maxlen=3, name="s"),
                       gen.ints(n, pl.Int64, lo=-30, hi=30, null_rate=0, name="i"))
    return [
        case("top_k_ints", TK, top("i", 2), note="Polars 2.1 keeps nulls as the least: [7, null] and [null, null]; skipped here, a finding"),
        case("bottom_k_ints", TK, bottom("i", 2), note="as top_k_ints: a finding"),
        case("top_k_floats_nan", TK, top("f", 2), note="NaN is the largest float"),
        case("top_k_strings", TK, top("s", 3)),
        case("bottom_k_dates", TK, bottom("d", 1)),
        case("top_k_decimals", TK, top("dec", 5), note="as top_k_ints: a finding"),
        # --- generated -----------------------------------------------------------------------------
        case("top_k_gen", gen_tk, lambda df: df.group_by("g").agg(
            c("f").top_k(3).sort(descending=True).alias("f3"), c("f").bottom_k(2).sort().alias("f_bottom2"),
            c("s").top_k(2).sort(descending=True).alias("s2"), c("s").bottom_k(3).sort().alias("s_bottom3"),
            c("i").top_k(4).sort(descending=True).alias("i4"), c("i").bottom_k(1).sort().alias("i_bottom1")).sort("g"),
             note="no nulls; each list sorted as ursus orders it"),
    ]
