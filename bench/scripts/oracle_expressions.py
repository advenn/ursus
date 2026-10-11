"""Write testdata/oracle/expressions.json: Polars' answers for 0.5's expressions.

Scope item R4 of context_files/v0.6-scope.md (step 184). Each case is a small frame
and one Polars expression over it; the JSON holds the frame and Polars' answer, and
oracle_test.go in the repository root asks ursus the same question, by the case's
name, and compares. The expected answers are therefore Polars', produced once here
and checked in, not ursus's own reading of what Polars would say.

    cd bench && uv run --offline python scripts/oracle_expressions.py

Columns are written as lists of JSON values with their type: floats, integers,
strings and booleans as themselves, and instants as integer ticks of their unit, so
nothing is lost to a text format. A null is null. An integer's kind is its Polars
type, Int8 or UInt32, because a bit count's answer depends on the width.
"""

from __future__ import annotations

import json
import math
import pathlib
from datetime import date, datetime, timedelta

import polars as pl

OUT = pathlib.Path(__file__).resolve().parents[2] / "testdata" / "oracle" / "expressions.json"

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
TS = [
    v * 1 for v in [
        0, 13 * H + 7 * 60_000_000, 31 * D, 59 * D + 23 * H, 365 * D + 12 * H,
        (54 * 365 + 13) * D + 5 * H, (54 * 365 + 59) * D + 18 * H + 30 * 60_000_000,
    ]
]
DATES = [date(2024, 1, 31), date(2024, 2, 29), date(2023, 12, 31), None, date(1969, 7, 20), date(2000, 3, 1)]

