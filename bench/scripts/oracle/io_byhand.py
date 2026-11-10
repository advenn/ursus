"""The io_byhand group: CSV and I/O against answers that were worked out by hand
(io_byhand_test.go; audit.md §5: I8-I10, I19, I21, I22 and I27, with S21 and S25's String
casts, which share the parsers), each Polars 2.1.0's answer now.

A read is read_csv of the param csv; a round trip is write_csv read back by read_csv;
each under the param options, its read_csv arguments; a written text is the answer
itself.
Not here, with the reason: i128.Parse's overflow, a Go API; and I22's refusal of a frame
with rows and no columns, an ursus rule (Polars writes a bare newline).
"""

from __future__ import annotations

import datetime as dt

import polars as pl

from . import Case, case
from .gen import Gen
from .iotext import csv_case, with_options

nan, inf = float("nan"), float("inf")


def rt(name: str, df: pl.DataFrame, note: str = "", **kw) -> Case:
    """Written by Polars and read back by Polars, under the param options."""
    return case(name, df, lambda df: pl.read_csv(df.write_csv().encode(), **kw), params=with_options({}, kw), note=note)


def wr(name: str, df: pl.DataFrame, note: str = "") -> Case:
    return case(name, df, lambda df: df.write_csv(), note=note)


def zoned(zone: str, iso: str) -> pl.DataFrame:
    """One UTC instant, as a Datetime(us) of the zone."""
    s = pl.Series("t", [dt.datetime.fromisoformat(iso)], dtype=pl.Datetime("us"))
    return pl.DataFrame({"t": s.dt.replace_time_zone("UTC").dt.convert_time_zone(zone)})


def strs(*vals: str) -> pl.DataFrame:
    return pl.DataFrame({"s": pl.Series(list(vals), dtype=pl.String)})


def cases() -> list[Case]:
    s = pl.col("s")
    ams, mon = pl.Datetime("us", "Europe/Amsterdam"), pl.Datetime("us", "Africa/Monrovia")
    g = Gen(5021)
    gen = g.frame(g.ints(120, name="i"), g.floats(120, name="f"), g.strings(120, name="s"), g.bools(120))
    text = g.strings(80, edges=False).to_list()
    text[0:4] = ["", "a,b", '"q"', "é"]
    plain = g.frame(g.ints(80, name="i"), g.floats(80, name="f"), pl.Series("s", text, dtype=pl.String), g.bools(80))
    return [
        # --- I8: an offset with seconds is written in UTC --------------------------------
        rt("i8_amsterdam_1930_round_trips", zoned("Europe/Amsterdam", "1929-12-31T23:40:28"), schema_overrides={"t": ams},
           note="Amsterdam's offset was +00:19:32 until 1937: the instant is written in UTC and reads back"),
        rt("i8_monrovia_1970_round_trips", zoned("Africa/Monrovia", "1970-01-01T00:44:30"), schema_overrides={"t": mon}),
        wr("control_amsterdam_2024_text", zoned("Europe/Amsterdam", "2024-01-01T12:00:00")),
        # --- I9 / S25: one float grammar, without underscores or hex ---------------------
        csv_case("i9_inference_underscore_and_hex", "a,b\n1_000,0x1p3\n"),
        csv_case("i9_float64_schema_refuses_underscore", "a\n1_000\n", schema={"a": pl.Float64}, contains="1_000"),
        case("s25_cast_refuses_underscore", strs("1_000"), lambda df: df.select(s.cast(pl.Float64)), contains="1_000"),
        case("s25_cast_refuses_hex", strs("0x1p3"), lambda df: df.select(s.cast(pl.Float64)), contains="0x1p3"),
        case("s25_cast_lossy_hex_is_null", strs("0x1p3"), lambda df: df.select(s.cast(pl.Float64, strict=False))),
        case("control_float_grammar", strs("1e3", "+1.5", ".5", "5.", "-inf", "NaN", "-1E-2"), lambda df: df.select(s.cast(pl.Float64))),
        # --- I10: a blank line is a record of nulls ---------------------------------------
        csv_case("i10_blank_line_int", "a\n1\n\n3\n"),
        csv_case("i10_blank_line_string", "a\nx\n\nz\n"),
        csv_case("i10_trailing_blank_line", "a\n1\n2\n\n"),
        csv_case("i10_blank_line_no_header", "1\n\n3\n", has_header=False),
        csv_case("blank_line_two_columns", "a,b\n1,2\n\n3,4\n"),
        # --- I19: Int128 and Decimal, read and parsed exactly -----------------------------
        csv_case("i19_u64_max_inferred", "a\n18446744073709551615\n"),
        csv_case("i19_2p63_beside_1", "a\n9223372036854775808\n1\n"),
        csv_case("i19_int128_schema", "a\n170141183460469231731687303715884105727\n-5\n", schema={"a": pl.Int128}),
        csv_case("i19_decimal_schema_rounds_half_away", "a\n12.34\n1.235\n-1.235\n7\n", schema={"a": pl.Decimal(10, 2)}),
        csv_case("i19_decimal_past_precision_refused", "a\n123456789012\n", schema={"a": pl.Decimal(10, 2)}, contains="123456789012"),
        case("s21_cast_string_int128", strs("-170141183460469231731687303715884105728", "42"), lambda df: df.select(s.cast(pl.Int128))),
        case("s21_cast_string_decimal", strs("1.235", "-0.5"), lambda df: df.select(s.cast(pl.Decimal(10, 2)))),
        # --- I21: a float is written so it reads back as a float --------------------------
        rt("i21_float64_round_trips", pl.DataFrame({"f": [1.0, 2.5, nan, inf, -inf]})),
        rt("i21_integral_float64_round_trips", pl.DataFrame({"f": [1.0, 2.0, -3.0]})),
        wr("i21_specials_written", pl.DataFrame({"f": [1.0, inf, -inf, nan]})),
        rt("control_float32_round_trips_as_float64", pl.DataFrame({"h": pl.Series([1.5, 2.0], dtype=pl.Float32)})),
        csv_case("control_only_nan_inferred", "a\nNaN\n", note="a column of only NaN: Polars 2.1 infers Float64"),
        # --- I27: a null String and an empty one are written and read apart ---------------
        rt("i27_null_string_round_trips", pl.DataFrame({"s": ["", None, "x"], "n": [1, 2, 3]})),
        csv_case("i27_unquoted_empty_is_null", "s,n\n,1\n\"\",2\n"),
        # --- I22 -------------------------------------------------------------------------
        case("i22_no_rows_no_columns_written", pl.DataFrame({"a": [1]}), lambda df: df.filter(pl.col("a") > 9).select().write_csv(),
             note="a frame of no rows and no columns: Polars writes a bare newline"),
        # --- generated -------------------------------------------------------------------
        rt("round_trip_gen", gen),
        wr("written_gen", plain, note="no field begins with a space, which ursus quotes and Polars writes bare (feature-gaps.md §9)"),
    ]
