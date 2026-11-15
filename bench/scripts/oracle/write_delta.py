"""Write Parquet files of pyarrow's delta-encoded pages, and their PLAIN twin (step 531).

Step 531 checks each delta-packed run of a DELTA_BINARY_PACKED,
DELTA_LENGTH_BYTE_ARRAY or DELTA_BYTE_ARRAY data page before arrow-go decodes it
(internal/source/parquet/deltapage.go). No file a writer made may be refused for it:
readfuzz's TestPyarrowDeltaFilesReadAsTheirPlainTwin reads each delta file this
writes and requires the frame of its PLAIN twin, the same table with no encoding but
PLAIN, which the reader decodes apart from the delta decoders.

One table of 500 rows: integers under DELTA_BINARY_PACKED, INT64 and INT32, with
nulls and without, spanning their types' ranges, and a date, a timestamp and a
UInt32 over them; strings and binaries under DELTA_LENGTH_BYTE_ARRAY and
DELTA_BYTE_ARRAY, with nulls, empty values and shared prefixes; a List(Int64) with
null lists and null elements, and a Struct of an Int64 and a String, each under its
leaves' delta encoding. Row groups of 200 rows and pages of 256 bytes, so each chunk
holds many pages, some ending a block part way. The delta files are V1 pages
uncompressed and V2 pages under Zstandard; the twin is V1 pages under Zstandard.

Polars 2.1.0 writes no delta encoding (its write_parquet takes none, and writes
PLAIN and dictionaries), so pyarrow's are the other writer's here.

A standalone script, apart from the Polars groups of this package: from the
repository root, with pyarrow at the version below installed,

    python bench/scripts/oracle/write_delta.py

writes testdata/parquet/delta/pyarrow_*.parquet, deterministically.
"""

from __future__ import annotations

import datetime
import pathlib
import random
import sys

import pyarrow as pa
import pyarrow.parquet as pq

PYARROW = "25.0.1"
ROWS = 500
SEED = 531

ROOT = pathlib.Path(__file__).resolve().parents[3]
DIR = ROOT / "testdata" / "parquet" / "delta"


def table() -> pa.Table:
    rng = random.Random(SEED)

    def maybe(v, k: int, every: int):
        return None if k % every == 3 else v

    def walk(lo: int, hi: int) -> list[int]:
        # A random walk with jumps across the type's range, so a block's widths vary
        # from a few bits to its type's.
        out, v = [], 0
        for k in range(ROWS):
            r = rng.random()
            if r < 0.02:
                v = rng.choice([lo, hi])
            elif r < 0.1:
                v = rng.randint(lo, hi)
            else:
                v = max(lo, min(hi, v + rng.randint(-1000, 1000)))
            out.append(v)
        return out

    i64 = walk(-(2**63), 2**63 - 1)
    i32 = walk(-(2**31), 2**31 - 1)
    words = ["", "a", "MAIL", "TRUCK", "é漢字", "DELIVER IN PERSON"]
    s = [maybe(f"{words[k % len(words)]}{rng.randint(0, 10**rng.randint(0, 6))}", k, 11) for k in range(ROWS)]
    keys = sorted(f"key/{rng.randint(0, 300):04d}/{rng.choice(words)}" for _ in range(ROWS))
    lists = []
    for k in range(ROWS):
        if k % 17 == 5:
            lists.append(None)
        else:
            lists.append([maybe(i64[(k + j) % ROWS], j, 4) for j in range(k % 5)])
    epoch = datetime.date(1970, 1, 1)
    return pa.table(
        {
            "i64": pa.array([maybe(v, k, 7) for k, v in enumerate(i64)], pa.int64()),
            "i64_req": pa.array(i64, pa.int64()),
            "i32": pa.array([maybe(v, k, 5) for k, v in enumerate(i32)], pa.int32()),
            "u32": pa.array([v % 2**32 for v in i32], pa.uint32()),
            "date": pa.array([epoch + datetime.timedelta(days=v % 40000) for v in i32], pa.date32()),
            "ts": pa.array([v // 1000 for v in i64], pa.timestamp("us")),
            "s": pa.array(s, pa.string()),
            "keys": pa.array([maybe(v, k, 13) for k, v in enumerate(keys)], pa.string()),
            "bin": pa.array([maybe(v.encode()[::-1], k, 9) for k, v in enumerate(keys)], pa.binary()),
            "l": pa.array(lists, pa.list_(pa.int64())),
            "st": pa.array(
                [maybe({"x": i64[k], "y": s[k]}, k, 19) for k in range(ROWS)],
                pa.struct([("x", pa.int64()), ("y", pa.string())]),
            ),
        }
    )


def main() -> int:
    if pa.__version__ != PYARROW:
        print(f"pyarrow {pa.__version__}, not {PYARROW}", file=sys.stderr)
        return 1
    DIR.mkdir(parents=True, exist_ok=True)
    t = table()
    encodings = {
        "i64": "DELTA_BINARY_PACKED",
        "i64_req": "DELTA_BINARY_PACKED",
        "i32": "DELTA_BINARY_PACKED",
        "u32": "DELTA_BINARY_PACKED",
        "date": "DELTA_BINARY_PACKED",
        "ts": "DELTA_BINARY_PACKED",
        "s": "DELTA_LENGTH_BYTE_ARRAY",
        "keys": "DELTA_BYTE_ARRAY",
        "bin": "DELTA_BYTE_ARRAY",
        "l.list.element": "DELTA_BINARY_PACKED",
        "st.x": "DELTA_BINARY_PACKED",
        "st.y": "DELTA_LENGTH_BYTE_ARRAY",
    }
    common = dict(use_dictionary=False, row_group_size=200, data_page_size=256, write_statistics=False)
    pq.write_table(t, DIR / "pyarrow_delta_v1.parquet", compression="none", data_page_version="1.0",
                   column_encoding=encodings, **common)
    pq.write_table(t, DIR / "pyarrow_delta_v2_zstd.parquet", compression="zstd", data_page_version="2.0",
                   column_encoding=encodings, **common)
    pq.write_table(t, DIR / "pyarrow_plain.parquet", compression="zstd", data_page_version="1.0", **common)
    return 0


if __name__ == "__main__":
    sys.exit(main())
