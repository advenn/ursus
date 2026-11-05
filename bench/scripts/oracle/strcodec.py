"""The strcodec group: encode and decode, to_integer's base and dtype, replace's n,
concat_str past nulls, json_path_match and json_decode (strcodec_test.go, step 271). A
regex replaced more than once, which Polars refuses, is held to Go's regexp in the Go
file.
"""

from __future__ import annotations

import json

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
S = pl.col("s").str

TEXT = pl.DataFrame({"s": ["hi", "é", None, ""]})
HEXES = pl.DataFrame({"s": ["6869", "C3A9", None, "zz", "abc"]})
B64S = pl.DataFrame({"s": ["aGk=", "w6k=", None, "!!", "aGk"]})
TOINT = pl.DataFrame({"s": ["ff", "10", "-1a", "z", None, " 7", "0x10"], "b": pl.Series("b", [16, 2, 16, 36, 10, 10, 16], dtype=pl.Int64)})
REPL = pl.DataFrame({"s": ["aaa", "abab", None]})
AB = pl.DataFrame({"a": ["x", None, "z", None], "b": pl.Series("b", [1, 2, None, None], dtype=pl.Int64)})
JSON_DOC = '{"a": {"b": [1, 2, {"c": "x"}]}, "d": 3.5, "e": null, "f": true}'
JSONS = pl.DataFrame({"s": [JSON_DOC, '{"a": 1}', None, "not json"]})
OBJS = pl.DataFrame({"s": ['{"a": 1, "b": "x"}', '{}', '{"b": "y", "z": [1]}', None]})
LISTS = pl.DataFrame({"s": ["[1,2]", "[]", None, "[3.5]"]})
AGG = pl.DataFrame({"a": pl.Series("a", [1, 2, 3], dtype=pl.Int64), "b": pl.Series("b", [10, 20, 30], dtype=pl.Int64)})
STRUCT_AB = pl.Struct({"a": pl.Int64, "b": pl.String})


def one(v: str) -> pl.DataFrame:
    return pl.DataFrame({"s": [v]})


def sel(*exprs: pl.Expr):
    return lambda df: df.select(*exprs)


def strings(s: pl.DataFrame, name: str, *exprs: pl.Expr, **opts) -> Case:
    return case(name, s, sel(*exprs), **opts)


