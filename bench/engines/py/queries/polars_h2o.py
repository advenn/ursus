"""polars implementations of the h2o.ai db-benchmark queries.

Each function takes a `scan(table_name) -> LazyFrame` and returns a LazyFrame.
Column names of the answer are part of the contract: the checksum lists in
config/suites.toml refer to them by name.
"""

from __future__ import annotations

from typing import Callable

import polars as pl

Scan = Callable[[str], "pl.LazyFrame"]


def gb1(scan: Scan):
    return scan("g1").group_by("id1").agg(pl.sum("v1"))


def gb2(scan: Scan):
    return scan("g1").group_by("id1", "id2").agg(pl.sum("v1"))


def gb3(scan: Scan):
    return scan("g1").group_by("id3").agg(pl.sum("v1"), pl.mean("v3"))


def gb4(scan: Scan):
    return scan("g1").group_by("id4").agg(pl.mean("v1"), pl.mean("v2"), pl.mean("v3"))


def gb5(scan: Scan):
    return scan("g1").group_by("id6").agg(pl.sum("v1"), pl.sum("v2"), pl.sum("v3"))


def gb6(scan: Scan):
    return scan("g1").group_by("id4", "id5").agg(
        pl.median("v3").alias("median_v3"),
        pl.std("v3").alias("sd_v3"),
    )


def gb7(scan: Scan):
    return scan("g1").group_by("id3").agg((pl.max("v1") - pl.min("v2")).alias("range_v1_v2"))


def gb8(scan: Scan):
    return (
        scan("g1")
        .drop_nulls("v3")
        .sort("v3", descending=True)
        .group_by("id6")
        .agg(pl.col("v3").head(2).alias("largest2_v3"))
        .explode("largest2_v3")
    )


def gb9(scan: Scan):
    return scan("g1").group_by("id2", "id4").agg((pl.corr("v1", "v2") ** 2).alias("r2"))


def gb10(scan: Scan):
    return (
        scan("g1")
        .group_by("id1", "id2", "id3", "id4", "id5", "id6")
        .agg(pl.sum("v3").alias("v3"), pl.len().alias("cnt"))
    )


def j1(scan: Scan):
    return scan("x").join(scan("small"), on="id1", how="inner")


def j2(scan: Scan):
    return scan("x").join(scan("medium"), on="id2", how="inner")


def j3(scan: Scan):
    return scan("x").join(scan("medium"), on="id2", how="left")


def j4(scan: Scan):
    return scan("x").join(scan("medium"), on="id5", how="inner")


def j5(scan: Scan):
    return scan("x").join(scan("big"), on="id3", how="inner")


QUERIES = {
    fn.__name__: fn
    for fn in (gb1, gb2, gb3, gb4, gb5, gb6, gb7, gb8, gb9, gb10, j1, j2, j3, j4, j5)
}
