"""Write the Parquet files of every codec pyarrow and Polars write (step 520).

Step 520 bounds each page's decompression by the size the page declares, with codecs
ursus registers in arrow-go's registry (internal/source/parquet/codecs.go). No file a
writer made may be refused for it: internal/source/parquet's
TestEveryParquetFileReadsAsWithArrowGosCodecs reads each file this writes, with
ursus's codecs and with arrow-go's, and requires the same frame.

The same frame of 500 rows under each codec: `i` Int64 with nulls, `f` Float64,
`s` String of 50 values, which a writer keeps in a dictionary, `l` List(Int64) with
null lists and null elements, which arrow-go's column reader reads, and `b` Boolean.
Pages of 512 bytes, so each column chunk holds several.

A standalone script, apart from the Polars groups of this package, run once under each
writer from the repository root:

    bench/.venv/bin/python bench/scripts/oracle/record_codecs.py
    ~/ursus-scratch-archive/polars-latest/bin/python bench/scripts/oracle/record_codecs.py

The first writes pyarrow 25.0.1's files, V1 and V2 data pages; the second Polars
2.1.0's. Each writes testdata/parquet/codecs/<writer>_<codec>[_v2].parquet,
deterministically, and refuses another version of its writer.
"""

from __future__ import annotations

import pathlib
import sys

PYARROW = "25.0.1"
POLARS = "2.1.0"
ROWS = 500
PAGE = 512

ROOT = pathlib.Path(__file__).resolve().parents[3]
DIR = ROOT / "testdata" / "parquet" / "codecs"


def columns() -> dict[str, list]:
    i = [None if k % 13 == 0 else k * 7 - 300 for k in range(ROWS)]
    f = [k / 8 for k in range(ROWS)]
    s = ["v%03d" % (k * 37 % 50) for k in range(ROWS)]
    l = [None if k % 17 == 0 else [k, None, -k][: k % 4] for k in range(ROWS)]
    b = [k % 3 == 0 for k in range(ROWS)]
    return {"i": i, "f": f, "s": s, "l": l, "b": b}


def with_pyarrow() -> None:
    import pyarrow as pa
    import pyarrow.parquet as pq

    if pa.__version__ != PYARROW:
        sys.exit(f"pyarrow {pa.__version__}, not {PYARROW}")
    c = columns()
    t = pa.table(
        {
            "i": pa.array(c["i"], pa.int64()),
            "f": pa.array(c["f"], pa.float64()),
            "s": pa.array(c["s"], pa.string()),
            "l": pa.array(c["l"], pa.list_(pa.int64())),
            "b": pa.array(c["b"], pa.bool_()),
        }
    )
    for codec in ["snappy", "gzip", "brotli", "zstd", "lz4"]:
        for version, suffix in [("1.0", ""), ("2.0", "_v2")]:
            pq.write_table(
                t,
                DIR / f"pyarrow_{codec}{suffix}.parquet",
                compression=codec,
                data_page_size=PAGE,
                data_page_version=version,
                use_dictionary=["s"],
            )


def with_polars() -> None:
    import polars as pl

    if pl.__version__ != POLARS:
        sys.exit(f"Polars {pl.__version__}, not {POLARS}")
    c = columns()
    df = pl.DataFrame(
        {
            "i": pl.Series(c["i"], dtype=pl.Int64),
            "f": pl.Series(c["f"], dtype=pl.Float64),
            "s": pl.Series(c["s"], dtype=pl.String),
            "l": pl.Series(c["l"], dtype=pl.List(pl.Int64)),
            "b": pl.Series(c["b"], dtype=pl.Boolean),
        }
    )
    for codec in ["snappy", "gzip", "brotli", "zstd", "lz4"]:
        df.write_parquet(DIR / f"polars_{codec}.parquet", compression=codec, data_page_size=PAGE)


def main() -> None:
    DIR.mkdir(parents=True, exist_ok=True)
    try:
        import pyarrow  # noqa: F401
    except ImportError:
        with_polars()
        return
    with_pyarrow()


if __name__ == "__main__":
    main()
