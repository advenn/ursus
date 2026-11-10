"""The hiveparent group: a partition path with a parent directory component is refused,
as Polars refuses it (hiveparent_test.go, step 420); a backslash or `..` alone, which
make one segment, are written. The answer is the dataset's file names, a line each; the
param key names the column partitioned by.
"""

from __future__ import annotations

import polars as pl

from . import Case
from .gen import Gen
from .iotext import in_dir, listing


def part(name: str, key: str, values: list[str | None], contains: str = "", note: str = "") -> Case:
    df = pl.DataFrame({key: pl.Series(values, dtype=pl.String), "x": pl.Series(range(len(values)), dtype=pl.Int64)})

    def q(df: pl.DataFrame) -> str:
        def run(root):
            df.lazy().sink_parquet(pl.PartitionBy(str(root / "out"), key=key))
            return listing(root / "out")
        return in_dir(run)

    return Case(name, {"df": df}, q, params={"key": key}, contains=contains, note=note)


def cases() -> list[Case]:
    g = Gen(5030)
    pieces = ["a\\b", "..x", "x..", ".", "...", "a\\..b", "\\", "a\\\\b", "x"]
    safe = [pieces[i] for i in g.rng.integers(0, len(pieces), 40)]
    safe[0:2] = [None, ""]
    return [
        part("a_value", "k", ["..\\..\\..\\x"], contains="'..'"),
        part("a_keys_name", "..\\..\\x", ["v"], contains="'..'"),
        part("a_value_of_a_second_key", "k", ["y\\..\\.."], contains="'..'"),
        part("a_backslash_written", "k", ["a\\b"], note="one segment: written as it is"),
        part("dotdot_alone_written", "k", [".."], note="k=.. is one segment, not a parent directory"),
        # --- generated -------------------------------------------------------------------
        part("segments_gen", "k", safe, note="no backslash-split part is '..', so every value is written"),
    ]
