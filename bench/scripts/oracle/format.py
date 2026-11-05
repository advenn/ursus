"""The format group: every type the format spells, generated, through the identity
query, so the Python encoder and the Go loader are proved to agree on each; then a
set-ordered answer and a refusal, the format's other two shapes.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen


def cases() -> list[Case]:
    g = Gen(496)
    n = 60
    ints = g.frame(*[g.ints(n, t, name=str(t)) for t in
                     (pl.Int8, pl.Int16, pl.Int32, pl.Int64, pl.UInt8, pl.UInt16, pl.UInt32, pl.UInt64, pl.Int128)])
    floats = g.frame(g.floats(n, name="f64"), g.floats(n, dtype=pl.Float32, name="f32"))
    text = g.frame(g.bools(n), g.strings(n), g.binary(n))
    temporal = g.frame(g.dates(n), g.datetimes(n, "us", name="t_us"), g.datetimes(n, "ms", name="t_ms"),
                       g.datetimes(n, "ns", "Europe/Paris", name="t_paris"),
                       g.datetimes(n, "us", "America/New_York", lo=2024, hi=2024, name="t_ny"),
                       g.durations(n, "ms", name="du_ms"), g.durations(n, "ns", name="du_ns"), g.times(n))
    decimals = g.frame(g.decimals(n, 12, 4, name="d12_4"), g.decimals(n, 38, 10, name="d38_10"),
                       g.decimals(n, 5, 0, name="d5_0"))
    words = g.strings(n, alphabet="ab", maxlen=2, edges=False, name="w")
    categorical = g.frame(words.cast(pl.Categorical).alias("cat"),
                          words.cast(pl.Enum(["a", "b", "aa", "ab", "ba", "bb"])).alias("enum"))
    elems_i = g.ints(n, pl.Int64, lo=-100, hi=100, name="e")
    elems_f = g.floats(n, name="e")
    elems_s = g.strings(n, name="e")
    inner = g.lists(n, elems_i, name="inner")
    nested = g.frame(
        g.lists(n, elems_i, name="l_i64"), g.lists(n, elems_f, name="l_f64"), g.lists(n, elems_s, name="l_str"),
        g.lists(n, g.datetimes(n, "us", "Asia/Tokyo", name="e"), name="l_t"),
        g.lists(n, inner, name="l_l_i64"),
        pl.Series("arr", [None if v is None else (v + [None] * 3)[:3] for v in g.lists(n, elems_f, maxlen=3).to_list()],
                  dtype=pl.Array(pl.Float64, 3)),
        pl.Series("st", [None if a is None and b is None and i % 7 == 0 else {"a": a, "b": b}
                         for i, (a, b) in enumerate(zip(elems_i.to_list(), elems_s.to_list()))],
                  dtype=pl.Struct({"a": pl.Int64, "b": pl.String})),
    )
    empty = pl.DataFrame({"i": pl.Series([], dtype=pl.Int64), "s": pl.Series([], dtype=pl.String),
                          "t": pl.Series([], dtype=pl.Datetime("us", "UTC"))})
    keys = g.ints(n, pl.Int64, lo=0, hi=5, null_rate=0.1, edges=False, name="k")
    grouped = g.frame(keys, g.floats(n, edges=False, name="v"))
    return [
        case("identity_ints", ints, lambda df: df.select(pl.all())),
        case("identity_floats", floats, lambda df: df.select(pl.all())),
        case("identity_bool_string_binary", text, lambda df: df.select(pl.all())),
        case("identity_temporal", temporal, lambda df: df.select(pl.all())),
        case("identity_decimals", decimals, lambda df: df.select(pl.all())),
        case("identity_categorical_enum", categorical, lambda df: df.select(pl.all())),
        case("identity_nested", nested, lambda df: df.select(pl.all())),
        case("identity_empty", empty, lambda df: df.select(pl.all())),
        case("two_inputs_concat", {"top": ints.head(5), "bottom": ints.tail(5)},
             lambda top, bottom: pl.concat([top, bottom])),
        case("set_group_sum", grouped, lambda df: df.group_by("k").agg(pl.col("v").sum()), order="set",
             float="rel:1e-12", note="a group-by's row order is unspecified, so the test sorts both sides; "
             "a float sum's order leaves last bits apart"),
        case("refusal_abs_of_text", text, lambda df: df.select(pl.col("s").abs()), contains="",
             note="Polars refuses; the test asserts ursus refuses too"),
    ]
