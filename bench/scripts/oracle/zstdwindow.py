"""The zstdwindow group: Zstandard streams, and Arrow IPC and Parquet files, whose
frames declare windows about 128 MiB, the limit the reference decoder keeps by default
(step 528), each Polars 2.1.0's reading of it.

klauspost's decoder, which ursus decompresses with, makes the whole window a frame
declares before it decodes a block, so a few bytes declaring 512 MiB cost 512 MiB.
Polars decompresses through libzstd, which refuses a window past 128 MiB in a stream
unless told otherwise, and ursus refuses one now too: a CSV, an NDJSON, a JSON or an
Arrow IPC file's buffer. Not a Parquet page, which Polars 2.1 and pyarrow read
whatever window its frame declares (Polars 1.44.1 refused one past 128 MiB), and ursus
decodes into the page's buffer, making no window (step 520).

record_zstdwindow.py writes the files into testdata/zstd: the zstd CLI's streams, and
pyarrow's files with their frames' headers changed to declare 128 or 256 MiB. A case's
param path names one, and its answer is Polars' read_csv, read_ndjson, read_json,
read_ipc, read_ipc_stream or read_parquet of it. zstd's refusal of a text's stream
reaches Python as an OSError, which is recorded as a ValueError of its message. ursus's
refusal must name the limit, "past 128 MiB".
"""

from __future__ import annotations

from typing import Any, Callable

import polars as pl

from . import ROOT, Case
from .record_zstdwindow import CLI_FILES, PYARROW_FILES

READERS: dict[str, Callable[..., Any]] = {
    "csv.zst": pl.read_csv,
    "ndjson.zst": pl.read_ndjson,
    "json.zst": pl.read_json,
    "arrow": pl.read_ipc,
    "arrows": pl.read_ipc_stream,
    "parquet": pl.read_parquet,
}


def read(path: str) -> Callable[[], pl.DataFrame]:
    reader = READERS[path.split(".", 1)[1]]

    def query() -> pl.DataFrame:
        try:
            return reader(ROOT / path)
        except OSError as e:
            raise ValueError(f"OSError: {e}") from None

    return query


def cases() -> list[Case]:
    out = []
    for file, past in CLI_FILES:
        path = f"testdata/zstd/{file}"
        out.append(Case(file.replace(".", "_"), {}, read(path), params={"path": path},
                        note="written by the zstd CLI 1.5.7 (record_zstdwindow.py)",
                        contains="past 128 MiB" if past else ""))
    for file, past in PYARROW_FILES:
        path = f"testdata/zstd/{file}"
        parquet = file.endswith(".parquet")
        out.append(Case(file.replace(".", "_"), {}, read(path), params={"path": path},
                        note="written by pyarrow 25.0.1, its frames declaring the window (record_zstdwindow.py)"
                        + ("; a Parquet page is decoded without the limit" if parquet else ""),
                        contains="past 128 MiB" if past and not parquet else ""))
    return out
