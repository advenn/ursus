"""The emptystruct group: a Struct of no fields, Polars' Struct({}), which a JSON or NDJSON
object field holding {} reads as (step 518), each Polars 2.1.0's answer.

A struct of no fields still has rows: one per row of its frame, {} or null. ursus took
a struct's length from its first field, so a struct of none had no rows, and a read of
{"a":{}} failed with an internal error (CI's fuzzing of FuzzScanJSON, step 511).

The reads are the param ndjson or json, as ndjsonread and jsonread read them: {} alone,
beside other columns, beside an object of fields, inside a struct or a list, and null
or a missing key against {}. The questions after are asked of a generated frame with a
Struct() column: filter, head, tail and slice, sort, concat, joins carrying it, unnest,
a field that is not there, casts, is_null and the counts, group-by with it a value and
a key, unique, explode, and the writers, IPC read back and Parquet's refusal of a group
of no columns. A frame's display and AssertFrameEqual are the Go side's
(TestEmptyStructDifferential). Not here, with the reason: a cast to a Struct of other
fields, which Polars answers by filling or dropping fields and ursus refuses as it
refuses every cast between Struct types (dtype.CanCast); and struct.drop of every
field, refused by its doc.
"""

from __future__ import annotations

import io

import polars as pl

from . import Case, case
from .gen import Gen
from .iotext import with_options

c = pl.col
EMPTY = pl.Struct({})


def nd(name: str, text: str, note: str = "", contains: str | list[str] = "", **kw) -> Case:
    b = text.encode()
    return Case(name, {}, lambda: pl.read_ndjson(b, **kw), params=with_options({"ndjson": text}, kw), note=note, contains=contains)


def js(name: str, doc: str, note: str = "", contains: str | list[str] = "", **kw) -> Case:
    b = doc.encode()
    return Case(name, {}, lambda: pl.read_json(b, **kw), params=with_options({"json": doc}, kw), note=note, contains=contains)


def empties(g: Gen, n: int, null_rate: float = 0.25, name: str = "a") -> pl.Series:
    """n rows of a Struct of no fields, {} or, at the rate, null."""
    return pl.Series(name, [None if r < null_rate else {} for r in g.rng.random(n)], dtype=EMPTY)


def sel(*exprs: pl.Expr):
    return lambda df: df.select(*exprs)


def ipc_back(df: pl.DataFrame) -> pl.DataFrame:
    b = io.BytesIO()
    df.write_ipc(b)
    return pl.read_ipc(b.getvalue())


def ipc_stream_back(df: pl.DataFrame) -> pl.DataFrame:
    b = io.BytesIO()
    df.write_ipc_stream(b)
    return pl.read_ipc_stream(b.getvalue())


def parquet_back(df: pl.DataFrame) -> pl.DataFrame:
    b = io.BytesIO()
    df.write_parquet(b)
    return pl.read_parquet(b.getvalue())


def lines(g: Gen, n: int) -> str:
    """n NDJSON lines of an id, and a key a that is {}, null or missing, each a third."""
    out = []
    for i, r in enumerate(g.rng.random(n)):
        a = '"a":{},' if r < 1 / 3 else '"a":null,' if r < 2 / 3 else ""
        out.append("{" + a + f'"i":{i}' + "}\n")
    return "".join(out)


