"""The scalar_byhand group: audit.md §6's scalar functions, S5 to S13
(scalar_byhand_test.go): dt.epoch's floor, is_in and list.contains against a value
of another type, a regex replace's groups, floor division and the remainder, split
and splitn, truncate and group_by_dynamic on a zoned wall clock, count_matches of an
empty pattern and the strips of Unicode space. Every literal frame is the one the Go
test held by hand, and a generated case follows each function's.

Not here, with the reason: Truncate of a Go time.Duration floors the UTC instant, where
Polars' "1h" floors the wall clock (the Go test holds it to the tick arithmetic); a
Duration's FloorDiv by an integer, which Polars 2.1 refuses (a differential through
Int64 in the Go test); Datetime(s), a unit Polars has not (the ms twin asks the same of
a literal finer than the unit); the refusals, which the Go test keeps.
"""

from __future__ import annotations

from datetime import datetime, timezone

import polars as pl

from . import Case, case
from .gen import Gen

nan, inf = float("nan"), float("inf")
c = pl.col
EPOCH = datetime(1970, 1, 1, tzinfo=timezone.utc)


def us(iso: str) -> int:
    """Microseconds since the epoch of a UTC wall clock."""
    t = datetime.fromisoformat(iso).replace(tzinfo=timezone.utc)
    return round((t - EPOCH).total_seconds() * 1_000_000)


def typed(vals: list, dtype: pl.DataType, name: str = "v") -> pl.DataFrame:
    return pl.DataFrame({name: pl.Series(name, vals, dtype=dtype)})


def datetimes(ticks: list, unit: str = "us", zone: str | None = None, name: str = "ts") -> pl.DataFrame:
    s = pl.Series(name, ticks, dtype=pl.Int64).cast(pl.Datetime(unit))
    if zone:
        s = s.dt.replace_time_zone("UTC").dt.convert_time_zone(zone)
    return pl.DataFrame({name: s})


def sel(*e: pl.Expr):
    return lambda df: df.select(*e)


ts, v, s, a, b = c("ts"), c("v"), c("s"), c("a"), c("b")

I8 = typed([1, 2], pl.Int8)
LIST_I8 = typed([[1, 2]], pl.List(pl.Int8))
STRS = pl.DataFrame({"s": ["abc", "", "é"]})
SPACE = pl.DataFrame({"s": ["   x 　\v\f"]})
AB = pl.DataFrame({"s": ["ab ab"]})
DIV = pl.DataFrame({"a": pl.Series([-7, -7, 7, 7], dtype=pl.Int64), "b": pl.Series([2, -2, 2, -2], dtype=pl.Int64)})


