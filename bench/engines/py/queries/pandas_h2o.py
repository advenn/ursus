"""pandas implementations of the h2o.ai db-benchmark queries.

Answer column names match the checksum lists in config/suites.toml.
"""

from __future__ import annotations

from typing import Callable

import numpy as np
import pandas as pd

Read = Callable[[str], "pd.DataFrame"]

COLUMNS: dict[str, dict[str, list[str]]] = {
    "gb1": {"g1": ["id1", "v1"]},
    "gb2": {"g1": ["id1", "id2", "v1"]},
    "gb3": {"g1": ["id3", "v1", "v3"]},
    "gb4": {"g1": ["id4", "v1", "v2", "v3"]},
    "gb5": {"g1": ["id6", "v1", "v2", "v3"]},
    "gb6": {"g1": ["id4", "id5", "v3"]},
    "gb7": {"g1": ["id3", "v1", "v2"]},
    "gb8": {"g1": ["id6", "v3"]},
    "gb9": {"g1": ["id2", "id4", "v1", "v2"]},
    "gb10": {"g1": ["id1", "id2", "id3", "id4", "id5", "id6", "v3"]},
}


def gb1(read: Read):
    return read("g1").groupby("id1", as_index=False, observed=True).agg(v1=("v1", "sum"))


def gb2(read: Read):
    return read("g1").groupby(["id1", "id2"], as_index=False, observed=True).agg(v1=("v1", "sum"))


def gb3(read: Read):
    return (
        read("g1")
        .groupby("id3", as_index=False, observed=True)
        .agg(v1=("v1", "sum"), v3=("v3", "mean"))
    )


def gb4(read: Read):
    return (
        read("g1")
        .groupby("id4", as_index=False, observed=True)
        .agg(v1=("v1", "mean"), v2=("v2", "mean"), v3=("v3", "mean"))
    )


def gb5(read: Read):
    return (
        read("g1")
        .groupby("id6", as_index=False, observed=True)
        .agg(v1=("v1", "sum"), v2=("v2", "sum"), v3=("v3", "sum"))
    )


def gb6(read: Read):
    return (
        read("g1")
        .groupby(["id4", "id5"], as_index=False, observed=True)
        .agg(median_v3=("v3", "median"), sd_v3=("v3", "std"))
    )


def gb7(read: Read):
    grouped = read("g1").groupby("id3", as_index=False, observed=True).agg(
        max_v1=("v1", "max"), min_v2=("v2", "min")
    )
    grouped["range_v1_v2"] = grouped["max_v1"] - grouped["min_v2"]
    return grouped[["id3", "range_v1_v2"]]


def gb8(read: Read):
    frame = read("g1").dropna(subset=["v3"])
    ranked = frame.sort_values("v3", ascending=False).groupby("id6", observed=True).head(2)
    return ranked[["id6", "v3"]].rename(columns={"v3": "largest2_v3"})


def gb9(read: Read):
    # corr() squared, computed from the raw moments so this is one pass over
    # each group rather than a python-level apply per group.
    frame = read("g1")
    frame = frame.assign(
        xy=frame["v1"] * frame["v2"],
        xx=frame["v1"] * frame["v1"],
        yy=frame["v2"] * frame["v2"],
    )
    g = frame.groupby(["id2", "id4"], as_index=False, observed=True).agg(
        n=("v1", "size"),
        sx=("v1", "sum"),
        sy=("v2", "sum"),
        sxy=("xy", "sum"),
        sxx=("xx", "sum"),
        syy=("yy", "sum"),
    )
    cov = g["n"] * g["sxy"] - g["sx"] * g["sy"]
    var_x = g["n"] * g["sxx"] - g["sx"] ** 2
    var_y = g["n"] * g["syy"] - g["sy"] ** 2
    denominator = var_x * var_y
    g["r2"] = np.where(denominator > 0, cov**2 / denominator, np.nan)
    return g[["id2", "id4", "r2"]]


def gb10(read: Read):
    keys = ["id1", "id2", "id3", "id4", "id5", "id6"]
    return (
        read("g1")
        .groupby(keys, as_index=False, observed=True)
        .agg(v3=("v3", "sum"), cnt=("v3", "size"))
    )


def _join(read: Read, right: str, on: str, how: str):
    return read("x").merge(read(right), on=on, how=how, suffixes=("", "_right"))


def j1(read: Read):
    return _join(read, "small", "id1", "inner")


def j2(read: Read):
    return _join(read, "medium", "id2", "inner")


def j3(read: Read):
    return _join(read, "medium", "id2", "left")


def j4(read: Read):
    return _join(read, "medium", "id5", "inner")


def j5(read: Read):
    return _join(read, "big", "id3", "inner")


QUERIES = {
    fn.__name__: fn
    for fn in (gb1, gb2, gb3, gb4, gb5, gb6, gb7, gb8, gb9, gb10, j1, j2, j3, j4, j5)
}