# Each case: name, columns {name: (values, polars dtype)}, and the expression. The name
# is what oracle_test.go maps to ursus's own spelling.
CASES = [
    # --- rolling -------------------------------------------------------------------
    ("rolling_sum_3", {"x": (F, pl.Float64)}, pl.col("x").rolling_sum(3)),
    ("rolling_mean_3_min1", {"x": (F, pl.Float64)}, pl.col("x").rolling_mean(3, min_samples=1)),
    ("rolling_min_4_min2", {"x": (F, pl.Float64)}, pl.col("x").rolling_min(4, min_samples=2)),
    ("rolling_max_2", {"x": (F, pl.Float64)}, pl.col("x").rolling_max(2)),
    ("rolling_var_3_min2", {"x": (F, pl.Float64)}, pl.col("x").rolling_var(3, min_samples=2)),
    ("rolling_std_4_min1_ddof0", {"x": (F, pl.Float64)}, pl.col("x").rolling_std(4, min_samples=1, ddof=0)),
    ("rolling_sum_3_center", {"x": (F, pl.Float64)}, pl.col("x").rolling_sum(3, center=True)),
    ("rolling_mean_4_center_min1", {"x": (F, pl.Float64)}, pl.col("x").rolling_mean(4, center=True, min_samples=1)),
    ("rolling_sum_by_1d", {"x": (F[:7], pl.Float64), "t": (instants(TS), pl.Datetime("us"))},
     pl.col("x").rolling_sum_by("t", "1d")),
    ("rolling_max_by_40d", {"x": (F[:7], pl.Float64), "t": (instants(TS), pl.Datetime("us"))},
     pl.col("x").rolling_max_by("t", "40d")),
    # --- ewm -----------------------------------------------------------------------
    ("ewm_mean_alpha05", {"x": (E, pl.Float64)}, pl.col("x").ewm_mean(alpha=0.5)),
    ("ewm_mean_span3_noadjust", {"x": (E, pl.Float64)}, pl.col("x").ewm_mean(span=3, adjust=False)),
    ("ewm_mean_com2_ignore_nulls", {"x": (E, pl.Float64)}, pl.col("x").ewm_mean(com=2, ignore_nulls=True)),
    ("ewm_mean_alpha05_min3", {"x": (E, pl.Float64)}, pl.col("x").ewm_mean(alpha=0.5, min_samples=3)),
    ("ewm_std_halflife2", {"x": (E, pl.Float64)}, pl.col("x").ewm_std(half_life=2)),
    ("ewm_var_alpha03_bias", {"x": (E, pl.Float64)}, pl.col("x").ewm_var(alpha=0.3, bias=True)),
    ("ewm_var_span4_noadjust", {"x": (E, pl.Float64)}, pl.col("x").ewm_var(span=4, adjust=False)),
    # --- cut and qcut --------------------------------------------------------------
    ("cut_0_1", {"x": (CUTS, pl.Float64)}, pl.col("x").cut([0, 1]).cast(pl.String)),
    ("cut_labels", {"x": (CUTS, pl.Float64)}, pl.col("x").cut([0, 1], labels=["lo", "mid", "hi"]).cast(pl.String)),
    ("cut_left_closed", {"x": (CUTS, pl.Float64)}, pl.col("x").cut([0, 1], left_closed=True).cast(pl.String)),
    ("qcut_quartiles", {"x": (CUTS, pl.Float64)}, pl.col("x").qcut([0.25, 0.5, 0.75]).cast(pl.String)),
    ("qcut_3", {"x": (CUTS, pl.Float64)}, pl.col("x").qcut(3).cast(pl.String)),
    # --- concat_str ----------------------------------------------------------------
    ("concat_str_dash", {"a": (STRS_A, pl.String), "b": (STRS_B, pl.String)},
     pl.concat_str(["a", "b"], separator="-")),
    ("concat_str_int", {"a": (STRS_A, pl.String), "i": (INT_B, pl.Int64)},
     pl.concat_str([pl.col("a"), pl.col("i")])),
    # --- bit counts ----------------------------------------------------------------
    ("count_ones", {"i": (INTS, pl.Int64)}, pl.col("i").bitwise_count_ones()),
    ("count_zeros", {"i": (INTS, pl.Int64)}, pl.col("i").bitwise_count_zeros()),
    ("leading_zeros", {"i": (INTS, pl.Int64)}, pl.col("i").bitwise_leading_zeros()),
    ("leading_ones", {"i": (INTS, pl.Int64)}, pl.col("i").bitwise_leading_ones()),
    ("trailing_zeros", {"i": (INTS, pl.Int64)}, pl.col("i").bitwise_trailing_zeros()),
    ("trailing_ones", {"i": (INTS, pl.Int64)}, pl.col("i").bitwise_trailing_ones()),
    ("count_ones_int8", {"i": ([0, 1, -1, 127, -128, None], pl.Int8)}, pl.col("i").bitwise_count_ones()),
    # --- the calendar --------------------------------------------------------------
    ("offset_by_1mo_dates", {"d": (DATES, pl.Date)}, pl.col("d").dt.offset_by("1mo")),
    ("offset_by_minus1y_dates", {"d": (DATES, pl.Date)}, pl.col("d").dt.offset_by("-1y")),
    ("offset_by_1d12h", {"t": (instants(TS), pl.Datetime("us"))}, pl.col("t").dt.offset_by("1d12h")),
    ("truncate_1w", {"t": (instants(TS), pl.Datetime("us"))}, pl.col("t").dt.truncate("1w")),
    ("truncate_1mo", {"t": (instants(TS), pl.Datetime("us"))}, pl.col("t").dt.truncate("1mo")),
    ("truncate_1q_dates", {"d": (DATES, pl.Date)}, pl.col("d").dt.truncate("1q")),
    ("round_1h", {"t": (instants(TS), pl.Datetime("us"))}, pl.col("t").dt.round("1h")),
    ("round_1d", {"t": (instants(TS), pl.Datetime("us"))}, pl.col("t").dt.round("1d")),
    ("month_start", {"t": (instants(TS), pl.Datetime("us"))}, pl.col("t").dt.month_start()),
    ("month_end_dates", {"d": (DATES, pl.Date)}, pl.col("d").dt.month_end()),
    ("is_leap_year_dates", {"d": (DATES, pl.Date)}, pl.col("d").dt.is_leap_year()),
    ("convert_tokyo_hour", {"t": (instants(TS), pl.Datetime("us", "UTC"))},
     pl.col("t").dt.convert_time_zone("Asia/Tokyo").dt.hour()),
    ("truncate_1d_london", {"t": (instants(TS), pl.Datetime("us", "UTC"))},
     pl.col("t").dt.convert_time_zone("Europe/London").dt.truncate("1d").dt.convert_time_zone("UTC")),
    ("replace_zone_new_york", {"t": (instants(TS), pl.Datetime("us"))},
     pl.col("t").dt.replace_time_zone("America/New_York", ambiguous="earliest", non_existent="null")
     .dt.convert_time_zone("UTC")),
    ("strftime", {"t": (instants(TS), pl.Datetime("us"))}, pl.col("t").dt.strftime("%Y-%m-%d %H:%M %a %j")),
]