def cases() -> list[Case]:
    g = Gen(4971)
    gen_us = g.frame(g.datetimes(120, "us", name="ts"))
    gen_ns = g.frame(g.datetimes(120, "ns", name="ts"))
    gen_ints = g.frame(g.ints(150, pl.Int64, lo=-100, hi=100, name="v"))
    gen_floats = g.frame(g.floats(150, lo=-5, hi=5, name="v"))
    gen_lists = g.frame(g.lists(120, g.ints(40, pl.Int16, lo=-3, hi=3), name="v"))
    gen_div = g.frame(g.ints(150, pl.Int64, lo=-1000, hi=1000, name="a"), g.ints(150, pl.Int64, lo=-7, hi=7, name="b"),
                      g.floats(150, lo=-100, hi=100, edges=False, name="f"), g.floats(150, lo=0.5, hi=10, edges=False, name="d"))
    # The float dividend stays small: Polars' remainder is a - b*floor(a/b), 0 or garbage
    # once the quotient's ulp passes the divisor, past 2^53.
    gen_div = gen_div.with_columns(pl.Series("f", g._place(gen_div["f"].to_list(), 0.0, [nan, inf, -inf, -0.0, 0.0, 0.5, -0.5, 2.5, -2.5, 99.5]), dtype=pl.Float64))
    gen_strs = g.frame(g.strings(150, alphabet="ab ", maxlen=8, name="s"))
    gen_space = g.frame(g.strings(150, alphabet=" x\t　", maxlen=6, name="s"))
    gen_kolkata = g.frame(g.datetimes(120, "us", zone="Asia/Kolkata", name="ts"))
    day_us = 86_400_000_000
    dense = sorted(int(t) for t in g.rng.integers(0, day_us, 200)) + [0, day_us - 1]
    gen_day = datetimes(sorted(us("2024-03-10T00:00:00") + t for t in dense), zone="America/New_York")
    return [
        # --- S5: epoch floors to the second an instant lies in ----------------------
        case("epoch_us_before_1970", datetimes([us("1969-12-31T23:59:59.5"), us("1969-12-31T23:59:59")]),
             sel(ts.dt.epoch("s"))),
        case("epoch_ns_minus_1", datetimes([-1], "ns"), sel(ts.dt.epoch("s"))),
        case("epoch_gen", gen_us, sel(ts.dt.epoch("s"))),
        case("epoch_ns_gen", gen_ns, sel(ts.dt.epoch("s"))),
        # --- S6: is_in answers what == answers ----------------------------------------
        case("is_in_i8_5000", I8, sel(v.is_in([5000])), note="a value the type cannot hold is in nothing"),
        case("is_in_u64_minus_1", typed([(1 << 64) - 1], pl.UInt64), sel(v.is_in([-1]))),
        case("is_in_f32_0_1", typed([0.1], pl.Float32), sel(v.is_in([0.1])), note="0.1 as a Float32 is not 0.1"),
        case("is_in_f32_exact", typed([0.1], pl.Float32), sel(v.is_in(pl.Series([0.1], dtype=pl.Float32)))),
        case("is_in_dt_ms_finer", datetimes([1000], "ms", zone="UTC", name="v"),
             sel(v.is_in(pl.Series([datetime(1970, 1, 1, 0, 0, 1, 500)], dtype=pl.Datetime("us", "UTC")))),
             note="an instant finer than the column's unit is in nothing"),
        case("list_contains_i8_5000", LIST_I8, sel(v.list.contains(5000))),
        case("list_contains_i8_2", LIST_I8, sel(v.list.contains(2))),
        case("is_in_i32_1_3", typed([1, 2, 3], pl.Int32), sel(v.is_in([1, 3]))),
        case("is_in_nan", typed([nan, 0.0], pl.Float64), sel(v.is_in([nan])), note="NaN is in a set holding NaN"),
        case("is_in_gen", gen_ints, sel(v.is_in([0, 1, -1, 50, 1 << 53, (1 << 63) - 1]))),
        case("is_in_floats_gen", gen_floats, sel(v.is_in([nan, 0.0, 0.5, 2.5, 1e300]))),
        case("list_contains_gen", gen_lists, sel(v.list.contains(1).alias("one"), v.list.contains(5000).alias("none"))),
        # --- S7: a regex replace expands its groups -----------------------------------
        case("replace_groups", AB, sel(s.str.replace("(a)(b)", "$2$1"))),
        case("replace_group_braced", AB, sel(s.str.replace("(a)(b)", "${1}x"))),
        case("replace_literal_dollar", AB, sel(s.str.replace("ab", "$1", literal=True))),
        case("replace_gen", gen_strs, sel(s.str.replace("(a)(b)", "$2$1").alias("first"),
                                          s.str.replace_all("(a)(b)", "<$1>").alias("all"),
                                          s.str.replace("ab", "$1", literal=True).alias("lit"))),
        # --- S8: floor division floors and the remainder is its remainder -------------
        case("floordiv_mod_col", DIV, sel((a // b).alias("d"), (a % b).alias("m"))),
        case("floordiv_mod_const", typed([-7, 7], pl.Int64, "a"), sel((a // 2).alias("d"), (a % -2).alias("m"))),
        case("float_mod", pl.DataFrame({"a": [-7.0, 7.0, -7.5], "b": [2.0, -2.0, 2.0]}), sel((a % b).alias("m"))),
        case("float_mod_zero_sign", pl.DataFrame({"a": [4.0, 0.0], "b": [-2.0, -2.0]}), sel((a % b).alias("m")),
             note="a zero remainder: Polars computes a - b*floor(a/b), +0; ursus gives the divisor's sign, -0"),
        case("u8_floordiv_2", typed([7], pl.UInt8, "a"), sel(a // 2)),
        case("by_zero_null", pl.DataFrame({"a": pl.Series([-7], dtype=pl.Int64), "b": pl.Series([0], dtype=pl.Int64)}),
             sel((a // b).alias("d"), (a % b).alias("m"))),
        case("floordiv_mod_gen", gen_div, sel((a // b).alias("d"), (a % b).alias("m"), (a // 3).alias("d3"), (a % -3).alias("m3"),
                                              (c("f") // c("d")).alias("fd"), (c("f") % c("d")).alias("fm")), float="rel:1e-9",
             note="the float divisor is positive: a zero remainder's sign differs, float_mod_zero_sign; Polars' remainder is "
                  "a - b*floor(a/b), inexact in its last bits, where ursus's fmod is exact"),
        # --- S9: split is every part, splitn a struct of n -----------------------------
        case("split_len", pl.DataFrame({"s": ["a b c"]}), sel(s.str.split(" ").list.len())),
        case("splitn_field_1", pl.DataFrame({"s": ["a b c"]}), sel(s.str.splitn(" ", 2).struct.field("field_1"))),
        case("split_gen", gen_strs, sel(s.str.split(" ").alias("parts"), s.str.split(" ").list.len().alias("n"),
                                        s.str.splitn(" ", 2).struct.field("field_0").alias("f0"),
                                        s.str.splitn(" ", 2).struct.field("field_1").alias("f1"))),
        # --- S10: truncate floors on the column's wall clock ----------------------------
        case("truncate_kolkata_1h", datetimes([us("2024-01-01T05:17:00")], zone="Asia/Kolkata"), sel(ts.dt.truncate("1h")),
             note="Kolkata is +05:30: the local hour starts at 04:30Z"),
        case("truncate_ny_2h", datetimes([us("2024-01-01T08:30:00")], zone="America/New_York"), sel(ts.dt.truncate("2h"))),
        case("truncate_ny_fallback_1h", datetimes([us("2024-11-03T05:30:00"), us("2024-11-03T06:30:00")], zone="America/New_York"),
             sel(ts.dt.truncate("1h")), note="both 01:30s of the fall-back night, each in its own offset"),
        case("truncate_ny_spring_2h", datetimes([us("2024-03-10T07:30:00")], zone="America/New_York"), sel(ts.dt.truncate("2h")),
             note="the grid's 02:00 does not exist that night"),
        case("truncate_kolkata_1h_3000", datetimes([us("3000-01-01T05:17:00")], zone="Asia/Kolkata"), sel(ts.dt.truncate("1h")),
             note="past 2262, where UnixNano is undefined"),
        case("truncate_kolkata_1500ms", datetimes([us("2024-01-01T05:17:01.7")], zone="Asia/Kolkata"), sel(ts.dt.truncate("1500ms"))),
        case("truncate_ns_min_plus_1h", datetimes([-(1 << 63) + 3_600_000_000_000], "ns"), sel(ts.dt.truncate("1h"))),
        case("truncate_gen", gen_kolkata, sel(ts.dt.truncate("1h").alias("h"), ts.dt.truncate("15m").alias("q"),
                                             ts.dt.truncate("1d").alias("d"), ts.dt.truncate("1500ms").alias("ms"))),
        case("gbd_kolkata_1h", datetimes([us("2024-01-01T05:17:00"), us("2024-01-01T05:50:00")], zone="Asia/Kolkata"),
             lambda df: df.group_by_dynamic("ts", every="1h").agg(pl.len().alias("n"))),
        case("gbd_ny_spring_gen", gen_day, lambda df: df.group_by_dynamic("ts", every="1h").agg(pl.len().alias("n")),
             note="a day's rows in every hour, the spring-forward one skipped by the clock"),
        # --- S12, S13: an empty pattern counts, an empty cutset is Unicode space ------
        case("count_matches_empty_literal", STRS, sel(s.str.count_matches("", literal=True))),
        case("count_matches_empty_regex", STRS, sel(s.str.count_matches(""))),
        case("count_matches_gen", gen_strs, sel(s.str.count_matches("", literal=True).alias("empty"),
                                                s.str.count_matches("a", literal=True).alias("a"),
                                                s.str.count_matches("a+").alias("runs"))),
        case("strip_start_end_space", SPACE, sel(s.str.strip_chars_start().alias("l"), s.str.strip_chars_end().alias("r"))),
        case("strip_space", SPACE, sel(s.str.strip_chars())),
        case("strip_gen", gen_space, sel(s.str.strip_chars_start().alias("l"), s.str.strip_chars_end().alias("r"),
                                         s.str.strip_chars().alias("b"))),
    ]
