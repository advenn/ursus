"""The trig group: the trigonometric and hyperbolic functions, degrees, radians and
cot (trig_test.go, step 114). The literal frame is the Go test's nine values and a
null; a generated case follows. The Go oracle against the math package stays in the
Go test.

Each function is Go's against glibc's, a few ulps apart at most, so the libm ones are
held to rel:1e-15 (rel:1e-6 at Float32); degrees and radians are a multiplication and
compared by bits.
"""

from __future__ import annotations

import math

import polars as pl

from . import Case, case
from .gen import Gen

nan, inf = float("nan"), float("inf")
x = pl.col("x")
IN = [-2, -1, -0.5, 0, 0.25, 0.5, 1, 2, math.pi / 3, None]
LIBM = {"sin": x.sin(), "cos": x.cos(), "tan": x.tan(), "arcsin": x.arcsin(), "arccos": x.arccos(), "arctan": x.arctan(),
        "sinh": x.sinh(), "cosh": x.cosh(), "tanh": x.tanh(), "arcsinh": x.arcsinh(), "arccosh": x.arccosh(), "arctanh": x.arctanh(),
        "cot": x.cot()}
ANGLES = {"degrees": x.degrees(), "radians": x.radians()}


def typed(vals: list, dtype: pl.DataType) -> pl.DataFrame:
    return pl.DataFrame({"x": pl.Series("x", vals, dtype=dtype)})


def named(ops: dict[str, pl.Expr]):
    return lambda df: df.select(*(e.alias(n) for n, e in ops.items()))


def cases() -> list[Case]:
    g = Gen(4980)
    gen = g.frame(g.floats(240, lo=-50, hi=50, edges=False))
    gen = gen.with_columns(pl.Series("x", g._place(gen["x"].to_list(), 0.1, [nan, inf, -inf, -0.0, 0.0, 1.0, -1.0, 0.5, -0.5]), dtype=pl.Float64))
    gen_f32 = g.frame(g.floats(120, lo=-50, hi=50, dtype=pl.Float32))
    return [
        case("trig", typed(IN, pl.Float64), named(LIBM), float="rel:1e-15", note="Go's against glibc's; out of the domain NaN in both"),
        case("angles", typed(IN, pl.Float64), named(ANGLES)),
        case("int_widens_f32_stays", pl.DataFrame({"i": pl.Series([0, 1], dtype=pl.Int64), "f": pl.Series([0, 1], dtype=pl.Float32)}),
             lambda df: df.select(pl.col("i").sin().alias("i"), pl.col("f").cos().alias("f")), float="rel:1e-6",
             note="sin of an integer is a Float64, cos of a Float32 a Float32"),
        case("trig_gen", gen, named(LIBM), float="rel:1e-15", note="Go's against glibc's"),
        case("angles_gen", gen, named(ANGLES)),
        case("trig_f32_gen", gen_f32, named(LIBM), float="rel:1e-6", note="each rounds its own double once to a float32"),
        case("angles_f32_gen", gen_f32, named(ANGLES), float="rel:1e-6", note="Polars multiplies at Float32, ursus the double rounded once"),
    ]
