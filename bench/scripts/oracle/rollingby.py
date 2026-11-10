"""The rollingby group: rolling windows by time (rollingby_test.go, step 177).

A window by time holding no value is null here and 0.0 in Polars for a sum
(feature-gaps.md §9; the expressions group's rolling_sum_by_1d): closed_left and
closed_none are in oracleDifferences, and the generated case with nulls asks no sum.
A rolling variance is held to rel:1e-12 (a sliding window's merge order).

Not here: the refusals, the plan, and the brute force over 400 rows.
"""

from __future__ import annotations

from datetime import date, datetime

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
v, t = c("v"), c("t")
HOUR_US = 3_600_000_000
T0 = 19723 * 86_400_000_000  # 2024-01-01 in microseconds


def hours(hs: list[int], vals: list, ok: bool = True) -> pl.DataFrame:
    return pl.DataFrame({"t": pl.Series([None if h is None else T0 + h * HOUR_US for h in hs], dtype=pl.Int64).cast(pl.Datetime("us")),
                         "v": pl.Series(vals, dtype=pl.Float64)})


HOURS = hours([0, 1, 2, 4, 5, 9], [1, 2, None, 4, 5, 6])
VAR = dict(float="rel:1e-12", note="a rolling variance's merge order")


def sel(*exprs: pl.Expr):
    return lambda df: df.select(*exprs)


