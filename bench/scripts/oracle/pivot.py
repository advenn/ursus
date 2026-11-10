"""The pivot group: DataFrame.pivot against Polars 2.1's (pivot_test.go; step 229, P1 of
v0.6.7-scope.md, step 303), its several On columns, on_columns given, and a cell of
none under each aggregate; and Transpose. Polars 2.1 has on_columns, so the lazy cases
ask it directly. A count is a Uint64 here and a sum an Int128, and a sum of none is
null here where Polars' is 0 (feature-gaps.md §9): the Go side names those. Not here:
the refusals, the plan and schema of a lazy pivot, the spilled pivot, and the
combination holding a null, which Polars refuses as a second "null" column (§9) and
which the Go test keeps as a pin of the documented naming.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
nan = float("nan")
PF = pl.DataFrame({"k": ["b", "a", "b", "a", None], "on": ["y", "x", "x", "y", "x"], "p": ["p", "q", "p", "q", "p"],
                   "v": [1, 2, 3, 4, 5], "w": [1.5, 2.5, None, 4.5, 5.5]})
DUP = pl.DataFrame({"k": [1, 1], "on": ["x", "x"], "v": [1, 2]})
NULL_ON = pl.DataFrame({"k": [1, 2], "on": [10, None], "v": [1, 2]})
NONE = pl.DataFrame({"k": [1, 1, 2], "on": ["x", "x", "y"], "v": [1, 2, 5]})
FLOATS = pl.DataFrame({"k": [1] * 6, "f": [1.5, 2.0, 1.5, nan, -0.0, 1.5], "v": [1, 2, 3, 4, 5, 6]})
ZEROS = pl.DataFrame({"k": [1, 1, 2], "f": [0.0, -0.0, 0.0], "v": [1, 2, 3]})
AGGS = {"len": pl.len(), "n_unique": pl.element().n_unique(), "sum": pl.element().sum(), "first": pl.element().first(),
        "last": pl.element().last(), "min": pl.element().min(), "max": pl.element().max(), "mean": pl.element().mean(),
        "median": pl.element().median()}


def piv(**o):
    return lambda df: df.pivot(**o)


def cases() -> list[Case]:
    g = Gen(5015)
    n = 200
    gen = g.frame(g.ints(n, pl.Int64, lo=0, hi=9, edges=False, name="k"), g.strings(n, edges=False, alphabet="abcd", maxlen=1, name="on"),
                  g.floats(n, edges=False, name="v"))
    tr = g.frame(g.floats(25, edges=False, name="a"), g.floats(25, edges=False, name="b"), g.ints(25, pl.Int64, name="i"))
    out = [
        case("one_value", PF, piv(on="on", index="k", values="v")),
        case("two_values", PF, piv(on="on", index="k", values=["v", "w"])),
        case("sort_columns", PF, piv(on="on", index="k", values="v", sort_columns=True)),
        case("values_left_out", PF.drop("p"), piv(on="on", index=["k", "v"]), note="Values left out are every other column: w"),
        case("agg_sum", DUP, lambda df: df.pivot(on="on", index="k", values="v", aggregate_function="sum").select("k", c("x").cast(pl.Int64)),
             note="a cell of two values summed; the Int128 sum here cast to Polars' Int64"),
        case("null_on", NULL_ON, piv(on="on", index="k", values="v"), note="a null On value is a column named null"),
        case("several_on", PF, piv(on=["on", "p"], index="k", values="v")),
        case("several_on_two_values", PF, piv(on=["on", "p"], index="k", values=["v", "w"])),
        case("several_on_sorted", PF, piv(on=["on", "p"], index="k", values="v", sort_columns=True)),
        case("several_on_given", PF, piv(on=["on", "p"], on_columns=pl.DataFrame({"on": ["x", "y"], "p": ["p", "q"]}), index="k", values="v"),
             note="the combinations given, a row of OnColumns a new column"),
        case("given_subset", PF, piv(on="on", on_columns=["x"], index="k", values="v")),
        case("given_reordered_never_held", PF, piv(on="on", on_columns=["z", "y", "x"], index="k", values="v", sort_columns=True),
             note="a value the frame never holds is a column of nulls; sort_columns is ignored beside on_columns"),
        case("given_x_y_lazy", PF, piv(on="on", on_columns=["x", "y"], index="k", values="v")),
        case("given_index_none_of_whose_values", pl.DataFrame({"k": ["a", "b"], "on": ["x", "y"], "v": [1, 2]}),
             piv(on="on", on_columns=["x"], index="k", values="v"), note="an index none of whose values is given is a row still"),
        case("given_null", PF.with_columns(pl.when(c("on") == "y").then(None).otherwise(c("on")).alias("on")),
             piv(on="on", on_columns=["x", None], index="k", values="v")),
        case("given_none", PF, piv(on="on", on_columns=[], index="k", values="v")),
        case("floats_nan_and_zeros", FLOATS, piv(on="f", on_columns=[nan, 0.0, 1.5], index="k", values="v", aggregate_function="first"),
             note="NaN matches NaN and 0 matches -0, as a group-by keys them; the column is named by the value given"),
        case("floats_both_zeros", ZEROS, lambda df: df.pivot(on="f", on_columns=[0.0, -0.0], index="k", values="v", aggregate_function="sum").
             select("k", c("0.0").cast(pl.Int64), c("-0.0").cast(pl.Int64)), note="both zeros given: each a column of both zeros' rows"),
        case("int64_given_for_int32", PF.with_columns(c("v").cast(pl.Int32).alias("n")), piv(on="n", on_columns=[2, 5], index="k", values="w"),
             note="OnColumns of another integer width are cast to the On column's"),
    ]
    out += [case(f"none_{name}", NONE, piv(on="on", index="k", values="v", aggregate_function=agg),
                 note="a cell none fall in is the aggregate of none") for name, agg in AGGS.items()]
    out += [
        case("none_sum_fill_0", NONE, piv(on="on", index="k", values="v", aggregate_function=pl.element().sum().fill_null(0)),
             note="Sum().FillNull(0) is Polars' sum of none"),
        case("none_no_agg", NONE.filter(c("v") != 2), piv(on="on", index="k", values="v")),
        case("pivot_gen_max", gen, piv(on="on", index="k", values="v", aggregate_function="max", sort_columns=True)),
        case("pivot_gen_mean", gen, piv(on="on", index="k", values="v", aggregate_function="mean", sort_columns=True), float="rel:1e-12",
             note="a mean of a few floats that nearly cancel: the summation order's last bits, exact here (§9)"),
        case("pivot_gen_given", gen, piv(on="on", on_columns=["d", "b", "z"], index="k", values="v", aggregate_function="min")),
        # --- transpose ----------------------------------------------------------------
        case("transpose_header", pl.DataFrame({"a": [1, 2], "b": [3.5, 4.5], "c": [None, 6]}), lambda df: df.transpose(include_header=True)),
        case("transpose_named", pl.DataFrame({"a": [1, 2], "b": [3.5, 4.5], "c": [None, 6]}), lambda df: df.transpose(column_names=["p", "q"])),
        case("transpose_mixed", pl.DataFrame({"a": [1, 3], "s": ["x", "y"], "b": [True, False]}), lambda df: df.transpose(),
             note="text beside a number or a Boolean is text"),
        case("transpose_bool_int", pl.DataFrame({"b": [True], "a": [1]}), lambda df: df.transpose(), note="a Boolean beside a number is the number"),
        case("transpose_names_from", pl.DataFrame({"id": ["a", "b", "c"], "col1": [1, 3, 2], "col2": [3, 4, 6]}),
             lambda df: df.transpose(include_header=True, column_names="id")),
        case("transpose_gen", tr, lambda df: df.transpose(include_header=True)),
    ]
    return out
