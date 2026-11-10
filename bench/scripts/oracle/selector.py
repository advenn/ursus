"""The selector group: each selector and their algebra over one frame of eight types
(selector/selector_test.go, step 503), the frame selected by each; a selector that
matches nothing, and Expand, as expand_selector's names. The generated frame has a
column of every type ursus has a generator for, under names of several shapes.
"""

from __future__ import annotations

import datetime as dt

import polars as pl
import polars.selectors as cs

from . import Case, case
from .gen import Gen

SEL = pl.DataFrame([
    pl.Series("id", [1], dtype=pl.Int64), pl.Series("Name", ["a"], dtype=pl.String),
    pl.Series("price", [1.5], dtype=pl.Float64), pl.Series("qty", [2], dtype=pl.Int64),
    pl.Series("when", [dt.date(2024, 1, 1)], dtype=pl.Date), pl.Series("ok", [True], dtype=pl.Boolean),
    pl.Series("at", [dt.datetime(2024, 1, 1)], dtype=pl.Datetime("us")), pl.Series("raw", [b"\x01"], dtype=pl.Binary),
])


def select(s: cs.Selector):
    return lambda df: df.select(s)


def expand(s: cs.Selector):
    """The names a selector expands to, as a frame of one String column."""
    return lambda df: pl.DataFrame({"name": pl.Series(list(cs.expand_selector(df, s)), dtype=pl.String)})


def cases() -> list[Case]:
    g = Gen(5034)
    n = 50
    gen = pl.DataFrame([
        g.ints(n, pl.Int32, name="a_1"), g.floats(n, name="b_2"), g.floats(n, name="Price"), g.ints(n, pl.UInt8, name="qty"),
        g.strings(n, name="note"), g.bools(n, name="ok"), g.dates(n, name="d"), g.datetimes(n, name="ts"),
        g.durations(n, unit="ms", name="span"), g.times(n, name="clock"), g.decimals(n, name="amt"),
        g.binary(n, name="blob"), g.lists(n, g.ints(n, pl.Int64, null_rate=0), name="l"),
    ])
    return [
        case("numeric", SEL, select(cs.numeric())),
        case("integer", SEL, select(cs.integer())),
        case("float", SEL, select(cs.float())),
        case("temporal", SEL, select(cs.temporal())),
        case("string", SEL, select(cs.string())),
        case("boolean", SEL, select(cs.boolean())),
        case("starts_with_p", SEL, select(cs.starts_with("p"))),
        case("ends_with_e", SEL, select(cs.ends_with("e"))),
        case("contains_a", SEL, select(cs.contains("a"))),
        case("matches", SEL, select(cs.matches("^[a-z]{2}$"))),
        case("first", SEL, select(cs.first())),
        case("last", SEL, select(cs.last())),
        case("by_index_0_last", SEL, select(cs.by_index(0, -1))),
        case("by_name_as_written", SEL, select(cs.by_name("qty", "id"))),
        case("numeric_minus_id", SEL, select(cs.numeric() - cs.by_name("id"))),
        case("not_numeric", SEL, select(~cs.numeric())),
        case("numeric_and_starts_with_q", SEL, select(cs.numeric() & cs.starts_with("q"))),
        case("numeric_or_string", SEL, select(cs.numeric() | cs.string())),
        case("numeric_xor_integer", SEL, select(cs.numeric() ^ cs.integer())),
        case("numeric_exclude_id_nope", SEL, select(cs.numeric().exclude("id", "nope"))),
        case("alpha", SEL, select(cs.alpha())),
        case("datetime", SEL, select(cs.datetime())),
        case("date", SEL, select(cs.date())),
        case("binary", SEL, select(cs.binary())),
        case("all_exclude_id_qty", SEL, select(cs.all().exclude("id", "qty"))),
        case("all", SEL, select(cs.all())),
        case("by_dtype_int64_bool", SEL, select(cs.by_dtype(pl.Int64, pl.Boolean))),
        case("expand_digit", SEL, expand(cs.digit()), note="no column is named by digits: no names"),
        case("expand_names_or_float", SEL, expand(cs.by_name("qty", "id") | cs.float())),
        case("expand_names", SEL, expand(cs.by_name("qty", "id"))),
        case("numeric_doubled", SEL, lambda df: df.select(cs.numeric().as_expr() * 2)),
        case("gen_numeric", gen, select(cs.numeric())),
        case("gen_temporal_or_boolean", gen, select(cs.temporal() | cs.boolean())),
        case("gen_matches_letter_digit", gen, select(cs.matches(r"^[a-z]_\d$"))),
        case("gen_by_index", gen, select(cs.by_index(1, 3, -1))),
        case("gen_not_text_nor_binary", gen, select(~(cs.string() | cs.binary()))),
        case("gen_contains_t_minus_integer", gen, select(cs.contains("t") - cs.integer())),
    ]
