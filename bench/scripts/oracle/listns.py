"""The listns group: the `.list` namespace's reducing and reshaping halves over a null
list, an empty list and a list of a null (listns_test.go, steps 44 and 291). Every
literal frame is the Parquet fixture the Go test wrote by hand; a generated case
follows each function's. The batch-size sweep stays in the Go file as a differential.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

c, el = pl.col, pl.element
L = pl.col("tags").list


def lists(rows: list, inner: pl.DataType = pl.Int64, name: str = "tags") -> pl.DataFrame:
    return pl.DataFrame({name: pl.Series(name, rows, dtype=pl.List(inner))})


def sel(*exprs: pl.Expr):
    return lambda df: df.select(*exprs)


NS = lists([[10, 20], [], None, [5]])
GETIDX = lists([[10, 20, 30], [7]])
STRS = lists([["go", "rust", "c"], []], pl.String)
RESHAPE = lists([[3, 1, 2], [2, 1, 3, 2], [1, None], [], None, [None, None, 4]])
EDGES = lists([[10, 20, 30]])
SORTSTR = lists([["rust", "c", "go"], []], pl.String)
HOURS = 3_600_000_000_000
DUR = lists([[HOURS, 3 * HOURS], [(1 << 63) - 1, (1 << 63) - 1, (1 << 63) - 1]]).select(
    c("tags").cast(pl.List(pl.Duration("ns"))))
F32 = lists([[1.0, 2.0, 3.0, 4.0]], pl.Float32)
MAXNAN = lists([[1.0, float("nan")], [float("nan")], [2.0, None], None], pl.Float64)

REDUCE = [L.len().alias("n"), L.sum().alias("sum"), L.min().alias("min"), L.max().alias("max"),
          L.get(0, null_on_oob=True).alias("first"), L.get(-1, null_on_oob=True).alias("last")]
RESHAPES = [L.reverse().alias("rev"), L.head(2).alias("head"), L.tail(2).alias("tail"), L.slice(1, 2).alias("slice"),
            L.sort().alias("sort"), L.sort(descending=True).alias("desc"), L.unique(maintain_order=True).alias("uniq"),
            L.drop_nulls().alias("drop")]


def cases() -> list[Case]:
    g = Gen(4991)
    n = 160
    gen_ints = g.frame(g.lists(n, g.ints(n, pl.Int64, lo=-50, hi=50, name="e"), name="tags"))
    gen_ints_full = g.frame(g.lists(n, g.ints(n, pl.Int64, lo=-50, hi=50, null_rate=0, name="e"), empty_rate=0, name="tags"))
    gen_strs = g.frame(g.lists(n, g.strings(n, name="e"), maxlen=5, name="tags"))
    gen_floats = g.frame(g.lists(n, g.floats(n, name="e"), maxlen=5, name="tags"))
    gen_floats_no_nan = g.frame(g.lists(n, g.floats(n, name="e").fill_nan(None), maxlen=5, name="tags"))
    gen_f32 = g.frame(g.lists(n, g.floats(n, dtype=pl.Float32, name="e"), maxlen=5, name="tags"))
    gen_dur = g.frame(g.lists(n, g.durations(n, "us", name="e"), maxlen=5, name="tags"))
    return [
        # --- the reducing half: null and empty apart ---------------------------------
        case("semantics", NS, sel(*REDUCE[:5], L.contains(20).alias("has20")),
             note="a null list is not an empty list: len 0 against null, contains false against null"),
        case("get_indexing", GETIDX, sel(L.get(1, null_on_oob=True).alias("at1"), L.get(-1, null_on_oob=True).alias("last"),
                                         L.get(-3, null_on_oob=True).alias("backthree"), L.get(9, null_on_oob=True).alias("past"))),
        case("strings", STRS, sel(L.len().alias("n"), L.min().alias("min"), L.contains("rust").alias("hasrust"),
                                  L.get(0, null_on_oob=True).alias("first"))),
        case("filter_len_gt_1", NS, lambda df: df.filter(L.len() > 1)),
        case("semantics_gen", gen_ints, sel(*[e for e in REDUCE if e.meta.output_name() != "sum"],
                                            L.contains(7).alias("has7"), L.contains(None).alias("hasnull"))),
        case("sum_gen", gen_ints_full, sel(L.sum().cast(pl.Int64).alias("sum")),
             note="no empty list and no null element, whose sum is null here and 0 in Polars; ursus's Int128 sum cast back to Polars' Int64"),
        case("max_nan", MAXNAN, sel(L.min().alias("min"), L.max().alias("max")),
             note="Max propagates NaN here, where Polars' max passes it over (feature-gaps.md §9); min agrees"),
        case("float_reduce_gen", gen_floats_no_nan, sel(L.min().alias("min"), L.max().alias("max"), L.get(0, null_on_oob=True).alias("first"),
                                                        L.contains(0.5).alias("has_half")), note="no NaN, which Max propagates here (§9)"),
        case("strings_gen", gen_strs, sel(L.len().alias("n"), L.min().alias("min"), L.max().alias("max"),
                                          L.contains("a").alias("has_a"), L.get(0, null_on_oob=True).alias("first"))),
        # --- the reshaping half -------------------------------------------------------
        case("reshape", RESHAPE, sel(*RESHAPES), note="a null list stays null through all eight; nulls sort first both ways"),
        case("reshape_edges", EDGES, sel(L.head(0).alias("h0"), L.head(99).alias("h99"), L.tail(0).alias("t0"), L.tail(99).alias("t99"),
                                         L.slice(-2, 1).alias("back"), L.slice(1, 99).alias("rest"), L.slice(9, 2).alias("past"),
                                         L.slice(-99, 2).alias("before"), L.slice(-4, 2).alias("cut")),
             note="an offset before the start cuts the length by as much, as Polars' does (step 291)"),
        case("sort_strings", SORTSTR, sel(L.sort().alias("s"), L.sort(descending=True).alias("d"), L.reverse().alias("r"))),
        case("chains", RESHAPE, sel(L.sort().list.head(2).list.len().alias("n"),
                                    L.sort().list.head(2).list.get(0, null_on_oob=True).alias("smallest"),
                                    L.drop_nulls().list.len().alias("present"))),
        case("reshape_gen", gen_ints, sel(*RESHAPES, L.slice(-2, 1).alias("back"), L.slice(-9, 3).alias("before"))),
        case("reshape_strings_gen", gen_strs, sel(*RESHAPES)),
        case("reshape_floats_gen", gen_floats, sel(*RESHAPES), note="NaN sorts after every number, as Polars sorts it"),
        # --- mean keeps the element type -------------------------------------------------
        case("mean_duration", DUR, sel(L.mean()), note="a List(Duration)'s mean is a Duration; three MaxInt64s' mean is MaxInt64"),
        case("mean_f32", F32, sel(L.mean())),
        case("mean_gen", gen_floats, sel(L.mean()), float="rel:1e-12",
             note="a float mean's summation order leaves last bits apart (feature-gaps.md §1)"),
        case("mean_f32_gen", gen_f32, sel(L.mean()), float="rel:1e-6", note="a Float32 mean summed in Float32 by Polars, wider here"),
        case("mean_duration_gen", gen_dur, sel(L.mean())),
    ]
