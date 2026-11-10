"""The join_byhand group: at what type a join key is compared (join_byhand_test.go,
audit.md §4). A key meets the other side's at a type that holds both exactly, and is
cast there strictly; Polars 2.1 refuses keys of two widths in an as-of join's `by`, an
Int32 against a Float32 and a Datetime(ms) against a Datetime(ns), so those cases ask
Polars with the narrow key cast to the type the two meet at here, which is the rule
under test. Not here: the refusals, and the spilled right join, which stays a Go
differential against the same join in memory.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
P53 = 1 << 53


def cases() -> list[Case]:
    g = Gen(5011)
    n = 120
    left_gen = g.frame(g.ints(n, pl.Int32, lo=-40, hi=40, edges=False, name="k").alias("k"),
                       pl.Series("lv", range(n), dtype=pl.Int64))
    right_gen = g.frame(g.ints(150, pl.Int64, lo=-40, hi=40, edges=False, name="k"),
                        pl.Series("rv", range(150), dtype=pl.Int64))
    # An as-of join wants each side sorted on its key; the by-key is narrow on one side.
    la = g.frame(g.ints(n, pl.Int64, lo=0, hi=5000, null_rate=0, edges=False, name="k"),
                 g.ints(n, pl.Int32, lo=0, hi=3, null_rate=0.1, edges=False, name="g")).sort("k").with_row_index("lv")
    ra = g.frame(g.ints(80, pl.Int64, lo=0, hi=5000, null_rate=0, edges=False, name="k"),
                 g.ints(80, pl.Int64, lo=0, hi=3, null_rate=0.1, edges=False, name="g")).sort("k").with_row_index("rv")
    la = la.with_columns(c("lv").cast(pl.Int64))
    ra = ra.with_columns(c("rv").cast(pl.Int64))
    i32, i64 = pl.Int32, pl.Int64
    return [
        case("asof_by_int32_against_int64",
             {"left": pl.DataFrame({"k": [10, 20], "g": pl.Series([1, 2], dtype=i32), "lv": [0, 1]}),
              "right": pl.DataFrame({"k": [5, 15], "g": pl.Series([1, 2], dtype=i64), "rv": [100, 200]})},
             lambda left, right: left.with_columns(c("g").cast(i64)).join_asof(right, on="k", by="g").select("lv", "rv"),
             note="Polars refuses by-keys of two widths; the Int32 is cast to the Int64 the two meet at here"),
        case("asof_by_int64_against_int32",
             {"left": pl.DataFrame({"k": [10, 20], "g": pl.Series([1, 2], dtype=i64), "lv": [0, 1]}),
              "right": pl.DataFrame({"k": [5, 15], "g": pl.Series([1, 2], dtype=i32), "rv": [100, 200]})},
             lambda left, right: left.join_asof(right.with_columns(c("g").cast(i64)), on="k", by="g").select("lv", "rv"),
             note="the narrow by-key on the right"),
        case("right_join_null_key",
             {"left": pl.DataFrame({"k1": [5], "k2": [5], "lv": [50]}),
              "right": pl.DataFrame({"k1": [1, 5, 7], "k2": [None, 5, 7], "rv": [-1, 5, 7]})},
             lambda left, right: left.join(right, on=["k1", "k2"], how="right").
             filter((c("rv") == -1) | (c("lv") == -12345)).select("k1", "k2", "rv"), order="set",
             note="a right join keeps the right key it coalesces, a null among its parts"),
        case("validate_1m_null_left_keys",
             {"left": pl.DataFrame({"k": pl.Series([None, None, 2], dtype=i64)}), "right": pl.DataFrame({"k": [2]})},
             lambda left, right: left.join(right, on="k", validate="1:m"), order="set",
             note="duplicate null left keys are not duplicates where nulls are not equal"),
        case("asof_by_null_matches_nothing",
             {"left": pl.DataFrame({"k": [10], "g": pl.Series([None], dtype=i64)}),
              "right": pl.DataFrame({"k": [5], "g": pl.Series([None], dtype=i64), "rv": [100]})},
             lambda left, right: left.join_asof(right, on="k", by="g").select("k", "rv")),
        case("int32_against_float32_exact",
             {"left": pl.DataFrame({"k": pl.Series([16777217, 3], dtype=i32)}),
              "right": pl.DataFrame({"k": pl.Series([16777216, 3], dtype=pl.Float32)})},
             lambda left, right: left.with_columns(c("k").cast(pl.Float64)).join(
                 right.with_columns(c("k").cast(pl.Float64)), on="k").select(c("k").cast(i64)), order="set",
             note="Polars refuses an Int32 against a Float32; both cast to the Float64 that holds both exactly, the rule here"),
        case("int64_against_uint64_at_int128",
             {"left": pl.DataFrame({"k": pl.Series([-1, (1 << 62) + 1], dtype=i64)}),
              "right": pl.DataFrame({"k": pl.Series([(1 << 64) - 1, (1 << 62) + 1], dtype=pl.UInt64)})},
             lambda left, right: left.join(right, on="k").select(c("k").cast(i64)), order="set"),
        case("datetime_ms_against_ns_in_range",
             {"left": pl.DataFrame({"k": pl.Series([1000, 2000]).cast(pl.Datetime("ms")), "lv": [0, 1]}),
              "right": pl.DataFrame({"k": pl.Series([2_000_000_000]).cast(pl.Datetime("ns")), "rv": [9]})},
             lambda left, right: left.with_columns(c("k").cast(pl.Datetime("ns"))).join(right, on="k").select("lv", "rv"),
             order="set", note="Polars refuses two units; the coarser cast to the finer, where every value fits"),
        case("filter_on_a_coalesced_key_of_two_widths",
             {"left": pl.DataFrame({"k": pl.Series([50000], dtype=i32), "lv": [1]}),
              "right": pl.DataFrame({"k": pl.Series([50000], dtype=i64), "rv": [2]})},
             lambda left, right: left.join(right, on="k").with_columns(c("k").cast(i64)).
             filter(c("k") * c("k") > 2_000_000_000).select("lv", "rv"), order="set",
             note="Polars' coalesced key keeps the left's Int32 and 50000*50000 wraps negative there, losing the row; "
                  "here the key is the Int64 both fit, so it is cast first"),
        case("join_widths_gen", {"left": left_gen, "right": right_gen},
             lambda left, right: left.join(right, on="k").select(c("k").cast(i64), "lv", "rv"), order="set",
             note="an Int32 key against an Int64 one, with nulls and repeats on both sides"),
        case("asof_by_widths_gen", {"left": la, "right": ra},
             lambda left, right: left.with_columns(c("g").cast(i64)).join_asof(right, on="k", by="g").select("lv", "rv"),
             note="the Int32 by-key cast to the Int64 the two meet at here; null by-keys match nothing"),
    ]
