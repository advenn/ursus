"""The csvwrite group: write_csv's options, an instant's text by its unit and zone, and
the text read back (csvwrite_test.go, step 354), each Polars 2.1.0's text.

A written text is the answer itself, compared byte for byte; the write_csv arguments
are the param options, and an instant's type the param to. Not here, with the reason:
a negative float precision, which Polars refuses at the Python layer; gzip and
Zstandard, whose bytes a Go check decompresses to the recorded default text; and
sink_csv's check of a path's extension, a filesystem's matter.
"""

from __future__ import annotations

import datetime as dt
import decimal

import polars as pl

from . import Case, case
from .encode import dtype_name
from .gen import Gen
from .iotext import shape_params, shaped, with_options

nan, inf = float("nan"), float("inf")

PROBE = pl.DataFrame({
    "i": pl.Series([1, None, -3], dtype=pl.Int64),
    "f": [1.5, 0.1, 1e20],
    "s": ["a", "b,c", 'q"x'],
    "d": pl.Series([dt.date(2020, 1, 2), None, dt.date(1999, 12, 31)], dtype=pl.Date),
    "t": pl.Series([dt.datetime(2020, 1, 2, 3, 4, 5, 600), None, dt.datetime(1999, 12, 31, 23, 59, 59)], dtype=pl.Datetime("us")),
    "tm": pl.Series([dt.time(1, 2, 3), None, dt.time(23, 59, 59, 500000)], dtype=pl.Time),
    "b": pl.Series([True, False, None], dtype=pl.Boolean),
})
F = PROBE.select("f")
ODD = pl.DataFrame({"f": [2.0, 1e-7, 1e16, 123456789.0, 0.30000000000000004, -0.0, nan, -inf, 1e300, 5e-324]})
NAN = pl.DataFrame({"x": [nan, inf, -0.0, 1234567.0]})
F32 = pl.DataFrame({"f32": pl.Series([1.5, 0.1], dtype=pl.Float32)})
DEC = pl.DataFrame({"d": pl.Series([decimal.Decimal("1.50"), decimal.Decimal("-2.25")], dtype=pl.Decimal(10, 2)), "f": [1.25, 2.5]})
NULLS = pl.DataFrame({"i": pl.Series([None], dtype=pl.Int64), "b": pl.Series([None], dtype=pl.Boolean), "s": pl.Series([None], dtype=pl.String)})
AT = pl.DataFrame({"t": pl.Series([dt.datetime(2020, 1, 2, 3, 4, 5, 123456)], dtype=pl.Datetime("us"))})


def wr(name: str, df: pl.DataFrame, note: str = "", select: str | None = None, head: int | None = None, **kw) -> Case:
    """The frame written under the param options, its write_csv arguments; select and
    head what the case writes of it."""
    return case(name, df, lambda df: shaped(df, select=select, head=head).write_csv(**kw),
                params=with_options(shape_params(select=select, head=head), kw), note=note)


def cast(to: pl.DataType) -> pl.Expr:
    """AT's instant as the type: a zoned type takes the instant as it is, UTC."""
    c = pl.col("t")
    if isinstance(to, pl.Datetime) and to.time_zone:
        return c.dt.replace_time_zone("UTC").dt.convert_time_zone(to.time_zone).cast(to)
    return c.cast(to)


def inst(name: str, to: pl.DataType, contains: str | None = None, **kw) -> Case:
    """AT's instant as the param to, written under the param options."""
    return case(name, AT, lambda df: df.select(cast(to)).write_csv(**kw), params=with_options({"to": dtype_name(to)}, kw),
                contains=contains or "")


def back(name: str, to: pl.DataType) -> Case:
    """Written and read back by ScanCSV under the type, an offset +0900 as written; the
    param options are the reading's."""
    kw = {"schema_overrides": {"t": to}}
    return case(name, AT, lambda df: pl.read_csv(df.select(cast(to)).write_csv().encode(), **kw),
                params=with_options({"to": dtype_name(to)}, kw))


