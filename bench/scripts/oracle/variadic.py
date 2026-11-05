"""The variadic group, step 302: a selection among a variadic function's operands is
its columns, one operand each, so the function answers one column named after the
first; a variadic or a fold inside list.eval and cumulative_eval; format of a variadic
(variadic_test.go). The literal frames are the Go test's; a generated case follows.

Not here, with the reason: a fold in cumulative_eval, which Polars refuses ("not
allowed with non-scalar output") and ursus answers (feature-gaps.md §9; a differential
in the Go test); hash_rows, whose numbers are ursus's own (§9); the refusals.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

c, el = pl.col, pl.element
a, b, x = c("a"), c("b"), c("x")

TWO = pl.DataFrame({"a": pl.Series([1, None], dtype=pl.Int64), "b": pl.Series([None, 2], dtype=pl.Int64)})
XY = pl.DataFrame({"x": [1, 2], "y": [3, 4]}).select(pl.concat_list(c("x")).alias("a"), pl.concat_list(c("y")).alias("b"))
GX = pl.DataFrame({"g": [1, 1, 2], "x": [1, 2, 3]})
LISTS = GX.group_by("g", maintain_order=True).agg(x.implode())
AB = pl.DataFrame({"a": [1, 2], "b": [3, 4]})


def add(acc, v):
    return acc + v


def text(e: pl.Expr) -> pl.Expr:
    return e.cast(pl.String)


def sel(*e: pl.Expr):
    return lambda df: df.select(*e)


def each(body: pl.Expr):
    return lambda df: df.select(x.list.eval(body).explode())


def cases() -> list[Case]:
    g = Gen(4978)
    gen = g.frame(g.ints(150, pl.Int64, lo=-50, hi=50, null_rate=0.2, name="a"), g.ints(150, pl.Int64, lo=-50, hi=50, null_rate=0.2, name="b"),
                  g.ints(150, pl.Int64, lo=-50, hi=50, null_rate=0, name="c"))
    gen_lists = g.frame(g.lists(120, g.ints(20, pl.Int64, lo=-9, hi=9, null_rate=0), null_rate=0.1, name="x"))
    return [
        # --- a selection as the operands -------------------------------------------------
        case("max_horizontal_all", TWO, sel(pl.max_horizontal(pl.all()))),
        case("min_horizontal_all", TWO, sel(pl.min_horizontal(pl.all()))),
        case("sum_horizontal_cols", TWO, sel(pl.sum_horizontal(c("a", "b")))),
        case("mean_horizontal_all", TWO, sel(pl.mean_horizontal(pl.all()))),
        case("any_horizontal_all_gt_1", TWO, sel(pl.any_horizontal(pl.all() > 1))),
        case("all_horizontal_all_gt_0", TWO, sel(pl.all_horizontal(pl.all() > 0))),
        case("coalesce_all", TWO, sel(pl.coalesce(pl.all(), pl.lit(-1, dtype=pl.Int64)))),
        case("concat_str_all", TWO, sel(pl.concat_str(pl.all().cast(pl.String).fill_null("_"), separator="-"))),
        case("struct_all_json", TWO, sel(pl.struct(pl.all()).struct.json_encode())),
        case("concat_list_all_len", TWO, sel(pl.concat_list(pl.all()).list.len())),
        case("list_concat_all", XY, sel(pl.all().list.concat(pl.lit([9], dtype=pl.List(pl.Int64))).list.eval(text(el())).list.join(","))),
        case("sum_horizontal_all_null", pl.DataFrame({"a": pl.Series([None, 1], dtype=pl.Int64), "b": pl.Series([None, None], dtype=pl.Int64)}),
             sel(pl.sum_horizontal(a, b), pl.mean_horizontal(a, b).alias("mean"), pl.max_horizontal(a, b).alias("max")),
             note="Polars sums a row of nulls to 0; ursus answers null, as Sum does of an all-null group"),
        case("all_minus_sum_horizontal", TWO, sel(pl.all() - pl.sum_horizontal(pl.all())),
             note="a selection outside the function expands per column, the function inside it built first"),
        case("horizontal_gen", gen, sel(pl.max_horizontal(pl.all()).alias("max"), pl.min_horizontal(pl.all()).alias("min"),
                                        pl.sum_horizontal(pl.all()).alias("sum"), pl.mean_horizontal(pl.all()).alias("mean"),
                                        pl.any_horizontal(pl.all() > 10).alias("any"), pl.all_horizontal(pl.all() > -10).alias("all"),
                                        pl.coalesce(pl.all(), pl.lit(-1, dtype=pl.Int64)).alias("coalesce"),
                                        pl.concat_str(pl.all().cast(pl.String).fill_null("_"), separator="-").alias("str"),
                                        pl.concat_list(pl.all()).alias("list"))),
        # --- inside a list's body ---------------------------------------------------------
        case("eval_concat_str", LISTS, sel(x.list.eval(pl.concat_str(text(el()), text(el()), separator=":")).list.join("|"))),
        case("eval_sum_horizontal", LISTS, each(pl.sum_horizontal(el(), el()))),
        case("eval_coalesce", LISTS, each(pl.coalesce(pl.when(el() > 1).then(el()).otherwise(pl.lit(None, dtype=pl.Int64)), el() * 10))),
        case("eval_struct_json", LISTS, lambda df: df.select(x.list.eval(pl.struct(el().alias("a"), (el() * 2).alias("b"))).explode().struct.json_encode())),
        case("eval_fold", LISTS, each(pl.fold(pl.lit(0, dtype=pl.Int64), add, [el(), el()]))),
        case("eval_in_group_by", GX, lambda df: df.group_by("g", maintain_order=True).agg(
            x.implode().list.eval(pl.concat_str(text(el()), text(el()), separator=":")).list.join("|")).select(x)),
        case("cumulative_eval_max_horizontal", GX, sel(x.cumulative_eval(pl.max_horizontal(el().sum(), el().max())))),
        case("eval_gen", gen_lists, sel(x.list.eval(pl.sum_horizontal(el(), el() * 2)).alias("sum"),
                                        x.list.eval(pl.fold(pl.lit(1, dtype=pl.Int64), add, [el(), el()])).alias("fold"),
                                        x.list.eval(pl.coalesce(pl.when(el() > 0).then(el()).otherwise(pl.lit(None, dtype=pl.Int64)), el() * 10)).alias("coalesce"),
                                        x.list.eval(pl.concat_str(text(el()), text(el()), separator=":")).list.join("|").alias("str"))),
        # --- format of a variadic -----------------------------------------------------------
        case("format_variadic_lit_first", AB, sel(pl.format("<{}>", pl.concat_str(pl.lit("x"), text(a), separator="-")))),
        case("format_variadic_col_first", AB, sel(pl.format("<{}>", pl.coalesce(b, a)))),
        case("format_variadic_gen", gen, sel(pl.format("<{}>", pl.coalesce(b, a, c("c"))), pl.format("[{}]", pl.sum_horizontal(a, c("c"))).alias("sum"))),
    ]
