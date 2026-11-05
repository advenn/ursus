"""The expressions group: 0.5's expressions against Polars (step 184's R4), the
oracle's first group, its 46 cases as they were plus Describe and ValueCounts as
frames. Each case is a small frame and one expression over it, aliased r.

Columns are typed by the series' dtype: an integer's width is the bit counts' answer.
"""

from __future__ import annotations

from datetime import date, datetime, timedelta

import polars as pl

from . import Case, case

nan = float("nan")
F = [1.0, 2.0, None, 4.0, 5.0, 6.0, 7.0, None, 9.0, 10.0]
E = [1.0, 3.0, None, 2.0, 8.0, 5.0, None, None, 4.0, 6.0]
CUTS = [-1.0, 0.0, 0.5, 1.0, 2.0, 3.0, None, 10.0, 0.25, 1.0]
INTS = [0, 1, -1, 255, 1 << 40, None, -(1 << 62), 7]
STRS_A = ["a", None, "c", "dd", ""]
STRS_B = ["x", "y", None, "zz", "w"]
INT_B = [1, 2, 3, None, 5]


def instants(us: list[int | None]) -> list[datetime | None]:
    return [None if u is None else datetime(1970, 1, 1) + timedelta(microseconds=u) for u in us]


H = 3_600_000_000
D = 24 * H
TS = [0, 13 * H + 7 * 60_000_000, 31 * D, 59 * D + 23 * H, 365 * D + 12 * H,
      (54 * 365 + 13) * D + 5 * H, (54 * 365 + 59) * D + 18 * H + 30 * 60_000_000]
DATES = [date(2024, 1, 31), date(2024, 2, 29), date(2023, 12, 31), None, date(1969, 7, 20), date(2000, 3, 1)]


def frame(cols: dict[str, tuple[list, pl.DataType]]) -> pl.DataFrame:
    return pl.DataFrame({c: pl.Series(c, v, dtype=t) for c, (v, t) in cols.items()})


X = {"x": (F, pl.Float64)}
XE = {"x": (E, pl.Float64)}
XC = {"x": (CUTS, pl.Float64)}
XT = {"x": (F[:7], pl.Float64), "t": (instants(TS), pl.Datetime("us"))}
I = {"i": (INTS, pl.Int64)}
DD = {"d": (DATES, pl.Date)}
T = {"t": (instants(TS), pl.Datetime("us"))}
TU = {"t": (instants(TS), pl.Datetime("us", "UTC"))}