def cases() -> list[Case]:
    g = Gen(49913)
    n = 150
    text = g.frame(g.strings(n, name="s"))
    ints = g.ints(n, pl.Int64, lo=-300, hi=300, name="v")
    bases = g.ints(n, pl.Int64, lo=2, hi=36, null_rate=0, edges=False, name="b")
    digits = [None if v is None else (("-" if v < 0 else "") + format(abs(v), "x") if b == 16 else
                                      ("-" if v < 0 else "") + format(abs(v), "b") if b == 2 else str(v) if b == 10 else
                                      ("-" if v < 0 else "") + to_base(abs(v), b))
              for v, b in zip(ints.to_list(), bases.to_list())]
    to_int = g.frame(pl.Series("s", digits), bases)
    repl = g.frame(g.strings(n, alphabet="ab", maxlen=7, name="s"))
    arrays = g.frame(pl.Series("s", [None if r is None else json.dumps(r) for r in
                                     g.lists(n, g.ints(n, pl.Int64, lo=-1000, hi=1000, name="e"), maxlen=4).to_list()]))
    objects = g.frame(pl.Series("s", [None if a is None and b is None else json.dumps({"a": a, "b": b})
                                      for a, b in zip(g.ints(n, pl.Int64, lo=-9, hi=9, name="a").to_list(), g.strings(n, name="b").to_list())]))
    return [
        strings(TEXT, "encode", S.encode("hex").alias("hex"), S.encode("base64").alias("b64")),
        strings(HEXES, "decode_hex", S.decode("hex", strict=False), note="upper case hex reads, odd or foreign digits do not"),
        strings(B64S, "decode_base64", S.decode("base64", strict=False), note="base64 must be padded"),
        strings(TOINT, "to_integer_base_16", S.to_integer(base=16, strict=False), note="no space, 0x or underscore reads"),
        strings(TOINT, "to_integer_base_a_row", S.to_integer(base=c("b"), strict=False)),
        strings(pl.DataFrame({"s": ["127", "128"]}), "to_integer_int8", S.to_integer(dtype=pl.Int8, strict=False)),
        strings(pl.DataFrame({"s": ["-1", "200"]}), "to_integer_uint8", S.to_integer(dtype=pl.UInt8, strict=False)),
        strings(pl.DataFrame({"s": ["12", "+5", "1_000"]}), "to_integer_sign_not_underscore", S.to_integer(strict=False)),
        strings(REPL, "replace_n2", S.replace("a", "X", literal=True, n=2)),
        strings(REPL, "replace_n0", S.replace("a", "X", literal=True, n=0)),
        strings(REPL, "replace_n_minus_1", S.replace("a", "X", literal=True, n=-1), note="every match"),
        strings(REPL, "replace_regex_n1", S.replace("a+", "X", n=1)),
        strings(REPL, "replace_regex_group_n1", S.replace("(a)", "<$1>", n=1)),
        strings(AB, "concat_str_ignore_nulls", pl.concat_str([c("a"), c("b")], separator="-", ignore_nulls=True)),
        strings(AB, "concat_str_with_a_literal", pl.concat_str([c("b"), pl.lit("!")], separator="", ignore_nulls=True)),
        strings(JSONS, "json_path_match", *[S.json_path_match(p).alias(p) for p in
                                            ["$.d", "$.a.b[0]", "$.a.b[2].c", "$.a.b", "$.e", "$.f", "$.a", "$['d']", "$.zz", "$.a.b[-1]", "$..c"]]),
        strings(one('[1e3, 1e15, 1e16, 1.5e-7, 0.00001, 1e-6, -0, 2.50, 1E2, 12345678901234567890, -9223372036854775809, 100000000000000000000]'),
                "json_path_numbers", S.json_path_match("$"), note="numbers reprinted, an integer that fits 64 bits and -0 as written"),
        strings(one('{"a": 1, "b": 0, "a": 2}'), "json_path_repeated_key", S.json_path_match("$.a").alias("a"), S.json_path_match("$").alias("all"),
                note="of a repeated key the last value, in the first one's place"),
        strings(one('{"c": "q\\"x\\n\\u0001é/\\u003c"}'), "json_path_escapes", S.json_path_match("$"), note="only what JSON must is escaped"),
        strings(pl.DataFrame({"s": ['{"a": {"c": 2}, "c": 1}', '{"a": [{"c": 3}, {"c": 4}], "b": {"c": 5}}']}), "json_path_recursive",
                S.json_path_match("$..c"), note="$..c is the first c found"),
        strings(OBJS, "json_decode_struct", S.json_decode(STRUCT_AB).alias("d")),
        strings(LISTS, "json_decode_list", S.json_decode(pl.List(pl.Int64))),
        strings(one('[1.9, -1.9, true, 9223372036854775808, 1e30, -3]'), "json_decode_int64", S.json_decode(pl.List(pl.Int64)),
                note="numbers and Booleans as each other; a number past its type's range null"),
        strings(one('[300, -128]'), "json_decode_int8", S.json_decode(pl.List(pl.Int8))),
        strings(one('[-1, 255, 256]'), "json_decode_uint8", S.json_decode(pl.List(pl.UInt8))),
        strings(one('[18446744073709551615, 1.5, -1.5, -0.5, false]'), "json_decode_uint64", S.json_decode(pl.List(pl.UInt64))),
        strings(one('[1e300, true, 2]'), "json_decode_float32", S.json_decode(pl.List(pl.Float32))),
        strings(one('[true, 1.50, 1e16, -0, -0.0, 1.5e-7, 12345678901234567890123, "x"]'), "json_decode_string", S.json_decode(pl.List(pl.String)),
                note="numbers and Booleans as text"),
        strings(one("5"), "json_decode_scalar_as_list", S.json_decode(pl.List(pl.Int64))),
        strings(one("[1, [2, 3]]"), "json_decode_scalars_as_lists", S.json_decode(pl.List(pl.List(pl.Int64)))),
        strings(one('{"a": 1, "a": 2}'), "json_decode_repeated_key", S.json_decode(pl.Struct({"a": pl.Int64})), note="the first value"),
        strings(AGG, "implode_then_eval", c("a").implode().list.eval(pl.element() * 2)),
        strings(AGG, "fold_of_aggregates", pl.fold(pl.lit(0), lambda acc, x: acc + x, [c("a").sum(), c("b").max()]).alias("f"),
                note="a literal 0 has its Go value's type here, Int64, where Polars' is an Int32 (§9)"),
        # --- generated -----------------------------------------------------------------
        strings(text, "codec_gen", S.encode("hex").alias("hex"), S.encode("base64").alias("b64"),
                S.encode("hex").str.decode("hex").alias("hex_back"), S.encode("base64").str.decode("base64").alias("b64_back")),
        strings(to_int, "to_integer_gen", S.to_integer(base=c("b"), strict=False).alias("v"), S.to_integer(base=16, strict=False).alias("v16"),
                S.to_integer(base=c("b"), dtype=pl.Int16, strict=False).alias("v16bit"),
                note="each value written in its row's base; read in base 16 too, where a digit past f is null"),
        strings(repl, "replace_n_gen", S.replace("a", "X", literal=True, n=1).alias("n1"), S.replace("a", "X", literal=True, n=2).alias("n2"),
                S.replace("a", "X", literal=True, n=-1).alias("all"), S.replace("b+", "_", n=1).alias("re1"),
                pl.concat_str([c("s"), c("s").str.len_chars()], separator="/", ignore_nulls=True).alias("cat")),
        strings(arrays, "json_decode_gen", S.json_decode(pl.List(pl.Int64)).alias("l"), S.json_path_match("$[0]").alias("first")),
        strings(objects, "json_decode_struct_gen", S.json_decode(STRUCT_AB).alias("d"), S.json_path_match("$.b").alias("b")),
    ]


def to_base(v: int, b: int) -> str:
    digits = "0123456789abcdefghijklmnopqrstuvwxyz"
    if v == 0:
        return "0"
    out = ""
    while v:
        v, r = divmod(v, b)
        out = digits[r] + out
    return out
