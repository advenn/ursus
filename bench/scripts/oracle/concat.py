"""The concat group: Concat's order, its diagonal, Polars' relaxed, horizontal and
align modes (concat_test.go; P5 of v0.6.7-scope.md, step 307). Where Polars' vertical
refuses an Int32 beside an Int64, which ConcatStrict meets at Int64 (feature-gaps.md
§9), the case asks vertical_relaxed. ConcatHorizontalExtend pads a shorter frame as
Polars' horizontal_extend does. Not here: the refusals, the schema and nullability
checks, the plans, and the spilled align, a Go differential.
"""

from __future__ import annotations

from datetime import date, datetime

import polars as pl

from . import Case, case
from .gen import Gen

A = pl.DataFrame({"k": [1, 2], "v": ["a", "b"]})
B = pl.DataFrame({"k": [3], "v": ["c"]})
IDS = pl.DataFrame({"id": [3, 1, 2], "x": [30, 10, 20]})
YS = pl.DataFrame({"id": [2, 4, 1], "y": ["b", "d", "a"]})
ZS = pl.DataFrame({"id": [1, 5], "z": [1.5, 5.5]})


def ab(a: pl.Series, *b: str) -> pl.DataFrame:
    return pl.DataFrame({"a": a, "b": pl.Series(list(b), dtype=pl.String)})


def cat(how: str):
    return lambda **frames: pl.concat([frames[k] for k in sorted(frames)], how=how)


def two(name: str, how: str, f0: pl.DataFrame, f1: pl.DataFrame, **opts) -> Case:
    return case(name, {"f0": f0, "f1": f1}, cat(how), **opts)


def cases() -> list[Case]:
    g = Gen(5014)
    gen3 = [g.frame(g.floats(70, name="x"), g.strings(70, name="s")) for _ in range(3)]
    diag = [g.frame(g.ints(50, pl.Int64, name="k"), g.strings(50, name="s")), g.frame(g.floats(40, name="x"), g.ints(40, pl.Int64, name="k")),
            g.frame(g.bools(30, name="b"))]
    keys = [pl.Series("id", sorted(set(g.rng.choice(200, size=60, replace=False).tolist())), dtype=pl.Int64) for _ in range(3)]
    align = [pl.DataFrame([keys[0], pl.Series("x", g.rng.uniform(-1, 1, len(keys[0])), dtype=pl.Float64)]).sample(fraction=1, shuffle=True, seed=1),
             pl.DataFrame([keys[1], pl.Series("y", [str(v) for v in keys[1].to_list()], dtype=pl.String)]).sample(fraction=1, shuffle=True, seed=2),
             pl.DataFrame([keys[2], pl.Series("z", keys[2].to_list(), dtype=pl.Int64)]).sample(fraction=1, shuffle=True, seed=3)]
    return [
        case("vertical_twice", {"a": A, "b": B}, lambda a, b: pl.concat([a, b, a]),
             note="every row of the first frame before every row of the second, and a frame used twice contributes twice"),
        two("vertical_int32_int64", "vertical_relaxed", pl.DataFrame({"v": pl.Series([1, 2], dtype=pl.Int32)}), pl.DataFrame({"v": [1 << 40]}),
            note="ConcatStrict meets an Int32 and an Int64 at Int64, cast not reinterpreted; Polars' vertical refuses them (§9)"),
        two("diagonal", "diagonal", pl.DataFrame({"k": [1], "v": ["a"]}), pl.DataFrame({"x": [1.5], "k": [2]}),
            note="the union of the columns in first appearance, nulls in the gaps"),
        two("relaxed_int_float", "vertical_relaxed", ab(pl.Series([3]), "z"), ab(pl.Series([1.5]), "w")),
        two("relaxed_number_text", "vertical_relaxed", ab(pl.Series([3]), "z"), ab(pl.Series(["t"]), "v")),
        two("relaxed_bool_number", "vertical_relaxed", ab(pl.Series([3]), "z"), ab(pl.Series([True]), "q")),
        two("relaxed_date_datetime", "vertical_relaxed", pl.DataFrame({"a": [date(2024, 1, 2)]}), pl.DataFrame({"a": [datetime(2024, 1, 3, 4)]})),
        two("relaxed_int32_int64", "vertical_relaxed", ab(pl.Series([1, 2], dtype=pl.Int32), "x", "y"), ab(pl.Series([3]), "z")),
        two("diagonal_relaxed", "diagonal_relaxed", ab(pl.Series([1]), "x"), pl.DataFrame({"a": [1.5], "c": [3.5]})),
        two("horizontal_extend", "horizontal_extend", pl.DataFrame({"a": [1, 2, 3]}), pl.DataFrame({"b": ["x", "y"]})),
        two("horizontal_extend_no_rows", "horizontal_extend", pl.DataFrame({"a": [1, 2]}), pl.DataFrame({"b": pl.Series([], dtype=pl.String)})),
        two("horizontal", "horizontal", pl.DataFrame({"a": [1, 2]}), pl.DataFrame({"b": ["x", "y"]})),
        case("align", {"f0": IDS, "f1": YS, "f2": ZS}, cat("align")),
        case("align_inner", {"f0": IDS, "f1": YS, "f2": ZS}, cat("align_inner")),
        case("align_left", {"f0": IDS, "f1": YS, "f2": ZS}, cat("align_left")),
        case("align_right", {"f0": IDS, "f1": YS, "f2": ZS}, cat("align_right")),
        two("align_two_columns", "align", pl.DataFrame({"k1": [1, 1, 2], "k2": ["a", "b", "a"], "v": [1, 2, 3]}),
            pl.DataFrame({"k2": ["a", "b"], "k1": [1, 1], "w": [10, 20]}), note="two keys, the second frame's in another order"),
        two("align_null_keys", "align", pl.DataFrame({"id": [1, None], "x": [1, 2]}), pl.DataFrame({"id": [None, 1], "y": [3, 4]}),
            note="null keys match nothing and sort first"),
        two("align_key_twice", "align", pl.DataFrame({"id": [1, 1], "x": [1, 2]}), pl.DataFrame({"id": [1], "y": [3]})),
        two("align_widths", "align", pl.DataFrame({"id": pl.Series([1, 2], dtype=pl.Int32), "x": [1, 1]}), pl.DataFrame({"id": [1, 2], "y": [3, 4]})),
        case("align_one_frame", {"f0": IDS}, cat("align"), note="one frame is itself, unsorted"),
        case("vertical_gen", {"f0": gen3[0], "f1": gen3[1], "f2": gen3[2]}, cat("vertical")),
        case("diagonal_gen", {"f0": diag[0], "f1": diag[1], "f2": diag[2]}, cat("diagonal")),
        case("align_gen", {"f0": align[0], "f1": align[1], "f2": align[2]}, cat("align"),
             note="three frames of unique keys, each a draw of 60 from 200, in no order"),
        case("align_left_gen", {"f0": align[0], "f1": align[1], "f2": align[2]}, cat("align_left")),
    ]
