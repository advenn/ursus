"""The strsplit group: str.split telling an empty string from a null (strsplit_test.go,
step 45), its list fed to the namespace, exploded, filtered and grouped by; extract_all;
pad_start, pad_end and zfill; then over generated text. The batch-size and stdlib
differentials stay in the Go file.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
S = pl.col("s").str

SPLIT = pl.DataFrame({"id": pl.Series("id", [0, 1, 2, 3, 4, 5], dtype=pl.Int64), "s": ["a,b", "", "x", "p,,q", "é,ü", None]})
DIGITS = pl.DataFrame({"s": ["a,b,c", "one1two22three", "no-digits"]})
PAD = pl.DataFrame({"s": ["5", "-5", "+5", "12345", "", "é"]})


def cases() -> list[Case]:
    g = Gen(49914)
    n = 150
    csv = g.frame(g.ints(n, pl.Int64, lo=0, hi=n, null_rate=0, edges=False, name="id"), g.strings(n, alphabet="ab,", maxlen=6, name="s"))
    digits = g.frame(g.strings(n, alphabet="ab12-", maxlen=8, name="s"))
    pad = g.frame(g.strings(n, alphabet="+-5é", maxlen=5, name="s"))
    return [
        case("split", SPLIT, lambda df: df.with_columns(S.split(",").alias("parts")),
             note="an empty string splits to [\"\"], not to the empty list; a null to a null list"),
        case("split_feeds_the_namespace", SPLIT, lambda df: df.with_columns(
            S.split(",").list.len().alias("n"), S.split(",").list.get(0, null_on_oob=True).alias("first"),
            S.split(",").list.sort().list.get(0, null_on_oob=True).alias("smallest")),
            note="the sorted first of p,,q is the empty part, a value and not a null"),
        case("split_then_explode", SPLIT, lambda df: df.with_columns(S.split(",").alias("part")).explode("part"),
             note="the null input explodes to a null row"),
        case("split_then_filter", SPLIT, lambda df: df.with_columns(S.split(",").alias("parts")).filter(c("id") > 2)),
        case("extract_all", DIGITS, lambda df: df.with_columns(S.extract_all(r"\d+").alias("nums")),
             note="no match is an empty list, not a null one"),
        case("pad_and_zfill", PAD, lambda df: df.with_columns(S.pad_start(4, "0").alias("ps"), S.pad_end(4, ".").alias("pe"), S.zfill(4).alias("z")),
             note="pad_start pads before the sign, zfill after it; runes, not bytes"),
        case("group_by_a_list", SPLIT, lambda df: df.with_columns(S.split(",").alias("parts")).group_by("parts", maintain_order=True).agg(pl.len().alias("n")),
             note="a count is a Uint64 here, Polars' a UInt32 (§9)"),
        # --- generated -----------------------------------------------------------------
        case("split_gen", csv, lambda df: df.with_columns(S.split(",").alias("parts"), S.split(",").list.len().alias("n"),
                                                          S.split(",").list.get(0, null_on_oob=True).alias("first"),
                                                          S.split(",").list.sort().list.get(-1, null_on_oob=True).alias("largest"))),
        case("split_explode_gen", csv, lambda df: df.with_columns(S.split(",").alias("part")).explode("part")),
        case("group_by_a_list_gen", csv, lambda df: df.with_columns(S.split(",").alias("parts")).group_by("parts").agg(
            pl.len().cast(pl.Int64).alias("n"), c("id").min().alias("first_id")), order="set",
             note="the count cast to the Int64 both agree on; the groups' order is unspecified"),
        case("extract_all_gen", digits, lambda df: df.with_columns(S.extract_all(r"\d+").alias("nums"), S.extract_all("a+").alias("as"))),
        case("pad_gen", pad, lambda df: df.with_columns(S.pad_start(4, "0").alias("ps"), S.pad_end(6, ".").alias("pe"), S.zfill(5).alias("z"),
                                                        S.pad_start(3, "é").alias("ps3"), S.zfill(2).alias("z2"))),
    ]
