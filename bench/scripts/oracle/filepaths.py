"""The filepaths group: include_file_paths for CSV and NDJSON, a String column of each
row's file path after the files' columns, the path as given (filepaths_test.go, step
359), each Polars 2.1.0's answer.

The param files maps each file's relative path to its text, scanned under the param
options, the scan_csv or scan_ndjson arguments; a path column is stripped of the
directory's root on both sides. Not here, with the reason: bytes read under a
name, which is the caller's here and "in-mem" in Polars; the big files cut into blocks
on four threads, a Go differential; and a JSON document's key of the path's name past
the inferred objects, a refusal the jsonread group's late key covers.
"""

from __future__ import annotations

import pathlib

import polars as pl

from . import Case
from .gen import Gen
from .iotext import strip, textfiles

FILES = {"p1.csv": "a,b\n1,x\n2,y\n", "p2.csv": "a,b\n3,z\n", "p1.ndjson": '{"a":1}\n{"a":2}\n', "p2.ndjson": '{"a":3}\n'}


PATH = {"include_file_paths": "path"}


def fp(name: str, query, files=FILES, scan: dict | None = None, **opts) -> Case:
    """query takes the directory's root and the scan's arguments, scan, the path column
    path's by default."""
    return textfiles(name, files, query, scan=PATH if scan is None else scan, **opts)


def csv(r: pathlib.Path, *names: str, **o) -> pl.LazyFrame:
    src = str(r / "p*.csv") if not names else [str(r / n) for n in names]
    return pl.scan_csv(src, **o).with_columns(strip(r, "path"))


def nd(r: pathlib.Path, *names: str, **o) -> pl.LazyFrame:
    src = str(r / "p*.ndjson") if not names else [str(r / n) for n in names]
    return pl.scan_ndjson(src, **o).with_columns(strip(r, "path"))


def cases() -> list[Case]:
    g = Gen(5031)
    big = {f"big{i}.csv": g.frame(g.ints(60 * (i + 1), name="a"), g.strings(60 * (i + 1), name="b")).write_csv() for i in range(3)}
    bigs = [f"big{i}.csv" for i in range(3)]
    return [
        fp("a_glob", lambda r, **o: csv(r, **o)),
        fp("a_list_as_given", lambda r, **o: pl.scan_csv([str(r / "p1.csv"), str(r) + "/./p2.csv"], **o).with_columns(strip(r, "path"))),
        fp("one_file", lambda r, **o: csv(r, "p1.csv", **o)),
        fp("the_path_alone", lambda r, **o: csv(r, **o).select("path")),
        fp("a_filter_on_it", lambda r, **o: csv(r, **o).filter(pl.col("path").str.ends_with("p2.csv")).select("a")),
        fp("under_a_limit", lambda r, **o: csv(r, **o).head(2).select("path")),
        fp("a_schema_given", lambda r, **o: csv(r, "p1.csv", **o), scan={**PATH, "schema": {"a": pl.Int64, "b": pl.String}}),
        fp("ndjsons_glob", lambda r, **o: nd(r, **o)),
        fp("ndjsons_path_alone", lambda r, **o: nd(r, "p2.ndjson", "p1.ndjson", **o).select("path")),
        fp("ndjsons_schema_given", lambda r, **o: nd(r, "p1.ndjson", **o), scan={**PATH, "schema": {"a": pl.Int32}}),
        # --- refused, as Polars refuses them -----------------------------------------------
        fp("a_field_past_the_files", lambda r, **o: pl.scan_csv(str(r / "m.csv"), **o), files={"m.csv": "a,b\n1,2\n3,4,5\n"},
           contains="", note="the path column is no file's: a record of a field more is refused still"),
        fp("csvs_name_taken", lambda r, **o: pl.scan_csv(str(r / "p1.csv"), **o), scan={"include_file_paths": "a"}, contains="file paths"),
        fp("ndjsons_name_taken", lambda r, **o: pl.scan_ndjson(str(r / "p1.ndjson"), **o), scan={"include_file_paths": "a"},
           contains="file paths"),
        # --- generated -------------------------------------------------------------------
        fp("many_rows_gen", lambda r, **o: csv(r, *bigs, **o), files=big),
        fp("a_count_a_file_gen", lambda r, **o: csv(r, *bigs, **o).group_by("path").agg(pl.len().alias("n")).sort("path"), files=big),
    ]
