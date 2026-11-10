"""The unstack group: Polars' unstack, vertical and horizontal, a column, a fill, and the
suffixes padded to the count's digits (unstack_test.go, tier 2 of v0.6.7-scope.md,
step 320). A Fill of one value for every column is Polars' scalar fill_values. Not
here: the frame of no rows, which Polars refuses and which is the grid of no values
here; the memory limit; the refusals.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

GRID = pl.DataFrame({"x": list("abcdefgh"), "y": list(range(8))})
XS = pl.DataFrame({"x": [1, 2, 3], "s": ["a", "b", "c"]})


def ints(n: int) -> pl.DataFrame:
    return pl.DataFrame({"x": pl.Series(list(range(n)), dtype=pl.Int64)})


def un(**o):
    return lambda df: df.unstack(**o)


def cases() -> list[Case]:
    g = Gen(5017)
    gen = g.frame(g.floats(100, name="f"), g.strings(100, name="s"))
    return [
        case("vertical", GRID, un(step=3)),
        case("horizontal", GRID, un(step=3, how="horizontal")),
        case("a_column", GRID, un(step=3, columns=["y"])),
        case("horizontal_two_across", ints(10), un(step=2, how="horizontal"), note="horizontally the frame is Step columns wide"),
        case("vertical_step_past_height", ints(3), un(step=5)),
        case("a_fill_for_all", XS, un(step=2, fill_values=0), note="one fill for every column, cast to each column's type, text of a number"),
        case("text_for_all", XS, un(step=2, fill_values="z"), note="text for a number is null"),
        case("a_fill_each", XS, un(step=2, fill_values=[2.5, "q"])),
        case("a_null_kept", pl.DataFrame({"x": [1, None, 3]}), un(step=2, fill_values=0), note="a value already null stays null"),
        case("horizontal_filled", pl.DataFrame({"x": [1, 2, 3]}), un(step=2, how="horizontal", fill_values=9)),
        case("padded_25_by_2", ints(25), un(step=2), note="the suffixes padded to the digits of the count of new columns"),
        case("padded_25_by_2_horizontal", ints(25), un(step=2, how="horizontal")),
        case("padded_100_by_1", ints(100), un(step=1)),
        case("padded_10_by_1", ints(10), un(step=1)),
        case("padded_9_by_1", ints(9), un(step=1)),
        case("unstack_gen", gen, un(step=7)),
        case("unstack_gen_horizontal", gen, un(step=7, how="horizontal", fill_values=[0.5, "pad"])),
    ]