def cases() -> list[Case]:
    gen = Gen(5013)
    n = 200
    mins = sorted(int(m) for m in gen.rng.integers(0, 600, n))
    ts = pl.Series("t", [T0 + m * 60_000_000 for m in mins], dtype=pl.Int64).cast(pl.Datetime("us"))
    gen_full = pl.DataFrame({"t": ts, "v": gen.floats(n, lo=-100, hi=100, null_rate=0, edges=False, name="v"),
                             "g": gen.ints(n, pl.Int64, lo=0, hi=2, null_rate=0, edges=False, name="g")})
    gen_nulls = pl.DataFrame({"t": ts, "v": gen.floats(n, lo=-100, hi=100, null_rate=0.2, edges=False, name="v")})
    return [
        case("sum_2h", HOURS, sel(v.rolling_sum_by("t", "2h"))),
        case("mean_2h", HOURS, sel(v.rolling_mean_by("t", "2h"))),
        case("min_2h", HOURS, sel(v.rolling_min_by("t", "2h"))),
        case("max_2h", HOURS, sel(v.rolling_max_by("t", "2h"))),
        case("var_2h", HOURS, sel(v.rolling_var_by("t", "2h")), **VAR),
        case("std_3h", HOURS, sel(v.rolling_std_by("t", "3h")), **VAR),
        case("sum_2h_min_samples_2", HOURS, sel(v.rolling_sum_by("t", "2h", min_samples=2))),
        case("sum_2h_closed_right", HOURS, sel(v.rolling_sum_by("t", "2h", closed="right"))),
        case("sum_2h_closed_left", HOURS, sel(v.rolling_sum_by("t", "2h", closed="left")),
             note="Polars answers an empty window 0 under min_samples=1; null here (§9): oracleDifferences"),
        case("sum_2h_closed_both", HOURS, sel(v.rolling_sum_by("t", "2h", closed="both"))),
        case("sum_2h_closed_none", HOURS, sel(v.rolling_sum_by("t", "2h", closed="none")), note="empty windows 0 in Polars, null here: oracleDifferences"),
        case("mean_2h_ints", hours([0, 1, 2, 4, 5, 9], [1, 2, 3, 4, 5, 6]).with_columns(v.cast(pl.Int64)), sel(v.rolling_mean_by("t", "2h")),
             note="integers: an exact sum, a float mean"),
        case("sum_2h_unsorted", hours([2, 0, 1, 5], [1, 2, 3, 4]), sel(v.rolling_sum_by("t", "2h"))),
        case("sum_1h_ties", hours([0, 0, 1], [1, 2, 3]), sel(v.rolling_sum_by("t", "1h")), note="ties share a window, the later rows too"),
        case("sum_2h_null_by", hours([0, None, 1], [1, 2, 3]), sel(v.rolling_sum_by("t", "2h")), note="a null by is in no window"),
        case("sum_2h_over_g", pl.DataFrame({"g": ["a", "a", "b", "b"], "t": pl.Series([T0, T0 + HOUR_US, T0, T0 + HOUR_US], dtype=pl.Int64).cast(pl.Datetime("us")),
                                            "v": pl.Series([1.0, 2, 10, 20], dtype=pl.Float64)}), sel(v.rolling_sum_by("t", "2h").over("g"))),
        case("sum_dates_2d", pl.DataFrame({"t": [date(2024, 1, 1), date(2024, 1, 2), date(2024, 1, 3), date(2024, 1, 5)], "v": [1.0, 2, 3, 4]}),
             sel(v.rolling_sum_by("t", "2d"))),
        case("sum_dates_1mo", pl.DataFrame({"t": [date(2024, 1, 1), date(2024, 1, 2), date(2024, 1, 3), date(2024, 1, 5)], "v": [1.0, 2, 3, 4]}),
             sel(v.rolling_sum_by("t", "1mo"))),
        case("sum_london_1d", pl.DataFrame({"t": pl.Series([datetime(2024, 3, 30, 12), datetime(2024, 3, 31, 12)], dtype=pl.Datetime("us"))
                                            .dt.replace_time_zone("Europe/London"), "v": [1.0, 2]}),
             sel(v.rolling_sum_by("t", "1d")), note="a calendar day across London's change of offset is not twenty-four hours"),
        case("sum_london_24h", pl.DataFrame({"t": pl.Series([datetime(2024, 3, 30, 12), datetime(2024, 3, 31, 12)], dtype=pl.Datetime("us"))
                                             .dt.replace_time_zone("Europe/London"), "v": [1.0, 2]}),
             sel(v.rolling_sum_by("t", "24h"))),
        case("names_three", hours([0, 1, 2], [1, 2, 3]), sel(v.rolling_sum_by("t", "1h").alias("one"), v.rolling_sum_by("t", "2h").alias("two"),
                                                            v.rolling_sum_by("t", "2h", closed="both").alias("both"), v.rolling_max_by("t", "1h"))),
        # --- generated ---------------------------------------------------------------------------
        case("full_gen", gen_full, sel(v.rolling_sum_by("t", "17m").alias("sum17"), v.rolling_mean_by("t", "17m").alias("mean17"),
                                       v.rolling_min_by("t", "1h").alias("min1h"), v.rolling_max_by("t", "1h").alias("max1h"),
                                       v.rolling_sum_by("t", "1h", closed="both").alias("sum1h_both"),
                                       v.rolling_sum_by("t", "45m", min_samples=3).over("g").alias("sum45g3"),
                                       v.rolling_mean_by("t", "2h").over("g").alias("mean2hg")),
             float="rel:1e-12", note="no null values: every window under closed right or both holds its own row; a sliding sum's residual is an ulp"),
        case("var_gen", gen_full, sel(v.rolling_var_by("t", "1h").alias("var1h"), v.rolling_std_by("t", "2h", min_samples=4).alias("std2h4"),
                                      v.rolling_var_by("t", "1h").over("g").alias("var1hg")), **VAR),
        case("nulls_gen", gen_nulls, sel(v.rolling_mean_by("t", "30m").alias("mean30"), v.rolling_min_by("t", "30m").alias("min30"),
                                         v.rolling_max_by("t", "30m").alias("max30"), v.rolling_mean_by("t", "1h", closed="left").alias("mean1h_left")),
             float="rel:1e-12", note="values with nulls: a window of only nulls is null in both for a mean, a min and a max, where a sum would be 0 in Polars; "
                                 "a sliding mean's residual is an ulp"),
    ]