def kind(dt: pl.DataType) -> str:
    if dt == pl.Float64 or dt == pl.Float32:
        return "f64"
    if dt == pl.Boolean:
        return "bool"
    if dt == pl.String:
        return "str"
    if dt == pl.Date:
        return "date"
    if isinstance(dt, pl.Datetime):
        return "datetime_us" + ("" if dt.time_zone is None else ":" + dt.time_zone)
    if dt.is_integer():
        return str(dt)  # Int8, UInt32, …: the width is the answer's, for the bit counts
    raise TypeError(f"no JSON kind for {dt}")


def encode(s: pl.Series) -> list:
    k = kind(s.dtype)
    if k == "date":
        return [None if v is None else v for v in s.cast(pl.Int32).to_list()]
    if k.startswith("datetime_us"):
        return s.dt.cast_time_unit("us").cast(pl.Int64).to_list()
    if k == "f64":
        return [None if v is None else ("NaN" if math.isnan(v) else v) for v in s.to_list()]
    return s.to_list()


def main() -> None:
    cases = []
    for name, cols, e in CASES:
        frame = pl.DataFrame({c: pl.Series(c, v, dtype=t) for c, (v, t) in cols.items()})
        got = frame.select(e.alias("r")).to_series()
        cases.append({
            "name": name,
            "columns": [{"name": c, "kind": kind(frame[c].dtype), "values": encode(frame[c])} for c in cols],
            "want": {"kind": kind(got.dtype), "values": encode(got)},
        })

    # Describe and ValueCounts answer frames; each is kept as its rows.
    df = pl.DataFrame({
        "f": pl.Series([1.5, None, 3.0, -2.0, 10.0, 4.5], dtype=pl.Float64),
        "i": pl.Series([3, 1, 4, 1, 5, None], dtype=pl.Int64),
        "s": pl.Series(["b", "a", None, "c", "a", "b"], dtype=pl.String),
        "b": pl.Series([True, False, None, True, True, False], dtype=pl.Boolean),
    })
    desc = df.describe()
    frames = {
        "describe": {
            "columns": [{"name": c, "kind": kind(df[c].dtype), "values": encode(df[c])} for c in df.columns],
            "statistics": desc["statistic"].to_list(),
            "answers": {c: [None if v is None else str(v) for v in desc[c].to_list()] for c in df.columns},
        },
    }
    vc = df.select("s").to_series().value_counts(sort=True)
    frames["value_counts"] = {
        "columns": [{"name": "s", "kind": "str", "values": encode(df["s"])}],
        "pairs": [[v, n] for v, n in vc.rows()],
    }
    OUT.parent.mkdir(parents=True, exist_ok=True)
    OUT.write_text(json.dumps({"polars": pl.__version__, "cases": cases, "frames": frames}, indent=1) + "\n")
    print(f"wrote {len(cases)} cases and {len(frames)} frames to {OUT}")


if __name__ == "__main__":
    main()
