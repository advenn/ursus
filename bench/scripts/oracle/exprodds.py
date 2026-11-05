"""The exprodds group, step 324's odds and ends: limit, truediv, pipe, arctan2,
to_physical, reinterpret, item, lower_bound, upper_bound and is_sorted
(exprodds_test.go). Every literal frame is the Go test's, a generated case beside
each; the refusals, the type checks and the batch-size differential stay in the Go
test.
"""

from __future__ import annotations

import decimal
from datetime import date, datetime, timedelta

import polars as pl

from . import Case, case
from .gen import Gen

nan, inf = float("nan"), float("inf")
c = pl.col
a = c("a")

INTS = pl.DataFrame({"a": [3, 1, 2], "b": [2, 2, 2]})
YX = pl.DataFrame({"y": [1, None, 0, 0], "x": [1, 1, 0, -1]})
PHYS = pl.DataFrame({
    "d": [date(1970, 1, 3)], "t": [datetime(1970, 1, 1, 0, 0, 1)], "u": [timedelta(seconds=1)], "e": ["b"],
    "m": [decimal.Decimal("1.50")], "k": [True], "s": ["x"],
}, schema_overrides={"e": pl.Enum(["a", "b"]), "m": pl.Decimal(10, 2), "t": pl.Datetime("us"), "u": pl.Duration("us")},
).with_columns(c("t").dt.time().alias("h")).select("d", "t", "u", "h", "e", "m", "k", "s")
REINTERPRET = pl.DataFrame({"u": pl.Series([(1 << 64) - 1, 1], dtype=pl.UInt64), "i": pl.Series([-1, 2], dtype=pl.Int64),
                            "i8": pl.Series([-1, 2], dtype=pl.Int8)})
BOUNDS = pl.DataFrame({"i8": pl.Series([1], dtype=pl.Int8), "u": pl.Series([1], dtype=pl.UInt64),
                       "f": pl.Series([1], dtype=pl.Float32), "g": [1.0], "d": [date(1970, 1, 2)], "t": [datetime(1970, 1, 1)]})
SORTED = {
    "1_2_2_3": pl.Series([1, 2, 2, 3], dtype=pl.Int64),
    "3_2_1": pl.Series([3, 2, 1], dtype=pl.Int64),
    "null_1_2": pl.Series([None, 1, 2], dtype=pl.Int64),
    "1_2_null": pl.Series([1, 2, None], dtype=pl.Int64),
    "null_null": pl.Series([None, None], dtype=pl.Int64),
    "none": pl.Series([], dtype=pl.Int64),
    "1_nan": pl.Series([1.0, nan], dtype=pl.Float64),
    "nan_1": pl.Series([nan, 1.0], dtype=pl.Float64),
    "a_b_B": pl.Series(["a", "b", "B"], dtype=pl.String),
}


def sel(*e: pl.Expr):
    return lambda df: df.select(*e)


def sorted4(e: pl.Expr, suffix: str = "") -> list[pl.Expr]:
    return [e.is_sorted().alias("asc" + suffix), e.is_sorted(descending=True).alias("desc" + suffix),
            e.is_sorted(nulls_last=True).alias("asc_last" + suffix),
            e.is_sorted(descending=True, nulls_last=True).alias("desc_last" + suffix)]