def cases() -> list[Case]:
    g = Gen(5180)
    n = 40
    df = g.frame(pl.Series("i", range(n), dtype=pl.Int64), empties(g, n), g.ints(n, lo=-50, hi=50, name="b"),
                 g.strings(n, alphabet="xyz", maxlen=1, edges=False, name="k"))
    other = pl.DataFrame({"k": ["x", "y", "w"], "v": pl.Series("v", [10, 20, 30], dtype=pl.Int64)})
    one = pl.DataFrame([pl.Series("s", [{"x": 1}, None, {"x": 3}], dtype=pl.Struct({"x": pl.Int64}))])
    nested = pl.DataFrame([pl.Series("i", [0, 1, 2], dtype=pl.Int64),
                           pl.Series("s", [{"e": {}, "n": 1}, None, {"e": None, "n": 3}],
                                     dtype=pl.Struct({"e": EMPTY, "n": pl.Int64}))])
    listed = pl.DataFrame([pl.Series("i", [0, 1, 2], dtype=pl.Int64),
                           pl.Series("l", [[{}, None], None, [{}]], dtype=pl.List(EMPTY))])
    return [
        # --- read from JSON and NDJSON ---------------------------------------------------------
        nd("nd_alone", '{"a":{}}\n{"a":{}}\n'),
        nd("nd_beside", '{"a":{},"b":1}\n{"a":{},"b":2}\n'),
        js("json_alone", '[{"a":{}}]'),
        js("json_beside", '[{"a":{},"b":1}]'),
        js("json_unnamed_key", '[{"":{}}]', note="CI's fuzz input (internal/readfuzz's empty-object-field)"),
        js("json_one_object", '{"a":{},"b":"x"}'),
        nd("nd_fields_then_none", '{"a":{"x":1}}\n{"a":{}}\n', note="{} beside an object of fields is that struct, its fields null"),
        nd("nd_none_then_fields", '{"a":{}}\n{"a":{"x":1}}\n'),
        js("json_fields_then_none", '[{"a":{"x":1},"b":1},{"a":{},"b":2}]'),
        nd("nd_inside_a_struct", '{"s":{"e":{},"n":1}}\n{"s":{"e":{},"n":2}}\n'),
        nd("nd_alone_inside_a_struct", '{"s":{"e":{}}}\n{"s":{"e":{}}}\n{"s":{"e":null}}\n'),
        nd("nd_inside_a_list", '{"l":[{},{}]}\n{"l":[]}\n{"l":null}\n{"l":[{}]}\n'),
        js("json_inside_a_list", '[{"l":[{}],"b":1},{"l":[{},{},{}],"b":2}]'),
        nd("nd_null_then_none", '{"a":null}\n{"a":{}}\n'),
        nd("nd_none_then_null", '{"a":{}}\n{"a":null}\n'),
        nd("nd_key_missing", '{"a":{},"b":1}\n{"b":2}\n{"a":{},"b":3}\n'),
        js("json_null_and_missing", '[{"a":{},"b":1},{"a":null,"b":2},{"b":3}]'),
        nd("nd_schema_given", '{"a":{},"b":1}\n{"a":null,"b":2}\n', schema={"a": EMPTY, "b": pl.Int64}),
        nd("nd_lines_gen", lines(g, 700), note="many lines: {}, null and a missing key, a third each"),
        # --- a frame holding a Struct() column -------------------------------------------------
        case("filter", df, lambda df: df.filter(c("b") > 0)),
        case("filter_on_it", df, lambda df: df.filter(c("a").is_not_null())),
        case("head", df, lambda df: df.head(5)),
        case("tail", df, lambda df: df.tail(4)),
        case("slice", df, lambda df: df.slice(3, 7)),
        case("select_alone", df, sel("a"), note="a frame of the struct alone keeps its rows"),
        case("sort", df, lambda df: df.sort("b", "i", nulls_last=True)),
        case("sort_descending", df, lambda df: df.sort("b", "i", descending=True, nulls_last=True)),
        case("reverse", df, lambda df: df.reverse()),
        case("concat", df, lambda df: pl.concat([df, df.head(3)])),
        case("concat_alone", df, lambda df: pl.concat([df.select("a"), df.select("a").tail(2)])),
        case("join_inner", {"df": df, "other": other}, lambda df, other: df.join(other, on="k").sort("i")),
        case("join_left", {"df": df, "other": other}, lambda df, other: other.join(df, on="k", how="left").sort("v", "i", nulls_last=True),
             note="w matches no row: its struct is null"),
        case("unnest", df, lambda df: df.unnest("a"), note="no fields, so no columns"),
        case("unnest_expr", df, sel(c("a").struct.unnest(), c("b"))),
        case("field_refused", df, sel(c("a").struct.field("x")), contains='"x"'),
        case("cast_to_itself", df, sel(c("a").cast(EMPTY), c("i"))),
        case("cast_to_string_refused", df, sel(c("a").cast(pl.String)), contains="cannot cast Struct() to String"),
        case("is_null", df, sel(c("a").is_null(), c("a").is_not_null().alias("nn"))),
        case("counts", df, sel(c("a").null_count().cast(pl.Int64).alias("nulls"), c("a").count().cast(pl.Int64).alias("n"),
                               c("a").len().cast(pl.Int64).alias("len"))),
        case("first_last", df, sel(c("a").first(), c("a").last().alias("last"))),
        case("shift", df, sel(c("i"), c("a").shift(2))),
        case("gather", df, lambda df: df.gather([5, 0, 0, 3, -1])),
        case("implode", df, sel(c("a").implode())),
        case("drop_nulls", df, lambda df: df.drop_nulls("a")),
        case("group_value", df, lambda df: df.group_by("k").agg(c("a").first(), c("a").last().alias("last"),
                                                                c("a").count().cast(pl.Int64).alias("n")).sort("k"),
             note="k is never null"),
        case("group_value_list", df, lambda df: df.group_by("k").agg(c("a")).sort("k")),
        case("group_key", df, lambda df: df.group_by("a").agg(c("i").min().alias("lo"), c("i").max().alias("hi")).sort("lo"),
             note="{} one group, null another"),
        case("unique", df, lambda df: df.select("a").unique(maintain_order=True)),
        case("with_fields", df, sel(c("a").struct.with_fields(c("b").alias("z"))), note="a null struct stays null"),
        case("rename_fields", df, sel(c("a").struct.rename_fields([]))),
        case("match_schema_to_none", one, lambda df: df.match_to_schema({"s": EMPTY}, extra_struct_fields="ignore"),
             note="every field dropped: its rows and nulls kept"),
        case("match_schema_from_none", df, lambda df: df.select("a").match_to_schema({"a": pl.Struct({"x": pl.Int64})},
                                                                                     missing_struct_fields="insert")),
        case("explode", listed, lambda df: df.explode("l")),
        case("nested_field", nested, sel(c("s").struct.field("e"), c("i"))),
        case("nested_sort", nested, lambda df: df.sort("i", descending=True)),
        # --- written ---------------------------------------------------------------------------
        case("write_ndjson", df, lambda df: df.head(6).write_ndjson()),
        case("write_json", df, lambda df: df.head(6).write_json()),
        case("write_ndjson_nested", nested, lambda df: df.write_ndjson()),
        case("write_csv_refused", df, lambda df: df.write_csv(), contains="of type Struct() to CSV"),
        case("ipc", df, ipc_back),
        case("ipc_stream", df, ipc_stream_back),
        case("ipc_nested", nested, ipc_back),
        case("ipc_listed", listed, ipc_back),
        case("parquet_refused", df, parquet_back, contains="no fields",
             note="Parquet has no group of no columns; Polars says to add a dummy field"),
        case("parquet_nested_refused", nested, parquet_back, contains="no fields"),
        case("parquet_listed_refused", listed, parquet_back, contains="no fields"),
    ]
