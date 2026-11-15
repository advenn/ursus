"""Write the Arrow IPC files and streams with a Null column (step 522).

An all-null column is Polars' Null type, and every Arrow writer writes a Null column
as a field node and no buffer. arrow-go's IPC reader builds such a column as a Null
array of no buffers, which arrow-go's own array.Validate, which ursus called, refused:
every IPC input with a Null column was refused, whoever wrote it. testdata/oracle's
ipcnull group records Polars 2.1.0's reading of each file this writes, and ursus must
read the same through every IPC entry point (oracle_ipcnull_test.go).

Polars 2.1.0 writes a frame of five rows, `a` Int64, `n` Null, `s` String with a
null, `l` List(Null), `st` Struct(z: Null, y: Int64) and `arr` Array(Null, 2), as a
file and a stream, uncompressed, LZ4 and Zstandard; the same frame with no rows, and a
frame of only a Null column, each a file and a stream; and the frame in record batches
of two rows, a file (write_ipc_stream writes a frame holding a Null column as one
batch). pyarrow 25.0.1 writes a table of three rows, `a` int64, `n` null, `l`
list<null>, `ll` large_list<null>, `st` struct<z: null, y: int64>, `fsl`
fixed_size_list<null>[2], `m` map<string, null> and `d` dictionary<int32, null>, as a
file and a stream in each codec, with no rows, and in batches of one row.

A standalone script, apart from the Polars groups of this package, run once under each
writer from the repository root:

    ~/ursus-scratch-archive/polars-latest/bin/python bench/scripts/oracle/record_ipcnull.py
    bench/.venv/bin/python bench/scripts/oracle/record_ipcnull.py

The first writes Polars 2.1.0's files, the second pyarrow 25.0.1's. Each writes
testdata/ipc/<writer>_null*.arrow (a file) and .arrows (a stream), the same bytes on
every run, and refuses another version of its writer.
"""

from __future__ import annotations

import pathlib
import sys

PYARROW = "25.0.1"
POLARS = "2.1.0"

ROOT = pathlib.Path(__file__).resolve().parents[3]
DIR = ROOT / "testdata" / "ipc"

CODECS = [("", "uncompressed"), ("_lz4", "lz4"), ("_zstd", "zstd")]

# Every file written, by name; the ipcnull group reads each.
POLARS_FILES = [f"polars_null{s}.{e}" for s, _ in CODECS for e in ("arrow", "arrows")] + [
    f"polars_null_{k}.{e}" for k in ("empty", "only") for e in ("arrow", "arrows")
] + ["polars_null_batches.arrow"]
PYARROW_FILES = [f"pyarrow_null{s}.{e}" for s, _ in CODECS for e in ("arrow", "arrows")] + [
    f"pyarrow_null_{k}.{e}" for k in ("empty", "batches") for e in ("arrow", "arrows")
]


def with_polars() -> None:
    import polars as pl

    if pl.__version__ != POLARS:
        sys.exit(f"Polars {pl.__version__}, not {POLARS}")
    df = pl.DataFrame(
        {
            "a": pl.Series([1, 2, 3, 4, 5], dtype=pl.Int64),
            "n": pl.Series([None] * 5, dtype=pl.Null),
            "s": pl.Series(["x", None, "y", "", "z"], dtype=pl.String),
            "l": pl.Series([[None], [], None, [None, None, None], [None]], dtype=pl.List(pl.Null)),
            "st": pl.Series(
                [{"z": None, "y": 1}, None, {"z": None, "y": 3}, {"z": None, "y": None}, {"z": None, "y": 5}],
                dtype=pl.Struct({"z": pl.Null, "y": pl.Int64}),
            ),
            "arr": pl.Series([[None, None], None, [None, None], [None, None], None], dtype=pl.Array(pl.Null, 2)),
        }
    )

    def write(frame: pl.DataFrame, stem: str, compression: str = "uncompressed") -> None:
        frame.write_ipc(DIR / f"{stem}.arrow", compression=compression)
        frame.write_ipc_stream(DIR / f"{stem}.arrows", compression=compression)

    for suffix, codec in CODECS:
        write(df, f"polars_null{suffix}", codec)
    write(df.clear(), "polars_null_empty")
    write(pl.DataFrame({"n": pl.Series([None] * 4, dtype=pl.Null)}), "polars_null_only")
    df.write_ipc(DIR / "polars_null_batches.arrow", record_batch_size=2)


def with_pyarrow() -> None:
    import pyarrow as pa
    import pyarrow.ipc as ipc

    if pa.__version__ != PYARROW:
        sys.exit(f"pyarrow {pa.__version__}, not {PYARROW}")
    t = pa.table(
        {
            "a": pa.array([1, 2, 3], pa.int64()),
            "n": pa.nulls(3),
            "l": pa.array([[None], [], None], pa.list_(pa.null())),
            "ll": pa.array([[None, None], None, []], pa.large_list(pa.null())),
            "st": pa.array(
                [{"z": None, "y": 1}, None, {"z": None, "y": 3}], pa.struct([("z", pa.null()), ("y", pa.int64())])
            ),
            "fsl": pa.array([[None, None], None, [None, None]], pa.list_(pa.null(), 2)),
            "m": pa.array([[("k", None)], None, []], pa.map_(pa.string(), pa.null())),
            "d": pa.nulls(3).dictionary_encode(),
        }
    )

    def write(table: pa.Table, stem: str, compression: str | None = None, rows: int | None = None) -> None:
        opts = ipc.IpcWriteOptions(compression=compression)
        with ipc.new_file(DIR / f"{stem}.arrow", table.schema, options=opts) as w:
            w.write_table(table, max_chunksize=rows)
        with ipc.new_stream(DIR / f"{stem}.arrows", table.schema, options=opts) as w:
            w.write_table(table, max_chunksize=rows)

    for suffix, codec in CODECS:
        write(t, f"pyarrow_null{suffix}", None if codec == "uncompressed" else codec)
    write(t.slice(0, 0), "pyarrow_null_empty")
    write(t, "pyarrow_null_batches", rows=1)


def main() -> None:
    try:
        import pyarrow  # noqa: F401
    except ImportError:
        with_polars()
        return
    with_pyarrow()


if __name__ == "__main__":
    main()