# name, columns, the expression. The name is what oracle_expressions_test.go spells.
CASES = [
    ("rolling_sum_3", X, pl.col("x").rolling_sum(3)),
    ("rolling_mean_3_min1", X, pl.col("x").rolling_mean(3, min_samples=1)),
    ("rolling_min_4_min2", X, pl.col("x").rolling_min(4, min_samples=2)),
    ("rolling_max_2", X, pl.col("x").rolling_max(2)),
    ("rolling_var_3_min2", X, pl.col("x").rolling_var(3, min_samples=2)),
    ("rolling_std_4_min1_ddof0", X, pl.col("x").rolling_std(4, min_samples=1, ddof=0)),
    ("rolling_sum_3_center", X, pl.col("x").rolling_sum(3, center=True)),
    ("rolling_mean_4_center_min1", X, pl.col("x").rolling_mean(4, center=True, min_samples=1)),
    ("rolling_sum_by_1d", XT, pl.col("x").rolling_sum_by("t", "1d")),
    ("rolling_max_by_40d", XT, pl.col("x").rolling_max_by("t", "40d")),
    ("ewm_mean_alpha05", XE, pl.col("x").ewm_mean(alpha=0.5)),
    ("ewm_mean_span3_noadjust", XE, pl.col("x").ewm_mean(span=3, adjust=False)),
    ("ewm_mean_com2_ignore_nulls", XE, pl.col("x").ewm_mean(com=2, ignore_nulls=True)),
    ("ewm_mean_alpha05_min3", XE, pl.col("x").ewm_mean(alpha=0.5, min_samples=3)),
    ("ewm_std_halflife2", XE, pl.col("x").ewm_std(half_life=2)),
    ("ewm_var_alpha03_bias", XE, pl.col("x").ewm_var(alpha=0.3, bias=True)),
    ("ewm_var_span4_noadjust", XE, pl.col("x").ewm_var(span=4, adjust=False)),
    ("cut_0_1", XC, pl.col("x").cut([0, 1]).cast(pl.String)),
    ("cut_labels", XC, pl.col("x").cut([0, 1], labels=["lo", "mid", "hi"]).cast(pl.String)),
    ("cut_left_closed", XC, pl.col("x").cut([0, 1], left_closed=True).cast(pl.String)),
    ("qcut_quartiles", XC, pl.col("x").qcut([0.25, 0.5, 0.75]).cast(pl.String)),
    ("qcut_3", XC, pl.col("x").qcut(3).cast(pl.String)),
    ("concat_str_dash", {"a": (STRS_A, pl.String), "b": (STRS_B, pl.String)}, pl.concat_str(["a", "b"], separator="-")),
    ("concat_str_int", {"a": (STRS_A, pl.String), "i": (INT_B, pl.Int64)}, pl.concat_str([pl.col("a"), pl.col("i")])),
    ("count_ones", I, pl.col("i").bitwise_count_ones()),
    ("count_zeros", I, pl.col("i").bitwise_count_zeros()),
    ("leading_zeros", I, pl.col("i").bitwise_leading_zeros()),
    ("leading_ones", I, pl.col("i").bitwise_leading_ones()),
    ("trailing_zeros", I, pl.col("i").bitwise_trailing_zeros()),
    ("trailing_ones", I, pl.col("i").bitwise_trailing_ones()),
    ("count_ones_int8", {"i": ([0, 1, -1, 127, -128, None], pl.Int8)}, pl.col("i").bitwise_count_ones()),
    ("offset_by_1mo_dates", DD, pl.col("d").dt.offset_by("1mo")),
    ("offset_by_minus1y_dates", DD, pl.col("d").dt.offset_by("-1y")),
    ("offset_by_1d12h", T, pl.col("t").dt.offset_by("1d12h")),
    ("truncate_1w", T, pl.col("t").dt.truncate("1w")),
    ("truncate_1mo", T, pl.col("t").dt.truncate("1mo")),
    ("truncate_1q_dates", DD, pl.col("d").dt.truncate("1q")),
    ("round_1h", T, pl.col("t").dt.round("1h")),
    ("round_1d", T, pl.col("t").dt.round("1d")),
    ("month_start", T, pl.col("t").dt.month_start()),
    ("month_end_dates", DD, pl.col("d").dt.month_end()),
    ("is_leap_year_dates", DD, pl.col("d").dt.is_leap_year()),
    ("convert_tokyo_hour", TU, pl.col("t").dt.convert_time_zone("Asia/Tokyo").dt.hour()),
    ("truncate_1d_london", TU,
     pl.col("t").dt.convert_time_zone("Europe/London").dt.truncate("1d").dt.convert_time_zone("UTC")),
    ("replace_zone_new_york", T,
     pl.col("t").dt.replace_time_zone("America/New_York", ambiguous="earliest", non_existent="null")
     .dt.convert_time_zone("UTC")),
    ("strftime", T, pl.col("t").dt.strftime("%Y-%m-%d %H:%M %a %j")),
]

# Describe's and ValueCounts' frame.
MIXED = pl.DataFrame({
    "f": pl.Series([1.5, None, 3.0, -2.0, 10.0, 4.5], dtype=pl.Float64),
    "i": pl.Series([3, 1, 4, 1, 5, None], dtype=pl.Int64),
    "s": pl.Series(["b", "a", None, "c", "a", "b"], dtype=pl.String),
    "b": pl.Series([True, False, None, True, True, False], dtype=pl.Boolean),
})


# A variance's merge order leaves last bits apart (feature-gaps.md §1): Polars 2.1.0's
# ewm_std and ewm_var moved by an ulp from 1.44.1's in two cases, and ursus's rolling
# and ewm variances are an ulp or two from Polars' in four more. These are held to
# rel:1e-12; every other case is compared by its bits (step 496).
LAST_BITS = {"rolling_var_3_min2", "rolling_std_4_min1_ddof0", "ewm_mean_alpha05", "ewm_var_alpha03_bias",
             "ewm_std_halflife2", "ewm_var_span4_noadjust"}


def cases() -> list[Case]:
    out = [case(name, frame(cols), (lambda e: lambda df: df.select(e.alias("r")))(e),
                **({"float": "rel:1e-12", "note": "a variance's merge order: last bits apart"}
                   if name in LAST_BITS else {}))
           for name, cols, e in CASES]
    out.append(case("describe", MIXED, lambda df: df.describe(), float="rel:1e-12",
                    note="the statistics as Polars' describe gives them, every number a Float64; the std's last bits"))
    out.append(case("value_counts", MIXED, lambda df: df.get_column("s").value_counts(sort=True), order="set",
                    note="Polars leaves ties' order to its threads"))
    return out
