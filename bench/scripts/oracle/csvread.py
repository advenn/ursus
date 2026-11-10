"""The csvread group: read_csv's options ignore_errors, infer_schema, raise_if_empty,
decimal_comma and encoding, and text checked as UTF-8 (csvread_test.go, step 357), each
Polars 2.1.0's answer.

The input is the param csv, or csv_b64 where the bytes are not UTF-8. Not here, with
the reason: raise_if_empty=False of an empty file, a frame of no columns, which the
format does not hold (a structural check in Go); a separator past ASCII, which Polars
refuses as an option; and the serial and the blocked reading paths, which a Go
differential compares.
"""

from __future__ import annotations

import polars as pl

from . import Case
from .gen import Gen
from .iotext import csv_case

I64 = {"a": pl.Int64}
SEMI = {"separator": ";", "decimal_comma": True}


def cases() -> list[Case]:
    g = Gen(5022)
    gen = g.frame(g.ints(150, name="i"), g.floats(150, name="f"), g.strings(150, name="s"), g.bools(150))
    floats = g.frame(g.floats(120, name="f", edges=False), g.ints(120, name="i"))
    return [
        csv_case("ignore_errors", "a\n1\nx\n3\n", schema_overrides=I64, ignore_errors=True),
        csv_case("ignore_errors_of_a_date", "a\n2020-13-45\n", schema_overrides={"a": pl.Date}, ignore_errors=True),
        csv_case("short_record", "a,b\n1,2\n3\n"),
        csv_case("blank_line", "a,b\n1,2\n\n3,4\n"),
        csv_case("blank_line_last", "a,b\n1,2\n\n\n"),
        csv_case("blank_line_crlf", "a,b\r\n1,2\r\n\r\n3,4\r\n"),
        csv_case("blank_line_before_header", "\na,b\n1,2\n"),
        csv_case("blank_line_first_no_header", "\n1,2\n3,4\n", has_header=False),
        csv_case("blank_line_among_strings", "a,b\n1,x\n\n3,y\n"),
        csv_case("no_inference", "a,b\n1,2\n", infer_schema=False),
        csv_case("no_inference_an_override", "a,b\n1,2\n", infer_schema=False, schema_overrides=I64),
        csv_case("header_alone", "a,b\n", raise_if_empty=False),
        csv_case("decimal_comma", "a;b\n1,5;2\n", **SEMI),
        csv_case("point_under_decimal_comma", "a;b\n1.5;2\n", **SEMI),
        csv_case("thousands_point", "a;b\n1.234,5;2\n", **SEMI),
        csv_case("exponent_decimal_comma", "a;b\n1,5e3;2\n", **SEMI),
        csv_case("quoted_decimal_comma", "a,b\n\"1,5\",2\n", decimal_comma=True),
        csv_case("float32_decimal_comma", "a;b\n1,5;2,25\n", schema_overrides={"b": pl.Float32}, **SEMI),
        csv_case("short_record_under_limit", "a,b\n1,2\n3\n", head=2),
        csv_case("ignore_errors_under_limit", "a\n1\nx\n3\n", schema_overrides=I64, ignore_errors=True, head=3),
        csv_case("decimal_comma_of_a_decimal", "a;b\n1,5;x\n", schema_overrides={"a": pl.Decimal(10, 2)}, **SEMI),
        csv_case("utf8_lossy", b"a\n\xff\xfe\n", encoding="utf8-lossy"),
        csv_case("utf8_lossy_maximal_subpart", b"a\n\xe2\x82A\n", encoding="utf8-lossy"),
        csv_case("utf8_lossy_sign_and_stray_byte", b"a\n\xe2\x82\xac\xff\n", encoding="utf8-lossy"),
        csv_case("name_not_utf8", b"a\xff,b\n1,1\n", note="a name that is not UTF-8 is read so whatever the encoding"),
        csv_case("text_under_limit", "a\n東京\nZürich\n€\n", head=3),
        csv_case("lossy_text_under_limit", b"a\n\xff\nZ\xc3\xbcrich\n", encoding="utf8-lossy", head=2),
        # --- refused, as Polars refuses them -----------------------------------------------
        csv_case("value_without_ignore_errors", "a\n1\nx\n", schema_overrides=I64, contains="WithIgnoreErrors"),
        csv_case("long_record_under_ignore_errors", "a,b\n1,2\n3,4,5\n", ignore_errors=True, contains="3 fields"),
        csv_case("empty_file", "", contains="WithRaiseIfEmpty"),
        csv_case("empty_file_no_header", "", has_header=False, contains="WithRaiseIfEmpty"),
        csv_case("not_utf8", b"a,b\n1,\xff\n", contains="UTF8Lossy"),
        csv_case("not_utf8_as_a_number", b"a,b\n\xff,1\n", schema_overrides=I64, contains="Int64"),
        csv_case("not_utf8_under_ignore_errors", b"a,b\n1,\xff\n", ignore_errors=True, contains="UTF8Lossy"),
        csv_case("character_cut_across_fields", b"a\n\xe2\x82\n\xac\n", contains="UTF8Lossy",
                 note="two fields whose bytes together are a character, each not one"),
        csv_case("character_cut_across_quoted_fields", b"a,b\n\"\xe2\x82\",1\n\"\xac\",2\n", contains="UTF8Lossy"),
        csv_case("not_utf8_under_limit", b"a,b\n1,x\n2,\xff\n", head=2, contains="UTF8Lossy"),
        csv_case("cut_character_under_limit", b"a\n\xe2\x82\n\xac\n", head=2, contains="UTF8Lossy"),
        csv_case("not_utf8_under_ignore_errors_and_limit", b"a,b\n1,\xff\n", ignore_errors=True, head=1,
                 contains="UTF8Lossy"),
        # --- generated -------------------------------------------------------------------
        csv_case("round_trip_gen", gen.write_csv(), note="Polars' own CSV of a generated frame, read back"),
        csv_case("decimal_comma_gen", floats.write_csv(decimal_comma=True, separator=";"), **SEMI),
    ]
