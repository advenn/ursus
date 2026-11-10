"""The decimalagg group: every aggregate over a Decimal(10, 2), by group
(internal/expr/decimalagg_test.go, step 503). The Go test stated each aggregate's
output type by hand, "measured against Polars, DuckDB and PyArrow"; here Polars 2.1.0
answers each with its type and its values. The values are dyadic, so a mean, a
variance and a product are exact in a Float64 and the comparison is by bits; entropy
alone takes Go's log against glibc's.

Polars refuses a bitwise aggregate of a Decimal, as ursus does (bitwise_and), and
answers any and all, true where a value is not zero, which ursus refuses (any_all, a
finding of step 503). The Go test keeps its refusals of "a pair of inputs" and its
walk of every aggregate, the mechanism.

Two questions are asked around ursus's documented rules (feature-gaps.md §9): a
Product is a Float64 for every number here, so Polars is asked the product of the
floats the Decimals hold, exact over these dyadic values, where its Decimal(38, 2)
product rounds 1.50 x 2.25 to 3.38; and n_unique does not count a null here, so
Polars is asked it of the values without their nulls.
"""

from __future__ import annotations

import decimal

import polars as pl

from . import Case, case
from .gen import Gen

D = decimal.Decimal
DEC = pl.Decimal(10, 2)
x = pl.col("x")


def dec(vals: list, groups: list) -> pl.DataFrame:
    return pl.DataFrame([pl.Series("g", groups, dtype=pl.Int64),
                         pl.Series("x", [None if v is None else D(v) for v in vals], dtype=DEC)])


# Six values in two groups, the second with a null: every value a multiple of 1/4.
DF = dec(["1.50", "2.25", None, "-3.25", "4.00", "0.75"], [1, 1, 1, 2, 2, 2])
# Positive values for entropy, whose logarithm wants them.
POS = dec(["1.50", "2.25", None, "3.25", "4.00"], [1, 1, 1, 2, 2])
# One value a group, for item.
ONE = dec(["1.50", "-3.10"], [1, 2])
# A mode a group.
MODAL = dec(["1.50", "1.50", "2.25", "-3.25", "4.00", "4.00"], [1, 1, 1, 2, 2, 2])


def agg(*exprs: pl.Expr):
    return lambda df: df.group_by("g", maintain_order=True).agg(*exprs)


def cases() -> list[Case]:
    g = Gen(5032)
    gen = pl.DataFrame([g.ints(150, pl.Int64, lo=0, hi=4, null_rate=0, edges=False, name="g"),
                        g.decimals(150, precision=10, scale=2, name="x")])
    return [
        case("sum", DF, agg(x.sum().alias("sum"))),
        case("mean", DF, agg(x.mean().alias("mean"))),
        case("min_max", DF, agg(x.min().alias("min"), x.max().alias("max"))),
        case("first_last", DF, agg(x.first().alias("first"), x.last().alias("last"))),
        case("counts", DF, agg(x.count().alias("count"), x.len().alias("len"), x.null_count().alias("null_count"),
                               x.drop_nulls().n_unique().alias("n_unique")),
             note="n_unique of the values without their nulls: n_unique does not count a null here (feature-gaps §9)"),
        case("arg_min_max", DF, agg(x.arg_min().cast(pl.Int64).alias("arg_min"), x.arg_max().cast(pl.Int64).alias("arg_max")),
             note="the position, cast to an Int64 on both sides: Polars' is a UInt32, ursus's a Uint64"),
        case("var_std", DF, agg(x.var().alias("var"), x.std().alias("std"), x.var(0).alias("var0"))),
        case("product", DF, agg(x.cast(pl.Float64).product().alias("product")),
             note="the floats' product: a Product is a Float64 for every number here (feature-gaps §9), "
                  "exact over these multiples of a quarter; Polars' Decimal(38, 2) product rounds"),
        case("any_all", DF, agg(x.any().alias("any"), x.all().alias("all")),
             note="Polars answers true where a value is not zero; ursus refuses a Decimal (Found)"),
        case("bitwise_and", DF, agg(x.bitwise_and().alias("and")), contains="requires an integer or a Boolean"),
        case("median_quantile", DF, agg(x.median().alias("median"), x.quantile(0.75, "nearest").alias("q75_nearest"),
                                        x.quantile(0.25, "linear").alias("q25_linear"))),
        case("implode", DF, agg(x.implode().alias("all"))),
        case("top_bottom_k", DF, agg(x.top_k(2).sort().alias("top"), x.bottom_k(2).sort().alias("bottom")),
             note="each list sorted: Polars' top_k has no order of its own"),
        case("skew_kurtosis", DF, agg(x.skew().alias("skew"), x.kurtosis().alias("kurtosis")), float="rel:1e-12",
             note="an ulp: Polars raises the second moment to the 1.5, ursus multiplies it by its square root"),
        case("nan_min", DF, agg(x.nan_min().alias("nan_min"))),
        case("entropy", POS, agg(x.entropy().alias("entropy")), float="rel:1e-15",
             note="Go's log against glibc's, as the math group holds them"),
        case("item", ONE, agg(x.item().alias("item"))),
        case("bounds", DF, agg(x.lower_bound().alias("lower"), x.upper_bound().alias("upper"))),
        case("is_sorted", DF, agg(x.is_sorted().alias("asc"), x.is_sorted(descending=True).alias("desc"))),
        case("mode", MODAL, agg(x.mode().alias("mode"))),
        case("gen", gen, agg(x.sum().alias("sum"), x.min().alias("min"), x.max().alias("max"), x.first().alias("first"),
                             x.last().alias("last"), x.mean().alias("mean"), x.median().alias("median"),
                             x.implode().alias("all")), float="rel:1e-15",
             note="Polars' mean of a Decimal is an ulp from the correctly rounded quotient in two groups; "
                  "ursus divides the exact sum"),
    ]
