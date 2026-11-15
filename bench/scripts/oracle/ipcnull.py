"""The ipcnull group: Arrow IPC files and streams with a Null column (step 522), each
Polars 2.1.0's reading of it.

Every Arrow writer writes a Null column as a field node and no buffer, and arrow-go's
IPC reader builds it as a Null array of no buffers, which arrow-go's own
array.Validate, which ursus called, refused: ursus refused every IPC input with a Null
column, Polars' own among them, since Polars writes an all-null column as Null.
record_ipcnull.py writes the files, Polars 2.1.0's and pyarrow 25.0.1's, into
testdata/ipc; a case's param path names one, and its answer is Polars' read_ipc, or
read_ipc_stream, of it. The Go side reads the file through every IPC entry point and
through arrow-go's records handed to ScanArrowRecords and ScanArrow, each the same
frame (oracle_ipcnull_test.go). Polars 2.1 reads pyarrow's map<string, null> as its
Map type, where ursus reads an Arrow map as the List(Struct(key, value)) it is on disk
(feature-gaps.md, "Parquet and Arrow map columns read as List(Struct)"), so those cases
cast it to that.
"""

from __future__ import annotations

import polars as pl

from . import ROOT, Case
from .record_ipcnull import POLARS_FILES, PYARROW_FILES

ENTRIES = pl.List(pl.Struct({"key": pl.String, "value": pl.Null}))


def read(path: str, maps: bool):
    def query() -> pl.DataFrame:
        p = ROOT / path
        df = pl.read_ipc_stream(p) if path.endswith(".arrows") else pl.read_ipc(p)
        return df.with_columns(pl.col("m").cast(ENTRIES)) if maps else df

    return query


def name(file: str) -> str:
    stem, ext = file.rsplit(".", 1)
    return stem + ("_stream" if ext == "arrows" else "_file")


def cases() -> list[Case]:
    out = []
    for file in POLARS_FILES:
        out.append(Case(name(file), {}, read(f"testdata/ipc/{file}", False), params={"path": f"testdata/ipc/{file}"},
                        note="written by Polars 2.1.0 (record_ipcnull.py)"))
    for file in PYARROW_FILES:
        out.append(Case(name(file), {}, read(f"testdata/ipc/{file}", True), params={"path": f"testdata/ipc/{file}"},
                        note="written by pyarrow 25.0.1 (record_ipcnull.py); its map cast to the List(Struct) ursus reads"))
    return out
