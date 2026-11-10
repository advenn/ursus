"""The update group: Polars' DataFrame.update, other's values over the frame's in the
columns both have (update_test.go, J2 of v0.6.7-scope.md, step 316). Polars' update
keeps the left order by default (maintain_order="left"), as ursus's does; the
right-order case passes "right". Not here: the refusals, the eager differential and
the spilled update.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
DF = pl.DataFrame({"A": [1, 2, 3, 4], "B": [400, 500, 600, 700]})
BY_PLACE = pl.DataFrame({"B": [-66, None, -99], "C": [5, 3, 1]})
BY_KEY = pl.DataFrame({"A": [3, 1, 5], "B": [-66, None, -99]})


TAIL = ("a full update appends the keys only one side has in no promised order in Polars, where ursus keeps that side's "
        "order: the appended rows sorted by key, which is that side's order on these inputs")


def upd(**o):
    def q(df: pl.DataFrame, other: pl.DataFrame) -> pl.DataFrame:
        out = df.update(other, **o)
        if o.get("how") != "full":
            return out
        # The side whose order maintain_order does not promise: other's keys under
        # the left order, the frame's under the right.
        key = o.get("on") or o.get("left_on")
        held = df[key] if o.get("maintain_order", "left") == "left" else other[o.get("right_on") or key]
        tail = c(key).is_in(held).not_()
        return out.with_row_index("__i").sort(tail, pl.when(tail).then(c(key)).otherwise(None), "__i").drop("__i")
    return q


def two(name: str, df: pl.DataFrame, other: pl.DataFrame, **o) -> Case:
    note = o.pop("note", "")
    if o.get("how") == "full":
        note = (note + "; " if note else "") + TAIL
    return case(name, {"df": df, "other": other}, upd(**o), note=note)


def cases() -> list[Case]:
    g = Gen(5020)
    keys = pl.Series("k", sorted(set(g.rng.choice(300, size=120, replace=False).tolist())), dtype=pl.Int64)
    frame = pl.DataFrame([keys, pl.Series("v", range(120), dtype=pl.Int64), g.floats(120, name="f")]).sample(fraction=1, shuffle=True, seed=4)
    okeys = pl.Series("k", sorted(set(g.rng.choice(400, size=60, replace=False).tolist())), dtype=pl.Int64)
    other = pl.DataFrame([okeys, g.ints(60, pl.Int64, lo=-1000, hi=0, name="v"), g.floats(60, lo=0, hi=1, name="f")])
    i32 = pl.Int32
    return [
        two("by_place", DF, BY_PLACE, note="C is not the frame's, so it is left out; a null leaves 500"),
        two("by_place_nulls", DF, BY_PLACE, include_nulls=True),
        two("on_a", DF, BY_KEY, on="A"),
        two("on_a_inner", DF, BY_KEY, on="A", how="inner"),
        two("on_a_full", DF, BY_KEY, on="A", how="full"),
        two("on_a_full_nulls", DF, BY_KEY, on="A", how="full", include_nulls=True),
        two("on_a_full_right_order", DF, BY_KEY, on="A", how="full", maintain_order="right"),
        two("a_key_twice_in_other", DF, pl.DataFrame({"A": [1, 1], "B": [7, 8]}), on="A", note="a key other holds twice gives its row twice"),
        two("a_key_twice_in_the_frame", pl.DataFrame({"A": [1, 1, 2], "B": [1, 2, 3]}), pl.DataFrame({"A": [1], "B": [9]}), on="A"),
        two("a_float_over_an_integer", DF, pl.DataFrame({"A": [1], "B": [1.5]}), on="A", note="the type that holds both"),
        two("text_over_an_integer", DF, pl.DataFrame({"A": [1], "B": ["x"]}), on="A"),
        two("keys_named_apart", DF, pl.DataFrame({"AA": [2], "B": [0]}), left_on="A", right_on="AA"),
        two("no_column_in_common", DF, pl.DataFrame({"A": [2], "Z": [0]}), on="A"),
        two("int32_key_left", pl.DataFrame({"A": pl.Series([1, 2], dtype=i32), "B": [10, 10]}), pl.DataFrame({"A": [2, 3], "B": [20, 30]}), on="A",
            note="a key keeps the frame's type"),
        two("int32_key_full", pl.DataFrame({"A": pl.Series([1, 2], dtype=i32), "B": [10, 10]}), pl.DataFrame({"A": [2, 3], "B": [20, 30]}), on="A",
            how="full", note="but where other's keys are new rows"),
        two("by_place_other_shorter", DF, pl.DataFrame({"B": [1, 2]}), note="the rows past other are as they were"),
        two("update_gen_left", frame, other, on="k", note="120 keys in no order updated from 60, half of them new"),
        two("update_gen_full", frame, other, on="k", how="full"),
        two("update_gen_inner_nulls", frame, other.with_columns(pl.when(c("v") % 3 == 0).then(None).otherwise(c("v")).alias("v")), on="k",
            how="inner", include_nulls=True),
    ]