def cases() -> list[Case]:
    g = Gen(5024)
    text = g.strings(100, edges=False).to_list()
    text[0:5] = ["", "a,b", '"q"', "é", "𝔘rsus"]
    gen = g.frame(g.ints(100, name="i"), g.floats(100, name="f"), pl.Series("s", text, dtype=pl.String), g.dates(100),
                  g.datetimes(100, "us"), g.datetimes(100, "us", zone="Asia/Tokyo", name="tz"), g.times(100), g.bools(100),
                  g.floats(100, name="f32", dtype=pl.Float32, edges=False))
    utc, tokyo = pl.Datetime("us", "UTC"), pl.Datetime("us", "Asia/Tokyo")
    return [
        wr("default", PROBE),
        wr("quote_necessary", PROBE, quote_style="necessary"),
        wr("quote_always", PROBE, quote_style="always"),
        wr("quote_never", PROBE, quote_style="never"),
        wr("quote_non_numeric", PROBE, quote_style="non_numeric"),
        wr("own_quote", PROBE, quote_char="'"),
        wr("formats", PROBE, date_format="%d/%m/%Y", datetime_format="%Y%m%d %H:%M", time_format="%H.%M"),
        wr("bom", PROBE, include_bom=True),
        wr("null_text_quoted", PROBE, null_value="NA", quote_style="always"),
        # floats
        wr("precision_2", F, float_precision=2),
        wr("precision_0", F, float_precision=0),
        wr("scientific", F, float_scientific=True),
        wr("never_scientific", F, float_scientific=False),
        wr("scientific_precision_2", F, float_scientific=True, float_precision=2),
        wr("decimal_comma", F, decimal_comma=True, note="Polars quotes every float under a decimal comma and a comma's separator"),
        wr("decimal_comma_semicolon", F, decimal_comma=True, separator=";"),
        wr("own_text", ODD, note="a float's own text is its shortest, Polars' cast's (step 325)"),
        wr("own_text_never_scientific", ODD, head=8, float_scientific=False),
        wr("nan_precision_2", NAN, float_precision=2),
        wr("nan_scientific", NAN, float_scientific=True),
        wr("float32_precision_3", F32, float_precision=3),
        wr("float32_own_text", pl.DataFrame({"f32": pl.Series([2.0, 1e-7, 0.1], dtype=pl.Float32)})),
        wr("decimal_under_precision", DEC, float_precision=1, note="a Decimal keeps its scale under a float precision"),
        wr("decimal_comma_of_a_decimal", DEC, select="d", decimal_comma=True),
        wr("integer_beside_comma", pl.DataFrame({"i": [1], "f": [2.5]}), float_precision=1, decimal_comma=True, quote_style="non_numeric"),
        wr("nulls_non_numeric", NULLS, null_value="NA", quote_style="non_numeric"),
        wr("never_empty_text", pl.DataFrame({"s": ["", None]}), quote_style="never"),
        # instants, by unit and zone
        inst("instant_us", pl.Datetime("us")),
        inst("instant_ms", pl.Datetime("ms")),
        inst("instant_ns", pl.Datetime("ns")),
        inst("instant_utc", utc),
        inst("instant_tokyo", tokyo),
        inst("instant_utc_format_offset", utc, datetime_format="%Y-%m-%d %H:%M%z"),
        inst("instant_utc_format_zone_name", utc, datetime_format="%Y-%m-%d %H:%M:%S %Z"),
        inst("instant_format_millis", pl.Datetime("us"), datetime_format="%Y-%m-%dT%H:%M:%S%.3f"),
        inst("date_ignores_datetime_format", pl.Date, datetime_format="%Y"),
        inst("instant_ignores_date_format", pl.Datetime("us"), date_format="%Y"),
        inst("time_format_millis", pl.Time, time_format="%H:%M:%S%.3f"),
        inst("naive_instant_zone_refused", pl.Datetime("us"), contains="", datetime_format="%Y %z"),
        inst("date_hour_refused", pl.Date, contains="", date_format="%H"),
        inst("time_year_refused", pl.Time, contains="", time_format="%Y"),
        inst("no_such_directive_refused", pl.Datetime("us"), contains="", datetime_format="%Q"),
        back("instant_us_read_back", pl.Datetime("us")),
        back("instant_utc_read_back", utc),
        back("instant_tokyo_read_back", tokyo),
        # --- generated -------------------------------------------------------------------
        wr("default_gen", gen, note="no field begins with a space, which ursus quotes and Polars writes bare (feature-gaps.md §9)"),
        wr("quote_always_gen", gen, quote_style="always"),
        wr("precision_3_gen", gen, float_precision=3),
        wr("scientific_gen", gen, float_scientific=True),
        wr("decimal_comma_gen", gen, decimal_comma=True, separator=";"),
        wr("formats_gen", gen, date_format="%d/%m/%Y", datetime_format="%Y%m%d %H:%M:%S", time_format="%H.%M"),
    ]
