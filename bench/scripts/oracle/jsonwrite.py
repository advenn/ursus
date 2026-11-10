"""The jsonwrite group: write_ndjson and write_json of one frame holding every kind of
value, far years, durations and no rows (jsonwrite_test.go, step 333), each Polars
2.1.0's text, compared byte for byte.

Not here, with the reason: batches of one row, a Go differential; sink_ndjson's file,
a filesystem's matter; and a Binary column, which Polars 2.1 panics on where ursus
refuses.
"""

from __future__ import annotations

import decimal

import polars as pl

from . import Case, case
from .gen import Gen

nan = float("nan")


def i64(name: str, vals: list) -> pl.Series:
    return pl.Series(name, vals, dtype=pl.Int64)


FRAME = pl.DataFrame({
    "i": i64("i", [1, None, 3]),
    "f": [1.0, None, 1e20],
    "g": [nan, -0.0, 1e-7],
    "s": ['a"b', None, "é\n</x>\\"],
    "b": [True, None, False],
    "d": pl.Series("d", [19724, None, -719162], dtype=pl.Int32).cast(pl.Date),
    "t": i64("t", [1704164645123456, None, 1704153600000000]).cast(pl.Datetime("us")),
    "tz": i64("tz", [1704164645123456, None, 1719889445000000]).cast(pl.Datetime("us")).dt.replace_time_zone("UTC").dt.convert_time_zone("Asia/Kolkata"),
    "du": i64("du", [86403000005, None, -500000]).cast(pl.Duration("us")),
    "tm": i64("tm", [11045000006000, None, 0]).cast(pl.Time),
    "dec": pl.Series("dec", [decimal.Decimal("1.25"), None, decimal.Decimal("-3.50")], dtype=pl.Decimal(10, 2)),
    "l": pl.Series("l", [[1, None], [], None], dtype=pl.List(pl.Int64)),
    "st": pl.Series("st", [{"x": 1, "y": "p"}, None, {"x": None, "y": "q"}], dtype=pl.Struct({"x": pl.Int64, "y": pl.String})),
})


def cases() -> list[Case]:
    g = Gen(5027)
    ints = g.ints(80, name="i")
    gen = g.frame(ints, g.floats(80, name="f"), g.strings(80, name="s"), g.bools(80), g.dates(80),
                  g.datetimes(80, "us"), g.datetimes(80, "us", zone="Asia/Kolkata", name="tz"), g.durations(80), g.times(80),
                  g.decimals(80, 10, 2), g.lists(80, g.ints(30, name="e"), name="l"),
                  pl.DataFrame([ints.alias("x"), g.strings(80, name="y", edges=False)]).to_struct("st"))
    return [
        case("ndjson", FRAME, lambda df: df.write_ndjson()),
        case("json", FRAME, lambda df: df.write_json()),
        case("far_years", pl.DataFrame({"d": pl.Series("d", [2932897, -719529, -719528], dtype=pl.Int32).cast(pl.Date)}),
             lambda df: df.write_ndjson(), note="a year past 9999, and before 1: signed, as chrono writes it"),
        case("durations", pl.DataFrame({"du": i64("du", [0, 60_000]).cast(pl.Duration("ms"))}), lambda df: df.write_ndjson(),
             note="no length is P0D; a minute of milliseconds PT60S"),
        case("no_rows_ndjson", FRAME, lambda df: df.head(0).write_ndjson()),
        case("no_rows_json", FRAME, lambda df: df.head(0).write_json()),
        # --- generated -----------------------------------------------------------------------
        case("ndjson_gen", gen, lambda df: df.write_ndjson(), note="NaN and the infinities are written null"),
        case("json_gen", gen, lambda df: df.write_json()),
    ]
