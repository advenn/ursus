"""The frameconv group: a frame's corr, gather, the horizontal reductions and fold
(frameconv_test.go, step 327). corr is NumPy's corrcoef in Polars; ursus's sums are
exact, and on a few rows the two agree to the bit, where the generated case is held
to rel:1e-12. Polars' to_numpy cannot hand NumPy a Decimal, so that case asks of the
floats the Decimal holds. Not here: row, item, the column accessors and the lazy
schema, Go's own API; the long parallel gather and corr, Go differentials.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

c = pl.col
NUM = pl.DataFrame({"a": [1, 2, 3, 4], "b": [2.0, 4.0, 5.0, 9.0]})
DF = NUM.with_columns(pl.Series("s", ["x", "y", "z", "w"]))
TEN = pl.DataFrame({"x": [1.0, 2, 3, 4, 5, 6, 7, 8, 9, 10], "y": [2.0, 3, 1, 5, 4, 8, 7, 6, 10, 9]})
WITH_NULL = pl.DataFrame({"a": [1, 2, None, 4], "b": [2.0, 4.0, 5.0, 9.0], "c": [1.0, 1, 1, 1]})
CLIP = pl.DataFrame({"a": [0.1, 0.3, 5.6], "b": [1.0, 2, 4]})
DEC = pl.DataFrame({"a": [1.0, 2.0, 4.0], "b": [1, 2, 5]})
HN = pl.DataFrame({"a": [1.0, None], "b": [3.0, None]})
HS = pl.DataFrame({"a": [1.0, 1.0], "b": [2.0, None]})


def series(f):
    return lambda df, **_: f(df).to_frame()


def cases() -> list[Case]:
    g = Gen(5025)
    gen = g.frame(g.floats(150, edges=False, null_rate=0, name="x"), g.floats(150, edges=False, null_rate=0, name="y"),
                  g.floats(150, edges=False, null_rate=0.05, name="z"))
    idx = [int(v) for v in g.rng.integers(-150, 150, 80)]
    return [
        case("corr", NUM, lambda df: df.corr(), note="NumPy's own, to the bit; the two sides of the diagonal differ"),
        case("corr_ten", TEN, lambda df: df.corr()),
        case("corr_with_null_and_constant", WITH_NULL, lambda df: df.corr()),
        case("corr_no_rows", NUM.head(0), lambda df: df.corr(), note="NaN throughout, as NumPy's is"),
        case("corr_one_row", NUM.head(1), lambda df: df.corr()),
        case("corr_clipped", CLIP, lambda df: df.corr(), float="rel:1e-15",
             note="clipped as NumPy clips: a's own correlation comes to 1.0000000000000002; the other cells an ulp from NumPy's, "
                  "ursus's sums exact (§9), as the old test allowed"),
        case("corr_decimal", DEC.select(c("a").cast(pl.Decimal(10, 2)), "b"), lambda df: df.with_columns(c("a").cast(pl.Float64)).corr(),
             float="rel:1e-15", note="Polars' to_numpy cannot hand NumPy a Decimal: asked of the floats it holds; two ulps, as the old test allowed"),
        case("gather", DF, lambda df: df.gather([2, 0, -1])),
        case("gather_null_on_oob", DF, lambda df: df.gather([9, 1], null_on_oob=True)),
        case("sum_horizontal", NUM, series(lambda df: df.sum_horizontal(ignore_nulls=True))),
        case("min_horizontal", NUM, series(lambda df: df.min_horizontal())),
        case("max_horizontal", NUM, series(lambda df: df.max_horizontal())),
        case("mean_horizontal_null_skipped", HN, series(lambda df: df.mean_horizontal(ignore_nulls=True))),
        case("sum_horizontal_null_not_ignored", HS, series(lambda df: df.sum_horizontal(ignore_nulls=False))),
        case("fold", NUM, series(lambda df: df.fold(lambda acc, x: acc + x))),
        case("corr_gen", gen, lambda df: df.corr(), float="rel:1e-12", note="NumPy's pairwise sums against exact ones over 150 rows"),
        case("gather_gen", gen, lambda df: df.gather(idx), params={"idx": idx}, note="indices from either end, as a param"),
        case("gather_gen_null_on_oob", gen, lambda df: df.gather([i * 2 for i in idx], null_on_oob=True), params={"idx": [i * 2 for i in idx]}),
        case("horizontal_gen", gen, lambda df: df.select(df.sum_horizontal(ignore_nulls=True), df.min_horizontal(), df.max_horizontal(),
                                                         df.mean_horizontal(ignore_nulls=False).alias("mean")), float="rel:1e-15",
             note="a row's sum of three floats: the summation order's last bit"),
    ]
