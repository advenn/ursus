"""The ipcfiles group: scan_ipc of several files, a glob, a list and a hive directory,
and include_file_paths (ipcfiles_test.go, step 360), and Polars' own files in
testdata/ipc (ipc_test.go, step 332), each Polars 2.1.0's answer.

The param files maps each file's relative path to the input frame it holds; the Go side
writes the same layout with WriteIPC, and scans it under the param options, the
scan_ipc arguments. A path column is stripped of the directory's root on both sides.
A file of testdata/ipc is the param path, written by Polars 1.44.1: what Polars 2.1
reads from its bytes is what ursus must. Not here, with the reason: a filter on a key
ruling a file out before it is opened, which a file that is no IPC file proves; and
the serial and the blocked reading paths, a Go differential.
"""

from __future__ import annotations

import pathlib

import polars as pl

from . import ROOT, Case
from .gen import Gen
from .iotext import dataset, strip

FILES = {
    "p1.arrow": pl.DataFrame({"a": [1, 2], "b": ["x", "y"]}),
    "p2.arrow": pl.DataFrame({"a": [3], "b": ["z"]}),
    "p3.arrow": pl.DataFrame({"b": ["w"], "a": [4]}),
    "p4.arrow": pl.DataFrame({"a": [5]}),
    "p5.arrow": pl.DataFrame({"a": [1.5], "b": ["q"]}),
    "d/k=1/j=a/x.arrow": pl.DataFrame({"a": [1]}),
    "d/k=2/j=b/y.arrow": pl.DataFrame({"a": [2]}),
}


def ipc(df: pl.DataFrame, p: pathlib.Path) -> None:
    df.write_ipc(p)


def ds(name: str, query, files=FILES, scan: dict | None = None, **opts) -> Case:
    """query takes the directory's root and scan_ipc's arguments, scan."""
    return dataset(name, files, query, write=ipc, scan=scan, **opts)


def fixture(name: str, file: str, stream: bool = False) -> Case:
    """A file of testdata/ipc, read by read_ipc, or read_ipc_stream."""
    path = f"testdata/ipc/{file}"
    read = pl.read_ipc_stream if stream else pl.read_ipc
    return Case(name, {}, lambda: read(ROOT / path), params={"path": path},
                note="written by Polars 1.44.1: string views, a large list, a struct")


def paths(root: pathlib.Path, *names: str) -> list[str]:
    return [str(root / n) for n in names]


def cases() -> list[Case]:
    g = Gen(5028)
    big = g.frame(pl.Series("r", range(90), dtype=pl.Int64), g.floats(90, name="f"), g.strings(90, name="s"))
    parts = {f"g/k={k}/p.arrow": big.slice(30 * k, 30) for k in range(3)}
    return [
        ds("a_glob", lambda r, **o: pl.scan_ipc(str(r / "p[12].arrow"), **o)),
        ds("a_list", lambda r, **o: pl.scan_ipc(paths(r, "p2.arrow", "p1.arrow"), **o)),
        ds("by_name", lambda r, **o: pl.scan_ipc(paths(r, "p1.arrow", "p3.arrow"), **o), note="the second file's columns in another order"),
        ds("a_directorys_keys", lambda r, **o: pl.scan_ipc(str(r / "d"), **o)),
        ds("a_glob_no_keys", lambda r, **o: pl.scan_ipc(str(r / "d" / "**" / "*.arrow"), **o)),
        ds("a_glob_with_keys", lambda r, **o: pl.scan_ipc(str(r / "d" / "**" / "*.arrow"), **o), scan={"hive_partitioning": True}),
        ds("the_paths", lambda r, **o: pl.scan_ipc(str(r / "d"), **o).with_columns(strip(r / "d", "p")), scan={"include_file_paths": "p"}),
        ds("a_filter_on_a_key", lambda r, **o: pl.scan_ipc(str(r / "d"), **o).filter(pl.col("k") == 2)),
        ds("a_key_alone", lambda r, **o: pl.scan_ipc(str(r / "d"), **o).select("k")),
        ds("a_keys_type_given", lambda r, **o: pl.scan_ipc(str(r / "d"), **o), scan={"hive_schema": {"k": pl.Int8, "j": pl.String}},
           note="every key named: Polars refuses a key its hive_schema does not name, where ursus infers it (feature-gaps.md §9)"),
        ds("under_a_limit", lambda r, **o: pl.scan_ipc(str(r / "p[12].arrow"), **o).head(2)),
        ds("one_files_path", lambda r, **o: pl.scan_ipc(str(r / "p1.arrow"), **o).with_columns(strip(r, "p")),
           scan={"include_file_paths": "p"}),
        # --- refused, as Polars refuses them -----------------------------------------------
        ds("a_column_missing", lambda r, **o: pl.scan_ipc(paths(r, "p1.arrow", "p4.arrow"), **o), contains='no column "b"'),
        ds("a_column_more", lambda r, **o: pl.scan_ipc(paths(r, "p4.arrow", "p1.arrow"), **o), contains='a column "b"'),
        ds("another_type", lambda r, **o: pl.scan_ipc(paths(r, "p1.arrow", "p5.arrow"), **o), contains="Float64"),
        ds("a_path_columns_name_taken", lambda r, **o: pl.scan_ipc(str(r / "d"), **o), scan={"include_file_paths": "k"},
           contains="file paths"),
        # --- Polars' own files -------------------------------------------------------------
        fixture("polars_file", "polars_types.arrow"),
        fixture("polars_file_lz4", "polars_types_lz4.arrow"),
        fixture("polars_file_zstd", "polars_types_zstd.arrow"),
        fixture("polars_stream", "polars_types.arrows", stream=True),
        # --- generated -------------------------------------------------------------------
        ds("a_directory_gen", lambda r, **o: pl.scan_ipc(str(r / "g"), **o).sort("r"), files=parts),
        ds("a_glob_gen", lambda r, **o: pl.scan_ipc(str(r / "g" / "**" / "*.arrow"), **o).sort("r"), files=parts),
    ]
