"""The structfields group: struct.field of several names, a regex or "*", a column a
field (structfields_test.go, step 275), on Polars' frame and over a generated struct.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
S = pl.col("s").struct

SF = pl.DataFrame({
    "a": pl.Series("a", [1, 3, None], dtype=pl.Int64), "ab": pl.Series("ab", [2, None, None], dtype=pl.Int64),
    "b": pl.Series("b", ["x", "y", None], dtype=pl.String), "c": pl.Series("c", [1.5, None, None], dtype=pl.Float64),
    "k": pl.Series("k", [1, 2, 3], dtype=pl.Int64),
}).select(pl.struct("a", "ab", "b", "c").alias("s"), "k")
AB = pl.DataFrame({"a": pl.Series("a", [1, 2], dtype=pl.Int64), "b": pl.Series("b", [10, 20], dtype=pl.Int64)})


def sel(*exprs: pl.Expr):
    return lambda df: df.select(*exprs)


def cases() -> list[Case]:
    g = Gen(4997)
    n = 120
    gen = g.frame(g.ints(n, pl.Int64, lo=-50, hi=50, name="a"), g.ints(n, pl.Int64, lo=0, hi=9, name="ab"),
                  g.strings(n, name="b"), g.floats(n, name="c"), g.ints(n, pl.Int64, lo=0, hi=3, null_rate=0, edges=False, name="k")
                  ).select(pl.struct("a", "ab", "b", "c").alias("s"), "k")
    return [
        case("two", SF, sel(S.field("b", "a")), note="in the struct's order, whatever the names'"),
        case("one", SF, sel(S.field("b"))),
        case("regex", SF, sel(S.field("^a.*$"))),
        case("regex_inside", SF, sel(S.field("^.b$"))),
        case("every_field", SF, sel(S.field("*"))),
        case("name_and_regex", SF, sel(S.field("c", "^a.*$"))),
        case("name_twice", SF, sel(S.field("a", "a"))),
        case("regex_and_its_match", SF, sel(S.field("^a.*$", "a"))),
        case("regex_of_none", SF, sel(c("k"), S.field("^z.*$"))),
        case("aggregate_each", SF, sel(S.field("a", "ab").sum())),
        case("arithmetic_each", SF, sel(S.field("a", "ab") * 2)),
        case("suffix_each", SF, sel(S.field("a", "ab").name.suffix("_x"))),
        case("unnest_suffix", SF, sel(S.unnest().name.suffix("_x"))),
        case("one_fields_name", SF, sel(S.field("c")), note="named after the field, not its struct"),
        case("with_columns", SF, lambda df: df.with_columns(S.field("a", "b")).select("k", "a", "b")),
        case("groups_first", SF, lambda df: df.group_by("k").agg(S.field("a", "ab").first()).sort("k")),
        case("fold_fields", AB, sel(pl.cum_fold(pl.lit(0), lambda acc, x: acc + x, [c("a"), c("b")]).struct.field("a", "b")),
             note="a literal 0 has its Go value's type here, Int64, where Polars' is an Int32 (§9)"),
        case("fields_gen", gen, sel(S.field("*"))),
        case("regex_gen", gen, sel(c("k"), S.field("^a.*$"), (S.field("c") * 2).alias("c2")),
             note="aliased: an expression over one field is named after the struct here and the field in Polars (§9)"),
        case("groups_first_gen", gen, lambda df: df.group_by("k").agg(S.field("a", "ab").first(), S.field("b").last().alias("b")).sort("k"),
             note="the last aliased, as regex_gen's"),
    ]
