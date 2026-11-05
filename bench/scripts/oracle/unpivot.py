"""The unpivot group: Unpivot's values and types (unpivot_test.go; P2 of
v0.6.7-scope.md, step 304). ursus gives each row's columns together, then the next
row's, where Polars gives every row's first column, then every row's second
(feature-gaps.md §9); its order depends on the batches, ursus's does not. So each case
asks Polars with a row index, sorts stably on it, and drops it: Polars' own values in
the row-major order. Not here: the schema and nullability checks, the plan, the
batch-size differential and the refusals.
"""

from __future__ import annotations

import polars as pl

from . import Case, case
from .gen import Gen

UP = pl.DataFrame({"id": [1, 2, 3], "region": ["eu", "us", "apac"], "jan": [10, 30, 50], "feb": [20, None, 60]})
ABS = pl.DataFrame({"id": [1, 2], "a": [3, 4], "b": [5, 6]})
ROW_MAJOR = "Polars' column-major rows sorted stably by a row index, the row-major order here (§9)"


def rowmajor(**o):
    def q(df: pl.DataFrame) -> pl.DataFrame:
        index = o.get("index")
        index = [] if index is None else ([index] if isinstance(index, str) else list(index))
        opts = dict(o)
        opts["index"] = ["__r", *index]
        return df.with_row_index("__r").unpivot(**opts).sort("__r", maintain_order=True).drop("__r")
    return q


def cases() -> list[Case]:
    g = Gen(5018)
    gen = g.frame(pl.Series("id", range(100), dtype=pl.Int64), g.floats(100, name="a"), g.floats(100, name="b"), g.floats(100, name="c"))
    return [
        case("row_major", UP, rowmajor(on=["jan", "feb"], index=["id", "region"]), note=ROW_MAJOR),
        case("promotes_int32_int64", pl.DataFrame({"id": [1], "small": pl.Series([7], dtype=pl.Int32), "big": [1 << 40]}),
             rowmajor(on=["small", "big"], index="id"), note="the Int32 widened, not reinterpreted"),
        case("index_only", ABS, rowmajor(index="id"), note="On left empty melts every column not in Index; " + ROW_MAJOR),
        case("every_column", ABS, rowmajor(), note=ROW_MAJOR),
        case("named", ABS, rowmajor(index="id", variable_name="col", value_name="val"), note=ROW_MAJOR),
        case("unpivot_gen", gen, rowmajor(on=["a", "b", "c"], index="id"), note=ROW_MAJOR),
    ]
