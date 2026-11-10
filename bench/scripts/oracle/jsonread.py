"""The jsonread group: read_json, a document of an array of objects or of one object,
its refusals, a key past the inferred objects, schema overrides, and write_json's
document read back (jsonread_test.go, step 345), each Polars 2.1.0's answer.

The input is the param json, read under the param options, its select and tail what
the case asks of the frame; a refusal names the reader too. Not here, with the reason:
a document of no object, a frame of no columns, which the format does not hold; a
later true or 1.5 in an Int64 column, which Polars reads as 1 and ursus refuses
(feature-gaps.md §9); a file and its plan; and TestReadJSONInParts, a Go differential
against the NDJSON of the same objects.
"""

from __future__ import annotations

import json

import polars as pl

from . import Case, case
from .gen import Gen
from .iotext import shape_params, shaped, with_options

TWO = '[{"a":1,"b":"x"},{"a":2,"c":true}]'


def phrases(contains: str | list[str], reader: str) -> str | list[str]:
    """What a refusal must name, and the reader's name beside it, as the old test's
    refused did."""
    if not contains:
        return ""
    return ([contains] if isinstance(contains, str) else contains) + [reader]


def rj(name: str, doc: str, note: str = "", contains: str | list[str] = "", select: str | None = None,
       tail: int | None = None, **kw) -> Case:
    b = doc.encode()
    return Case(name, {}, lambda: shaped(pl.read_json(b, **kw), select=select, tail=tail),
                params=with_options({"json": doc, **shape_params(select=select, tail=tail)}, kw), note=note,
                contains=phrases(contains, "read_json"))


def nd(name: str, text: str, contains: str = "", **kw) -> Case:
    b = text.encode()
    return Case(name, {}, lambda: pl.read_ndjson(b, **kw), params=with_options({"ndjson": text}, kw),
                contains=phrases(contains, "read_ndjson"))


def late(last: str) -> str:
    """150 objects of a, then one more: past the first 100, the schema's."""
    return "[" + '{"a":1},' * 150 + last + "]"


def cases() -> list[Case]:
    g = Gen(5026)
    ints = g.ints(100, name="i")
    gen = g.frame(ints, g.floats(100, name="f", edges=False), g.strings(100, name="s"), g.bools(100),
                  g.lists(100, g.ints(40, name="e"), name="l"),
                  pl.DataFrame([ints.alias("x"), g.strings(100, name="y", edges=False)]).to_struct("o"))
    doc = gen.write_json()
    pretty = json.dumps(json.loads(doc), indent=2, ensure_ascii=False)
    refused = [
        ("empty_document_refused", "", "the document is empty"),
        ("a_number_refused", "3", ["can only read JSON objects as rows", "starts with '3'"]),
        ("arrays_refused", "[[1,2],[3]]", "element 1 of the array starts with '['"),
        ("a_number_among_objects_refused", '[{"a":1},2]', "element 2 of the array starts with '2'"),
        ("a_null_among_objects_refused", '[{"a":1},null]', "element 2 of the array is null"),
        ("a_trailing_comma_refused", '[{"a":1},]', "a comma after object 1"),
        ("text_after_the_end_refused", '[{"a":1}] x', "text after the document's end"),
        ("two_documents_refused", '{"a":1}{"a":2}', "text after the document's end"),
        ("an_unclosed_array_refused", '[{"a":1}', "ends inside its array"),
        ("an_unclosed_object_refused", '[{"a":1', "ends inside object 1"),
        ("a_malformed_object_refused", '[{"a":1},{"a":}]', ["malformed JSON", "object 2"]),
    ]
    return [
        rj("an_array_of_objects", TWO, note="a column a key, in the order first seen, a key missing a null"),
        rj("spread_over_lines", "[\n  {\n    \"a\": 1,\n    \"b\": \"x\"\n  }\n  ,\n  {\"a\":\n 2}\n]\n"),
        rj("a_byte_order_mark", "﻿" + '[{"a":1}]'),
        rj("a_key_twice", '[{"a":1,"a":2}]', note="its first value"),
        rj("text_beside_a_number", '[{"a":1},{"a":"x"}]'),
        rj("an_empty_object_first", '[{},{"a":1}]'),
        rj("one_column_alone", TWO, select="c", note="the other keys passed over"),
        rj("one_object_one_row", '{"a":[1,2],"b":{"x":1}}'),
        *[rj(name, doc_, contains=c) for name, doc_, c in refused],
        rj("a_key_past_the_inferred_objects_refused", late('{"a":1,"b":2}'),
           contains=['key "b" is not in the schema', "object 151", "WithInferSchemaLines"]),
        rj("a_key_past_the_inferred_objects_in_a_struct_refused", "[" + '{"s":{"a":1}},' * 150 + '{"s":{"a":1,"b":2}}]',
           contains=['key "b" is not in the schema', "object 151, key s"]),
        rj("a_key_inferred_from_every_object", late('{"a":1,"b":2}'), infer_schema_length=None, tail=2),
        rj("a_key_a_given_schema_skips", late('{"a":7,"b":2}'), schema={"a": pl.Int64}, tail=1),
        rj("a_late_text_refused", late('{"a":"x"}'), contains=["cannot parse 'x' (a string) as Int64", "object 151, key a"]),
        # --- schema overrides ---------------------------------------------------------------
        rj("override_float32", TWO, schema_overrides={"a": pl.Float32}),
        rj("override_string", TWO, schema_overrides={"a": pl.String}, select="a"),
        rj("override_date", '[{"d":"2024-01-02"}]', schema_overrides={"d": pl.Date}),
        rj("override_nested", '[{"l":[1,2],"s":{"x":1}}]', schema_overrides={"l": pl.List(pl.Float64), "s": pl.Struct({"x": pl.Float64})}),
        rj("override_over_a_schema", TWO, schema={"a": pl.Int64, "b": pl.String}, schema_overrides={"a": pl.Float64}),
        rj("override_of_no_column_refused", TWO, schema_overrides={"z": pl.Int64}, contains='unknown column "z"'),
        nd("ndjson_override", '{"a":1}\n{"a":2,"b":"x"}\n', schema_overrides={"a": pl.Int32}),
        nd("ndjson_override_of_no_column_refused", '{"a":1}\n', schema_overrides={"z": pl.Int32}, contains='unknown column "z"'),
        # --- write_json read back -----------------------------------------------------------
        case("write_json_read_back", pl.DataFrame({"a": [1, None, 3], "b": ["x", 'y\n"z"', None], "f": [1.5, None, -0.25]}),
             lambda df: pl.read_json(df.write_json().encode())),
        # --- generated -----------------------------------------------------------------------
        rj("round_trip_gen", doc, note="Polars' own document of a generated frame, read back"),
        rj("pretty_gen", pretty, note="the same objects, spread over lines"),
    ]