def cases() -> list[Case]:
    g = Gen(4975)
    gen_ab = g.frame(g.ints(120, pl.Int64, lo=-50, hi=50, name="a"), g.ints(120, pl.Int64, lo=-5, hi=5, name="b"))
    gen_yx =g.frame(g.floats(150, lo=-10, hi=10, name="y"), g.floats(150, lo=-10, hi=10, name="x"))
    gen_phys = g.frame(g.dates(60, name="d"), g.datetimes(60, "ms", zone="Europe/Paris", name="t"), g.durations(60, "ns", name="u"),
                       g.times(60, name="h"), g.decimals(60, 12, 4, name="m"), g.bools(60, name="k"),
                       g.strings(60, alphabet="ab", maxlen=2, name="s").cast(pl.Enum(["a", "b", "aa", "ab", "ba", "bb"]), strict=False))
    gen_reinterpret = g.frame(g.ints(120, pl.UInt64, name="u"), g.ints(120, pl.Int64, name="i"),
                              g.ints(120, pl.Int8, name="i8"), g.ints(120, pl.UInt16, name="u16"))
    gen_bounds = g.frame(g.ints(20, pl.Int16, name="i16"), g.ints(20, pl.UInt8, name="u8"), g.floats(20, name="f"),
                         g.dates(20, name="d"), g.durations(20, "ms", name="du"), g.times(20, name="tm"))
    asc = g.ints(100, pl.Int64, null_rate=0.1).sort()
    rnd = g.ints(100, pl.Int64, null_rate=0.1)
    # From lists: a Series Polars sorted carries a sorted flag that is_sorted reads
    # instead of looking, and the flag does not know where the nulls are.
    gen_sorted = pl.DataFrame({"asc": asc.to_list(), "desc": asc.reverse().to_list(), "asc_last": asc.sort(nulls_last=True).to_list(),
                               "rnd": rnd.to_list()}, schema={"asc": pl.Int64, "desc": pl.Int64, "asc_last": pl.Int64, "rnd": pl.Int64})
    gen_item = g.frame(g.ints(1, pl.Int64, null_rate=0, name="a"), g.strings(1, null_rate=0, name="s"))
    out = [
        # --- limit, truediv, pipe ------------------------------------------------------
        case("limit_2", INTS, sel(a.limit(2))),
        case("truediv", INTS, sel(a.truediv(c("b")))),
        case("pipe_triple", INTS, sel(a.pipe(lambda e: e * 3))),
        case("limit_gen", gen_ab, sel(a.limit(7))),
        case("truediv_pipe_gen", gen_ab, sel(a.truediv(c("b")).alias("q"), a.pipe(lambda e: e * 3).alias("p"))),
        # --- arctan2 ---------------------------------------------------------------------
        case("arctan2", YX, sel(pl.arctan2(c("y"), c("x")))),
        case("arctan2_f32", pl.DataFrame({"y": pl.Series([1.0], dtype=pl.Float32), "x": pl.Series([1.0], dtype=pl.Float32)}),
             sel(pl.arctan2(c("y"), c("x")))),
        case("arctan2_gen", gen_yx, sel(pl.arctan2(c("y"), c("x"))), float="rel:1e-15", note="Go's atan2 against glibc's"),
        # --- to_physical, reinterpret -------------------------------------------------
        case("to_physical", PHYS, sel(pl.all().to_physical())),
        case("to_physical_gen", gen_phys, sel(pl.all().to_physical())),
        case("reinterpret", REINTERPRET, sel(c("u").reinterpret(signed=True), c("i").reinterpret(signed=False), c("i8").reinterpret(signed=False))),
        case("reinterpret_gen", gen_reinterpret, sel(c("u").reinterpret(signed=True), c("i").reinterpret(signed=False),
                                                     c("i8").reinterpret(signed=False), c("u16").reinterpret(signed=True))),
        # --- item -------------------------------------------------------------------------
        case("item_one", pl.DataFrame({"a": [7], "b": [1]}), sel(a.item(), c("b"))),
        case("item_null", pl.DataFrame({"a": pl.Series([None], dtype=pl.Int64)}), sel(a.item())),
        case("item_empty_allowed", INTS.head(0), sel(a.item(allow_empty=True))),
        case("item_gen", gen_item, sel(a.item(), c("s").item())),
        # --- the bounds -----------------------------------------------------------------
        case("lower_bounds", BOUNDS, sel(pl.all().lower_bound())),
        case("upper_bounds", BOUNDS, sel(pl.all().upper_bound())),
        case("bound_broadcast", pl.DataFrame({"a": pl.Series([1, 2], dtype=pl.Int16)}), sel(a.upper_bound().alias("hi"), a)),
        case("bound_grouped", pl.DataFrame({"g": [1, 1, 2], "a": [1, 2, 3]}),
             lambda df: df.group_by("g", maintain_order=True).agg(a.lower_bound())),
        case("bounds_gen", gen_bounds, sel(pl.all().lower_bound().name.suffix("_lo"), pl.all().upper_bound().name.suffix("_hi"))),
        # --- is_sorted ------------------------------------------------------------------
        *[case(f"is_sorted_{name}", pl.DataFrame({"a": s}), sel(*sorted4(a))) for name, s in SORTED.items()],
        case("is_sorted_grouped", pl.DataFrame({"g": [1, 1, 2, 2], "a": [1, 2, 4, 3]}),
             lambda df: df.group_by("g", maintain_order=True).agg(a.is_sorted())),
        case("is_sorted_gen", gen_sorted, sel(*sorted4(c("asc"), "_a"), *sorted4(c("desc"), "_d"), *sorted4(c("asc_last"), "_l"),
                                              *sorted4(c("rnd"), "_r"))),
    ]
    return out
