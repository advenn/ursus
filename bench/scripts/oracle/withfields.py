"""The withfields group: struct.with_fields (withfields_test.go, steps 171 and 275): a
field an expression names replaced in place, the rest added after, a field read named
after its field.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
P = pl.col("p").struct

PEOPLE = pl.DataFrame({
    "age": pl.Series("age", [30, 40, None], dtype=pl.Int64), "name": ["ann", "bo", "cy"], "city": ["x", "y", "z"],
}).select(pl.struct("age", "name").alias("p"), "city")
TWO = pl.DataFrame({"age": pl.Series("age", [30, 40], dtype=pl.Int64), "name": ["ann", "bo"]}).select(pl.struct("age", "name").alias("p"))


def sel(*exprs: pl.Expr):
    return lambda df: df.select(*exprs)


def cases() -> list[Case]:
    g = Gen(4998)
    n = 120
    gen = g.frame(g.ints(n, pl.Int64, lo=0, hi=99, name="age"), g.strings(n, name="name"), g.strings(n, alphabet="xyz", maxlen=1, name="city")
                  ).select(pl.struct("age", "name").alias("p"), "city")
    return [
        case("with_fields", PEOPLE, sel(P.with_fields(pl.field("age") + 1, c("city"), pl.lit("fixed").alias("tag"))),
             note="age replaced in place, name kept, city and tag added after; a null age plus one is null"),
        case("names_a_field_as_planned", TWO, sel(P.with_fields(pl.field("name")).alias("q")),
             note="a Field read is named after its field, so with_fields of name sets name itself"),
        case("with_fields_gen", gen, sel(P.with_fields(pl.field("age") * 2, c("city"), pl.field("name").str.to_uppercase().alias("shout")))),
    ]
