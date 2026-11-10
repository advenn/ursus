"""The arrns group: Polars' arr namespace (arrns_test.go, step 342): three Arrays of
three, a null element in each, a null array and a row's index; then generated arrays
of integers, floats, text and Booleans.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

c, el = pl.col, pl.element
A = pl.col("a").arr


def arrays(rows: list, inner: pl.DataType, width: int, name: str) -> pl.Series:
    return pl.Series(name, rows, dtype=pl.Array(inner, width))


ARR = pl.DataFrame([
    arrays([[3, 1, 2], [4, None, 6], None, [2, 2, 1]], pl.Int64, 3, "a"),
    arrays([["b", "a", "b"], ["x", None, "y"], None, ["q", "q", "q"]], pl.String, 3, "t"),
    arrays([[True, False, True], [None, True, True], None, [False, False, False]], pl.Boolean, 3, "b"),
    pl.Series("i", [0, 1, 2, -1], dtype=pl.Int64),
])
FLOATS = pl.DataFrame([arrays([[1.5, None, 2.5], [None, None, None], None], pl.Float64, 3, "a")])


def sel(*exprs: pl.Expr):
    return lambda df: df.select(*exprs)


def one(name: str, e: pl.Expr, **opts) -> Case:
    return case(name, ARR, sel(e.alias("r")), **opts)


def gen_arrays(g: Gen, n: int, elements: pl.Series, width: int, null_rate: float = 0.1, full: bool = False,
               name: str = "a") -> pl.Series:
    """Arrays of width over the elements given, some null; a shorter draw is padded
    with nulls, or, where full, every array is width fresh draws."""
    if full:
        elems = elements.to_list()
        picks = g.rng.integers(0, len(elems), (n, width))
        rows = [[elems[j] for j in row] for row in picks]
        rows = g._place(rows, null_rate, [])
    else:
        rows = g.lists(n, elements, maxlen=width, null_rate=null_rate, empty_rate=0).to_list()
        rows = [None if r is None else (r + [None] * width)[:width] for r in rows]
    return pl.Series(name, rows, dtype=pl.Array(elements.dtype, width))


def cases() -> list[Case]:
    g = Gen(4992)
    n = 120
    ints = gen_arrays(g, n, g.ints(n, pl.Int64, lo=-20, hi=20, name="e"), 4)
    ints_full = gen_arrays(g, n, g.ints(n, pl.Int64, lo=-20, hi=20, null_rate=0, name="e"), 4, full=True)
    floats = gen_arrays(g, n, g.floats(n, edges=False, null_rate=0, name="e"), 3, full=True)
    text = gen_arrays(g, n, g.strings(n, name="e"), 3, name="t")
    bools = gen_arrays(g, n, g.bools(n, name="e"), 3, name="b")
    idx = g.ints(n, pl.Int64, lo=-5, hi=5, name="i")
    gen_ints = g.frame(ints, idx)
    gen_full = g.frame(ints_full)
    gen_floats = g.frame(floats)
    gen_text = g.frame(text)
    gen_bools = g.frame(bools)
    return [
        one("max", A.max()), one("min", A.min()),
        one("sum", A.sum(), note="an integer Sum is an Int128 here, Polars' arr.sum an Int64 (feature-gaps.md §9)"),
        one("mean", A.mean()), one("median", A.median()),
        one("n_unique", A.n_unique(), note="Polars' n_unique counts a null as a value, [4, null, 6] 3; ursus's passes it over (§9)"),
        one("arg_min", A.arg_min()), one("arg_max", A.arg_max()),
        one("first", A.first()), one("last", A.last()),
        one("get_1", A.get(1, null_on_oob=True)), one("get_i", A.get(c("i"), null_on_oob=True)),
        one("contains_2", A.contains(2)), one("count_matches_2", A.count_matches(2)), one("len", A.len()),
        one("head_2", A.head(2)), one("tail_2", A.tail(2)), one("slice_1_2", A.slice(1, 2)),
        one("reverse", A.reverse()), one("shift_1", A.shift(1)), one("shift_m1", A.shift(-1)),
        one("sort", A.sort()), one("sort_desc_nulls_last", A.sort(descending=True, nulls_last=True)),
        one("unique", A.unique(maintain_order=True)),
        one("eval", A.eval(el() * 10)), one("eval_as_list", A.eval(el() * 10, as_list=True)),
        one("agg_max", A.agg(el().max())),
        one("dot", A.dot(c("a")), note="a Dot is a Mul's Sum, an Int128 here (§9)"),
        one("dot_null_row", A.dot(pl.when(c("i") == 0).then(pl.lit(None, dtype=pl.Array(pl.Int64, 3))).otherwise(A.reverse()))),
        one("to_list", A.to_list()),
        one("join", c("t").arr.join("-")),
        one("any", c("b").arr.any()), one("all", c("b").arr.all()),
        one("std", A.std(1), float="rel:1e-12", note="a one-pass variance here, two in Polars: an ulp apart (§9)"),
        one("var_ddof0", A.var(0), float="rel:1e-12", note="as std"),
        case("sum_floats_all_null", FLOATS, sel(A.sum().alias("r")),
             note="an array of nulls alone sums to null here, as a group of no value does, and to 0.0 in Polars (§9)"),
        case("explode", ARR, sel(A.explode()), note="a null array is one null row, as Polars 2.1 keeps it"),
        one("to_struct", A.to_struct(), note="a null array is a null struct, as Polars 2.1 gives it"),
        one("to_struct_named_field", A.to_struct(["x", "y", "z"]).struct.field("y")),
        # --- generated -----------------------------------------------------------------
        case("aggs_gen", gen_ints, sel(A.max().alias("max"), A.min().alias("min"), A.mean().alias("mean"), A.median().alias("median"),
                                       A.arg_min().alias("amin"), A.arg_max().alias("amax"), A.first().alias("first"), A.last().alias("last"),
                                       A.get(2, null_on_oob=True).alias("g2"), A.get(c("i"), null_on_oob=True).alias("gi"),
                                       A.contains(3).alias("c3"), A.count_matches(3).alias("cm3"), A.len().alias("len"))),
        case("sum_n_unique_gen", gen_full, sel(A.sum().cast(pl.Int64).alias("sum"), A.n_unique().alias("nu"),
                                               A.dot(c("a")).cast(pl.Int64).alias("dot")),
             note="full arrays of no null element, which n_unique counts in Polars and not here; the Int128 sums cast back"),
        case("reshape_gen", gen_ints, sel(A.head(2).alias("h"), A.tail(3).alias("t"), A.slice(1, 2).alias("s"), A.reverse().alias("r"),
                                          A.shift(1).alias("sh"), A.shift(c("i")).alias("shi"), A.sort().alias("so"),
                                          A.sort(descending=True, nulls_last=True).alias("sd"), A.unique(maintain_order=True).alias("u"),
                                          A.to_list().alias("tl"), A.eval(el() * 10).alias("e"), A.agg(el().min()).alias("ag"))),
        case("floats_gen", gen_floats, sel(A.std(1).alias("std"), A.var(0).alias("var0"), A.mean().alias("mean"), A.sum().alias("sum"),
                                           A.max().alias("max")), float="rel:1e-12",
             note="full arrays of no null element; one-pass moments and sums, an ulp apart (§9)"),
        case("text_gen", gen_text, sel(c("t").arr.join("-").alias("j"), c("t").arr.first().alias("f"),
                                       c("t").arr.sort().alias("s"), c("t").arr.contains("a").alias("has_a"))),
        case("bools_gen", gen_bools, sel(c("b").arr.any().alias("any"), c("b").arr.all().alias("all"),
                                         c("b").arr.any(ignore_nulls=False).alias("any_k"), c("b").arr.all(ignore_nulls=False).alias("all_k"))),
    ]
