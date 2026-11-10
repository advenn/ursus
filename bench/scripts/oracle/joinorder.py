"""The joinorder group: Polars' maintain_order for every kind of join (joinorder_test.go,
J1 of v0.6.7-scope.md, step 314). Each case is the left and right value columns alone,
so the key's place and type, which a right join lays out otherwise here
(feature-gaps.md §9), is not the question; the row order is, so every case is `order:
rows`. Polars' maintain_order promises one side's order and appends the other side's
unmatched rows after it in no promised order (its streaming engine gives another each
run), where ursus appends them in their own frame's order: each case sorts that tail
by its own value, which is its frame's order on these inputs, so the recording is
Polars' matched rows in Polars' order and the tail in the order ursus promises. Not
here: the validation and the plan, and the spilled joins, Go differentials.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
L = pl.DataFrame({"k": [3, 1, 2, 1, 9], "l": ["l0", "l1", "l2", "l3", "l4"]})
R = pl.DataFrame({"k": [1, 3, 1, 7, 3], "r": ["r0", "r1", "r2", "r3", "r4"]})
FL = pl.DataFrame({"k": [-0.0, 1.0], "l": ["a", "b"]})
FR = pl.DataFrame({"k": [0.0, 1.0], "r": ["x", "y"]})
KINDS = ["inner", "left", "right", "full"]
ORDERS = ["left", "right", "left_right", "right_left"]
TAIL = "the unmatched tail of the other side, which Polars appends in no promised order, sorted by its own value"


def stable(df: pl.DataFrame, order: str) -> pl.DataFrame:
    """The rows in their order, but the other side's unmatched tail (the right's under a
    left order, the left's under a right order) sorted by its own value column."""
    tail = c("l").is_null() if order in ("left", "left_right") else c("r").is_null()
    key = c("r") if order in ("left", "left_right") else c("l")
    return df.with_row_index("__i").sort(tail, pl.when(tail).then(key).otherwise(None), "__i").drop("__i")


def joined(how: str, order: str, *sel: str, **opts):
    sel = sel or ("l", "r")
    return lambda left, right: stable(left.join(right, on="k", how=how, maintain_order=order, **opts).select(*sel), order)


def cases() -> list[Case]:
    g = Gen(5012)
    gl = g.frame(g.ints(80, pl.Int64, lo=0, hi=30, edges=False, name="k"), pl.Series("l", range(80), dtype=pl.Int64))
    gr = g.frame(g.ints(90, pl.Int64, lo=10, hi=40, edges=False, name="k"), pl.Series("r", range(90), dtype=pl.Int64))
    out = [case(f"{how}_{order}", {"left": L, "right": R}, joined(how, order), note=TAIL) for how in KINDS for order in ORDERS]
    out += [case(f"{how}_{order}", {"left": L, "right": R},
                 lambda left, right, how=how, order=order: left.join(right, on="k", how=how, maintain_order=order).select("l"),
                 note="a semi or anti join keeps the left order, whatever is asked")
            for how in ["semi", "anti"] for order in ORDERS]
    out += [case(f"cross_{order}", {"left": L.head(2), "right": R.head(2)},
                 lambda left, right, order=order: left.join(right, how="cross", maintain_order=order).select("l", "r"))
            for order in ["left", "right"]]
    out += [case(f"right_key_{order}", {"left": L, "right": R}, joined("right", order, "k", "l", "r"),
                 note="the merged key on every row of a right join, the unmatched r3's 7 too; " + TAIL)
            for order in ORDERS]
    out += [case(f"full_coalesced_key_{order}", {"left": L, "right": R}, joined("full", order, "k", "l", "r", coalesce=True), note=TAIL)
            for order in ORDERS]
    out += [case(f"negative_zero_key_{how}", {"left": FL, "right": FR}, joined(how, "right", "k", "l", "r", coalesce=True),
                 note="the merged key is the left's -0.0, whatever the order")
            for how in ["inner", "left", "full"]]
    out += [case(f"gen_{how}_{order}", {"left": gl, "right": gr}, joined(how, order),
                 note="keys with nulls and repeats on both sides, a share unmatched; " + TAIL)
            for how in KINDS for order in ["left", "right"]]
    out += [case(f"gen_{how}", {"left": gl, "right": gr},
                 lambda left, right, how=how: left.join(right, on="k", how=how, maintain_order="left").select("l"))
            for how in ["semi", "anti"]]
    return out
