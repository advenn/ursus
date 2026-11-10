"""The grouphead group: group_by().head(n) and tail(n) with maintain_order=True, the
keys first, the groups in the order each first appears, each group's rows in the
frame's order (grouphead_test.go, G1 of v0.6.7-scope.md, step 308). Not here: head(0),
a row of nulls a group in Polars and no rows here (feature-gaps.md §9), which the Go
test keeps as a pin; the keyless tail, the frame's own, a differential; the refusals,
the eager and spilled differentials.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
LF = pl.DataFrame({"v": [1, 2, 3, 4, 5, 6, 7], "k": ["b", "a", "b", "a", "b", None, "b"], "w": [10, 20, 30, 40, 50, 60, 70]})


def by(*keys):
    return lambda df: df.group_by(*keys, maintain_order=True)


def cases() -> list[Case]:
    g = Gen(5031)
    gen = g.frame(g.strings(120, alphabet="abcdefgh", maxlen=1, name="k"), pl.Series("v", range(120), dtype=pl.Int64), g.floats(120, name="w"))
    return [
        case("head_2", LF, lambda df: by("k")(df).head(2)),
        case("tail_2", LF, lambda df: by("k")(df).tail(2)),
        case("head_past_a_groups_rows", LF, lambda df: by("k")(df).head(10)),
        case("two_keys", LF, lambda df: by("k", "j")(df.with_columns((c("v") % 2).alias("j"))).head(1)),
        case("a_computed_key", LF, lambda df: by(c("v") % 3)(df).head(1), note="a key computed from v, named v, replaces it"),
        case("a_computed_key_aliased", LF, lambda df: by((c("v") % 3).alias("m"))(df).tail(1)),
        case("no_rows", LF.head(0), lambda df: by("k")(df).head(2)),
        case("head_gen", gen, lambda df: by("k")(df).head(3)),
        case("tail_gen", gen, lambda df: by("k")(df).tail(2)),
    ]
