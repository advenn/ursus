"""The ndjsonread group: read_ndjson, the types its lines meet, a schema given, errors
ignored and several files (ndjsonread_test.go, step 334), each Polars 2.1.0's answer.

The input is the param ndjson, read under the param options, or the param files for
several; a schema refusal names its line too. Not here, with the reason: a file read
in batches with its columns pushed down, and a long line, which check the plan and the
blocks; and TestReadNDJSONInParts, a Go differential against encoding/json.
"""

from __future__ import annotations

import polars as pl

from . import Case
from .gen import Gen
from .iotext import textfiles, with_options

SCHEMA = {"i": pl.Int32, "u": pl.UInt64, "f": pl.Float32, "c": pl.Enum(["a", "2024-01-02"])}
ONE = {"infer_schema_length": 1, "ignore_errors": True}


def nd(name: str, text: str, note: str = "", contains: str | list[str] = "", **kw) -> Case:
    b = text.encode()
    return Case(name, {}, lambda: pl.read_ndjson(b, **kw), params=with_options({"ndjson": text}, kw), note=note, contains=contains)


def cases() -> list[Case]:
    g = Gen(5025)
    ints = g.ints(100, name="i")
    gen = g.frame(ints, g.floats(100, name="f"), g.strings(100, name="s"), g.bools(100),
                  g.lists(100, g.ints(40, name="e"), name="l"),
                  pl.DataFrame([ints.alias("x"), g.strings(100, name="y", edges=False)]).to_struct("o"))
    return [
        nd("a_column_a_key", '{"a":1,"b":"x","c":1.5}\n{"a":2,"b":null,"c":2}\n{"a":null,"b":"z"}\n{"a":4,"b":"w","c":3.25,"d":true}\n'),
        nd("keys_in_the_order_first_seen", '{"b":1,"a":2}\n{"a":3,"c":4}\n{"a":5,"a":6}\n', note="a key twice, its first value"),
        # --- the types met -------------------------------------------------------------------
        nd("integer_then_float", '{"a":1}\n{"a":2.5}\n'),
        nd("integer_and_text", '{"a":1}\n{"a":"x"}\n'),
        nd("boolean_and_text", '{"a":true}\n{"a":"x"}\n'),
        nd("only_nulls", '{"a":null}\n{"a":null}\n'),
        nd("null_then_integer", '{"a":null}\n{"a":1}\n'),
        nd("past_int64", '{"a":9223372036854775807}\n{"a":18446744073709551615}\n'),
        nd("exponent", '{"a":1e3}\n'),
        nd("list_of_integers_and_a_float", '{"a":[1,2.5]}\n'),
        nd("list_of_integers_and_text", '{"a":[1,"x"]}\n'),
        nd("list_of_objects", '{"a":[{"x":1},{"y":"z"}]}\n'),
        nd("two_objects", '{"s":{"x":1}}\n{"s":{"y":"q"}}\n'),
        nd("list_and_text", '{"a":"s"}\n{"a":[1]}\n'),
        nd("text_and_object", '{"a":{"x":1}}\n{"a":"s"}\n'),
        nd("elements_float_and_object", '{"a":[1.0,{"b":"q"}]}'),
        nd("elements_float_boolean_integer", '{"a":[2.5,true,1]}'),
        nd("elements_object_and_number", '{"a":[{"x":1},1]}'),
        nd("elements_objects", '{"a":[{"y":1},{"x":true},{"y":"s","z":null}]}'),
        nd("elements_past_int64_and_integer", '{"a":[18446744073709551615,1]}'),
        nd("elements_past_int64_alone", '{"a":[18446744073709551615]}'),
        nd("elements_empty_list_and_list", '{"a":[[],[1]]}'),
        nd("text_of_numbers", '{"a":1}\n{"a":"x"}\n{"a":2.5}\n{"a":true}\n', note="the text of a number beside text, as Polars writes it"),
        nd("text_as_rust_prints", '{"a":"s"}\n{"a":1.50}\n{"a":1e3}\n{"a":-0}\n{"a":1E-7}\n{"a":-0.0}\n'
           '{"a":[1.0,{"b":"q\\"\\n\\t\\\\"},null,true]}\n{"a":{"k\\"":[]}}\n{"a":12345678901234567890}\n{"a":  [ [ ] ,  { } ] }\n',
           note="an array or an object as JSON of Polars' spacing, its strings' quotes, newlines and carriage returns escaped"),
        nd("elements_as_text", '{"a":[1.0,{"b":"q"}]}\n{"a":[2.5,true,1]}\n'),
        nd("list_rows", '{"a":[1,2.5]}\n{"a":[]}\n{"a":null}\n'),
        nd("struct_rows", '{"s":{"x":1}}\n{"s":{"y":"q"}}\n{"s":null}\n'),
        nd("text_escapes", '{"a":"\\u00e9\\ud83d\\ude00","b":"a\\"b\\\\c\\n"}\n  {"a" : "x" , "b":"y" }  \r\n\n'),
        # --- refused -------------------------------------------------------------------------
        nd("boolean_and_number_refused", '{"a":true}\n{"a":1}\n', contains="no type in common"),
        nd("object_and_number_refused", '{"s":{"x":1}}\n{"s":5}\n', contains="no type in common"),
        nd("later_lines_type_refused", '{"a":1}\n{"a":"x"}\n', infer_schema_length=1, contains="cannot parse 'x' (a string) as Int64"),
        nd("line_not_an_object_refused", '[1,2]\n', contains="holds an array"),
        nd("malformed_line_refused", '{"a":1}\n{"a":\n', contains="malformed JSON"),
        nd("leading_zero_refused", '{"a":01}\n', contains="leading zero"),
        nd("leading_zero_in_floats_refused", '{"a":1.5}\n{"a":-01.5}\n', infer_schema_length=1, contains="leading zero"),
        nd("keys_types_by_line_refused", '{"a":1}\n\n{"a":true}\n', contains="at line 3, key a"),
        nd("lines_place_past_a_blank_refused", '{"a":1}\n \n{"a":"x"}\n', infer_schema_length=1, contains="at line 3, key a"),
        nd("boolean_among_numbers_refused", '{"a":[1,true,2.5]}', contains="cannot parse 'true' (a Boolean) as Float64"),
        # --- a schema given ------------------------------------------------------------------
        nd("schema_given", '{"a":1,"b":"x","d":"2024-01-02"}\n{"a":2.5,"d":null}\n', schema={"a": pl.Float64, "d": pl.Date}),
        nd("schema_of_each_type", '{"i":1,"u":1,"f":1,"c":"a"}\n{"i":300,"u":18446744073709551615,"f":300,"c":"2024-01-02"}\n', schema=SCHEMA),
        nd("schema_refuses_text_as_int32", '{"i":1}\n{"i":"a"}\n', schema=SCHEMA, contains=["cannot parse 'a' (a string) as Int32", "at line 2"]),
        nd("schema_refuses_past_int32", '{"i":1}\n{"i":3000000000}\n', schema=SCHEMA, contains=["cannot parse '3000000000' (a number) as Int32", "at line 2"]),
        nd("schema_refuses_negative_uint64", '{"i":1}\n{"u":-1}\n', schema=SCHEMA, contains=["cannot parse '-1' (a number) as Uint64", "at line 2"]),
        # --- errors ignored ------------------------------------------------------------------
        nd("ignored_in_integers", '{"a":1}\n{"a":"x"}\n{"a":2.5}\n{"a":3}\n{"a":[1]}\n', **ONE),
        nd("ignored_in_booleans", '{"b":true}\n{"b":1}\n{"b":"t"}\n', **ONE),
        nd("ignored_in_a_list", '{"a":[1,2]}\n{"a":[1,"x",{"y":1}]}\n{"a":"s"}\n', **ONE),
        nd("ignored_in_a_struct", '{"o":{"x":1,"y":"q"}}\n{"o":5}\n{"o":null}\n{"o":{"x":"bad","y":"r"}}\n', **ONE,
           note="a value that is not an object, in a Struct column, is null"),
        nd("ignored_under_a_schema", '{"d":"2024-01-02"}\n{"d":"nope"}\n', schema={"d": pl.Date}, ignore_errors=True),
        nd("malformed_refused_still", '{"a":1}\n{"a":\n', contains="malformed JSON", **ONE),
        # --- several files -------------------------------------------------------------------
        textfiles("two_files", {"a.ndjson": '{"a":1,"b":"x"}\n{"a":2,"b":"y"}\n', "b.ndjson": '{"a":3,"b":"z","c":true}\n{"b":"w","a":4}\n'},
                  lambda r: pl.read_ndjson([str(r / "a.ndjson"), str(r / "b.ndjson")]),
                  note="the first file's schema; a key a later file adds is passed over"),
        # --- generated -----------------------------------------------------------------------
        nd("round_trip_gen", gen.write_ndjson(), note="Polars' own NDJSON of a generated frame, read back; NaN was written null"),
    ]
