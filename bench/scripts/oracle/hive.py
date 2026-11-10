"""The hive group: a directory of Parquet files read as one frame, each key=value
directory a column of the files beneath it, and each file's path as a column
(hive_test.go, step 347), each Polars 2.1.0's answer.

The param files maps each file's relative path to the input frame it holds; the Go side
writes the same layout with SinkParquet, and scans it under the param options, the
scan_parquet arguments. A path column is stripped of the directory's root on both
sides. Not here, with the reason: the files a filter skips (FileStats, an
ursus counter); the names a directory holds that are passed over, which ursus and
Polars read otherwise (feature-gaps.md §9); and forty files on eight threads, a Go
differential against Concat.
"""

from __future__ import annotations

import pathlib

import polars as pl

from . import Case
from .gen import Gen
from .iotext import dataset, strip


def v(*vals: int) -> pl.DataFrame:
    return pl.DataFrame({"v": pl.Series(list(vals), dtype=pl.Int64)})


T = {"t/a=1/b=x/p0.parquet": v(1, 2), "t/a=2/b=y%20z/p0.parquet": v(3), "t/a=__HIVE_DEFAULT_PARTITION__/b=w/p0.parquet": v(4)}
DAYS = {"days/d=2024-01-02/p.parquet": v(1), "days/d=2024-01-03/p.parquet": v(2)}
HELD = {"x/a=1/p0.parquet": pl.DataFrame({"a": [9], "v": [1]})}
HELD_TEXT = {"bad/a=x/p0.parquet": pl.DataFrame({"a": [9], "v": [1]})}
KEYS_APART = {"ka/a=1/p.parquet": v(1), "ka/b=2/p.parquet": v(1)}
DEPTHS_APART = {"da/a=1/b=2/p.parquet": v(1), "da/a=3/p.parquet": v(1)}


def t(r: pathlib.Path, **kw) -> pl.LazyFrame:
    return pl.scan_parquet(str(r / "t"), **kw)


def hv(name: str, query, files=T, scan: dict | None = None, **opts) -> Case:
    """query takes the directory's root and scan_parquet's arguments, scan."""
    return dataset(name, files, query, scan=scan, **opts)


def cases() -> list[Case]:
    g = Gen(5029)
    parts = {f"m/g={k % 4}/h={chr(97 + k % 5)}/p{k}.parquet": g.frame(pl.Series("r", range(5 * k, 5 * k + 5), dtype=pl.Int64),
                                                                        g.ints(5, name="x")) for k in range(40)}
    return [
        hv("a_directory", lambda r, **o: t(r, **o).sort("v"),
           note="the keys after the files' columns, typed by their values, percent-decoded, the default partition null"),
        hv("the_keys_alone", lambda r, **o: t(r, **o).select("a", "b").sort("b")),
        hv("a_count", lambda r, **o: t(r, **o).select(pl.len())),
        hv("a_hive_schema", lambda r, **o: t(r, **o).sort("v"), scan={"hive_schema": {"a": pl.Int8, "b": pl.String}},
           note="every key named: Polars refuses a key its hive_schema does not name, where ursus infers it (feature-gaps.md §9)"),
        hv("dates", lambda r, **o: pl.scan_parquet(str(r / "days"), **o).sort("v"), files=DAYS),
        hv("dates_as_text", lambda r, **o: pl.scan_parquet(str(r / "days"), **o).sort("v"), files=DAYS,
           scan={"try_parse_hive_dates": False}),
        hv("a_filter_on_a_key", lambda r, **o: t(r, **o).filter(pl.col("a") == 2)),
        hv("a_key_and_a_column", lambda r, **o: t(r, **o).filter((pl.col("a") == 1) & (pl.col("v") > 1)).sort("v")),
        hv("a_keys_text", lambda r, **o: t(r, **o).filter(pl.col("b").str.starts_with("y"))),
        hv("a_keys_arithmetic", lambda r, **o: t(r, **o).filter(pl.col("a") + 1 == 3)),
        hv("a_null_key", lambda r, **o: t(r, **o).filter(pl.col("a").is_null())),
        hv("file_paths", lambda r, **o: t(r, **o).select("v", strip(r / "t", "path")).sort("v"), scan={"include_file_paths": "path"}),
        hv("a_path_named_as_a_key", lambda r, **o: t(r, **o), scan={"include_file_paths": "a"}, contains="file paths"),
        hv("a_key_the_file_holds", lambda r, **o: pl.scan_parquet(str(r / "x"), **o), files=HELD,
           note="the key's value replaces the file's, in the file's place and type"),
        hv("a_key_the_file_holds_as_text", lambda r, **o: pl.scan_parquet(str(r / "bad"), **o), files=HELD_TEXT, contains=""),
        hv("a_glob", lambda r, **o: pl.scan_parquet(str(r / "t" / "*" / "*" / "*.parquet"), **o).sort("v"), note="no keys unless asked"),
        hv("a_glob_with_keys", lambda r, **o: pl.scan_parquet(str(r / "t" / "*" / "*" / "*.parquet"), **o).sort("v"),
           scan={"hive_partitioning": True}),
        hv("one_file", lambda r, **o: pl.scan_parquet(str(r / "t" / "a=1" / "b=x" / "p0.parquet"), **o)),
        hv("one_file_with_keys", lambda r, **o: pl.scan_parquet(str(r / "t" / "a=1" / "b=x" / "p0.parquet"), **o),
           scan={"hive_partitioning": True}),
        hv("a_directory_without_keys", lambda r, **o: t(r, **o).sort("v"), scan={"hive_partitioning": False}),
        hv("keys_apart_refused", lambda r, **o: pl.scan_parquet(str(r / "ka"), **o), files=KEYS_APART, contains="partitioned by"),
        hv("depths_apart_refused", lambda r, **o: pl.scan_parquet(str(r / "da"), **o), files=DEPTHS_APART, contains="partitioned by"),
        # --- generated -------------------------------------------------------------------
        hv("many_files_gen", lambda r, **o: pl.scan_parquet(str(r / "m"), **o).sort("r"), files=parts),
    ]
