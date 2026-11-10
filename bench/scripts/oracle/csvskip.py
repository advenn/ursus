"""The csvskip group: read_csv's options skip_rows_after_header, skip_lines and
quote_char=None (csvskip_test.go, step 358), each Polars 2.1.0's answer.

The input is the param csv, read under the param options; the two-file cases read it
twice. Not here, with the reason: skip_lines past the end under raise_if_empty=False,
a frame of no columns, which the format does not hold; and a value error's physical
line, ursus's own text.
"""

from __future__ import annotations

import io

import polars as pl

from . import Case
from .gen import Gen
from .iotext import csv_case, with_options

THREE = "a,b\n1,2\n3,4\n5,6\n"


def twice(name: str, **kw) -> Case:
    b = THREE.encode()
    return Case(name, {}, lambda: pl.read_csv([io.BytesIO(b), io.BytesIO(b)], **kw), params=with_options({"csv": THREE}, kw),
                note="two files, each skipping its own rows and lines")


def cases() -> list[Case]:
    g = Gen(5023)
    gen = g.frame(g.ints(90, name="i"), g.floats(90, name="f"), g.strings(90, name="s"))
    plain = g.frame(g.ints(70, name="i"), g.strings(70, name="s", edges=False))
    return [
        csv_case("skip_rows_after_header", THREE, skip_rows_after_header=1),
        csv_case("past_the_last_row", THREE, skip_rows_after_header=5),
        csv_case("after_skip_rows", THREE, skip_rows=1, skip_rows_after_header=1),
        csv_case("blank_line_one", "a,b\n1,2\n\n3,4\n", skip_rows_after_header=1),
        csv_case("blank_line_skipped", "a,b\n\n1,2\n3,4\n", skip_rows_after_header=1),
        csv_case("blank_line_skipped_text_after", "a,b\n\nx,2\n3,4\n", skip_rows_after_header=1),
        csv_case("quoted_record_one", "a,b\n\"x\ny\",2\n3,4\n", skip_rows_after_header=1),
        csv_case("no_header", "1,2\n3,4\n5,6\n", has_header=False, skip_rows_after_header=2),
        csv_case("no_header_one", THREE, has_header=False, skip_rows_after_header=1),
        csv_case("skip_lines", "junk \"q\nmore\na,b\n1,2\n", skip_lines=2),
        csv_case("comment_line", "#c\na,b\n1,2\n", skip_lines=1),
        csv_case("crlf", "junk\r\na,b\r\n1,2\r\n", skip_lines=1),
        csv_case("skip_lines_no_header", "junk\n1,2\n3,4\n", skip_lines=1, has_header=False),
        csv_case("skip_lines_and_after_header", "junk\na,b\n1,2\n3,4\n", skip_lines=1, skip_rows_after_header=1),
        csv_case("no_quote", "a,b\n\"x,1\n", quote_char=None),
        csv_case("no_quote_quotes_kept", "a,b\n\"x\",\"y\"\n", quote_char=None),
        csv_case("no_quote_line_end_in_quotes", "a,b\n\"x\ny\",2\n", quote_char=None),
        csv_case("no_quote_empty_pair", "a,b\n\"\",2\n,3\n", quote_char=None),
        csv_case("no_quote_doubled_quotes", "a,b\n\"say \"\"hi\"\"\",2\n", quote_char=None),
        csv_case("no_quote_separator_in_quotes", "a,b,c\n\"x,y\",2\n", quote_char=None),
        csv_case("no_quote_number_in_quotes", "a,b\n\"1\",2\n", quote_char=None),
        twice("two_files_rows", skip_rows_after_header=2),
        twice("two_files_lines", skip_lines=1, has_header=False),
        # --- refused, as Polars refuses them -----------------------------------------------
        csv_case("skip_rows_and_skip_lines", THREE, skip_rows=1, skip_lines=1, contains="WithSkipLines"),
        csv_case("skip_lines_past_the_end", THREE, skip_lines=5, contains="WithRaiseIfEmpty"),
        # --- generated -------------------------------------------------------------------
        csv_case("skip_gen", "junk \"q\nmore\n" + gen.write_csv(), skip_lines=2, skip_rows_after_header=7),
        csv_case("no_quote_gen", plain.write_csv(), quote_char=None, note="plain text, which no quote touches"),
    ]
