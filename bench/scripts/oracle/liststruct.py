"""The liststruct group: list.to_struct with its fields named and struct.rename_fields
(liststruct_test.go, step 224), literal and generated.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col

TOSTRUCT = pl.DataFrame({"a": pl.Series("a", [[1, 2, 3], [4], [], None, [None, 5]], dtype=pl.List(pl.Int64))})
RENAME = pl.DataFrame({"s": pl.Series("s", [{"a": 1, "b": "x"}, None, {"a": None, "b": 'q"z'}],
                                      dtype=pl.Struct({"a": pl.Int64, "b": pl.String}))})


def sel(*exprs: pl.Expr):
    return lambda df: df.select(*exprs)


def cases() -> list[Case]:
    g = Gen(4996)
    n = 120
    gen_lists = g.frame(g.lists(n, g.ints(n, pl.Int64, lo=-20, hi=20, name="e"), maxlen=4, name="a"))
    ints, words = g.ints(n, pl.Int64, lo=-100, hi=100, name="a"), g.strings(n, name="b")
    rows = [None if i % 9 == 0 else {"a": a, "b": b} for i, (a, b) in enumerate(zip(ints.to_list(), words.to_list()))]
    gen_struct = g.frame(pl.Series("s", rows, dtype=pl.Struct({"a": pl.Int64, "b": pl.String})))
    return [
        case("to_struct", TOSTRUCT, sel(c("a").list.to_struct(["x", "y"])),
             note="field i is element i, null where the list is shorter, the rest dropped; a null list is a null struct"),
        case("rename_fields", RENAME, sel(c("s").struct.rename_fields(["p", "q"]))),
        case("rename_then_field", RENAME, sel(c("s").struct.rename_fields(["p", "q"]).struct.field("q"))),
        case("to_struct_gen", gen_lists, sel(c("a").list.to_struct(["x", "y", "z"]))),
        case("to_struct_one_gen", gen_lists, sel(c("a").list.to_struct(["only"]))),
        case("rename_fields_gen", gen_struct, sel(c("s").struct.rename_fields(["p", "q"]))),
    ]
