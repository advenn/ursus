"""The expr_cat group: the categorical namespace on an Enum (expr_cat_test.go, step
273). Polars 2 removed get_categories; its cases are held to the type's own category
list in the Go file.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
S = pl.col("s").cat
FRUITS = pl.Enum(["apple", "banana", "éclair", ""])
FRUIT = pl.DataFrame({"s": pl.Series("s", ["banana", None, "éclair", "apple", "", "banana"], dtype=FRUITS),
                      "g": pl.Series("g", [0, 1, 0, 1, 0, 1], dtype=pl.Int64)})


def sel(*exprs: pl.Expr):
    return lambda df: df.select(*exprs)


def one(name: str, e: pl.Expr, **opts) -> Case:
    return case(name, FRUIT, sel(e.alias("r")), **opts)


def phys(k: int) -> Case:
    cats = pl.Enum([f"c{i}" for i in range(k)])
    return case(f"physical_of_{k}", pl.DataFrame({"s": [f"c{k - 1}"]}), sel(c("s").cast(cats).to_physical()),
                note="the index's width grows with the categories, as Polars' does")


def cases() -> list[Case]:
    g = Gen(49911)
    n = 150
    cats = pl.Enum(["", "a", "b", "ab", "ba", "aa", "bb", "é"])
    gen = g.frame(g.strings(n, alphabet="ab", maxlen=2, edges=False, name="s").cast(cats, strict=False))
    return [
        one("starts_with", S.starts_with("ba")), one("ends_with", S.ends_with("ir")), one("empty_prefix", S.starts_with("")),
        one("len_bytes", S.len_bytes()), one("len_chars", S.len_chars()),
        one("slice", S.slice(1, 3)), one("slice_from_end", S.slice(-2)), one("slice_to_end", S.slice(2)),
        one("physical", c("s").to_physical()),
        phys(255), phys(256),
        case("filter_physical_1", FRUIT, lambda df: df.head(4).filter(c("s").to_physical() == 1)),
        case("cat_gen", gen, sel(S.starts_with("a").alias("sw"), S.ends_with("b").alias("ew"), S.len_bytes().alias("lb"),
                                 S.len_chars().alias("lc"), S.slice(1, 1).alias("sl"), S.slice(-1).alias("last"),
                                 c("s").to_physical().alias("ph"))),
    ]
