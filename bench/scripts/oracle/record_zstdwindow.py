"""Write the Zstandard files of step 528: frames declaring windows about 128 MiB.

Step 528 refuses a Zstandard frame declaring a window past 128 MiB, the reference
decoder's default limit (libzstd's ZSTD_WINDOWLOG_LIMIT_DEFAULT, 27), which Polars
keeps through it; zstd writes such a frame only in its --long modes past 27.
testdata/oracle's zstdwindow group records Polars 2.1.0's reading of each file this
writes, and ursus must answer the same (oracle_zstdwindow_test.go).

The zstd CLI 1.5.7 compresses a CSV, an NDJSON and a JSON text read from its standard
input, so that the window its frame declares is the one asked for, however short the
text: windows of 1, 8 and 64 MiB (--zstd=wlog) and of 128, 256 and 512 MiB (--long) for
the CSV, of 128 and 256 MiB for the NDJSON and the JSON. And a CSV of 140 MiB given its
size (--stream-size) under --long=28, which zstd writes as a single segment, whose
window is its content size: 13 KB declaring 140 MiB.

pyarrow 25.0.1 writes a table as an Arrow IPC file and stream and as a Parquet file,
compressed by Zstandard; the header of each frame is then changed in place to declare
a window of 128 or 256 MiB (declare), which leaves the frame what it was: a larger
window holds whatever a smaller one does. pyarrow writes no such frame itself, Arrow
C++ compressing a buffer or a page in one call, its window fitted to it.

A standalone script, apart from the Polars groups of this package, run from the
repository root:

    bench/.venv/bin/python bench/scripts/oracle/record_zstdwindow.py

It writes testdata/zstd/*, the same bytes on every run, and refuses another version of
pyarrow or of the zstd CLI.
"""

from __future__ import annotations

import io
import json
import pathlib
import subprocess
import sys

PYARROW = "25.0.1"
ZSTD = "v1.5.7"

ROOT = pathlib.Path(__file__).resolve().parents[3]
DIR = ROOT / "testdata" / "zstd"

ROWS = [{"i": i, "s": f"s{i % 7}", "f": i / 4} for i in range(12)]

# Each file written: its name, and whether the window one of its frames declares is
# past 128 MiB, which the zstdwindow group asks ursus to refuse, naming the limit.
CLI_FILES = [
    ("window_1mib.csv.zst", False),
    ("window_8mib.csv.zst", False),
    ("window_64mib.csv.zst", False),
    ("window_128mib.csv.zst", False),
    ("window_256mib.csv.zst", True),
    ("window_512mib.csv.zst", True),
    ("single_segment_140mib.csv.zst", True),
    ("window_128mib.ndjson.zst", False),
    ("window_256mib.ndjson.zst", True),
    ("window_128mib.json.zst", False),
    ("window_256mib.json.zst", True),
]
PYARROW_FILES = [
    ("window_128mib.arrow", False),
    ("window_256mib.arrow", True),
    ("window_128mib.arrows", False),
    ("window_256mib.arrows", True),
    ("window_128mib.parquet", False),
    ("window_256mib.parquet", True),
]


def texts() -> dict[str, bytes]:
    csv = "i,s,f\n" + "".join(f"{r['i']},{r['s']},{r['f']}\n" for r in ROWS)
    ndjson = "".join(json.dumps(r) + "\n" for r in ROWS)
    return {"csv": csv.encode(), "ndjson": ndjson.encode(), "json": json.dumps(ROWS).encode()}


def zstd(text: bytes, *args: str) -> bytes:
    return subprocess.run(["zstd", "-q", "-c", *args], input=text, capture_output=True, check=True).stdout


def with_cli() -> None:
    version = subprocess.run(["zstd", "--version"], capture_output=True, text=True, check=True).stdout
    if ZSTD not in version.replace(",", " ").split():
        sys.exit(f"zstd {version.strip()}, not {ZSTD}")
    t = texts()
    windows = {"1mib": ["--zstd=wlog=20"], "8mib": ["--zstd=wlog=23"], "64mib": ["--zstd=wlog=26"],
               "128mib": ["--long=27"], "256mib": ["--long=28"], "512mib": ["--long=29"]}
    for name, _ in CLI_FILES:
        if name.startswith("single_segment"):
            size = 140 << 20
            text = b"a\n" + b"x" * (size - 3) + b"\n"
            out = zstd(text, "--long=28", f"--stream-size={size}")
            if out[4] & (1 << 5) == 0:
                sys.exit(f"{name}: zstd wrote no single segment")
        else:
            stem, kind, _ = name.removeprefix("window_").split(".")
            out = zstd(t[kind], *windows[stem])
        (DIR / name).write_bytes(out)


def declare(b: bytes, wd: int) -> bytes:
    """b with the header of every Zstandard frame pyarrow wrote in it, a single segment
    of a content size of 1 or 2 bytes, changed in place to declare the window of the
    Window_Descriptor wd: the content size's bytes become the descriptor, and a
    dictionary ID of 0 where there are two."""
    out = bytearray(b)
    n, i = 0, 0
    while (i := out.find(b"\x28\xb5\x2f\xfd", i)) >= 0:
        fhd = out[i + 4]
        if fhd & 0x23 != 0x20 or fhd >> 6 > 1:
            sys.exit(f"a frame at byte {i} whose header {fhd:#x} is not a short single segment")
        if fhd >> 6 == 0:
            out[i + 4:i + 6] = bytes([fhd & 0x04, wd])
        else:
            out[i + 4:i + 7] = bytes([fhd & 0x04 | 0x01, wd, 0])
        n, i = n + 1, i + 4
    if n == 0:
        sys.exit("no Zstandard frame")
    return bytes(out)


def with_pyarrow() -> None:
    import pyarrow as pa
    import pyarrow.ipc as ipc
    import pyarrow.parquet as pq

    if pa.__version__ != PYARROW:
        sys.exit(f"pyarrow {pa.__version__}, not {PYARROW}")
    t = pa.table({
        "i": pa.array([r["i"] for r in ROWS] * 30, pa.int64()),
        "s": pa.array([r["s"] for r in ROWS] * 30, pa.string()),
        "b": pa.array([bytes((k * 37 + j * 11) % 251 for j in range(24)) for k in range(360)], pa.binary()),
    })
    opts = ipc.IpcWriteOptions(compression="zstd")
    file, stream = io.BytesIO(), io.BytesIO()
    with ipc.new_file(file, t.schema, options=opts) as w:
        w.write_table(t)
    with ipc.new_stream(stream, t.schema, options=opts) as w:
        w.write_table(t)
    parquet = io.BytesIO()
    pq.write_table(t, parquet, compression="zstd", use_dictionary=False, write_statistics=False)
    written = {"arrow": file.getvalue(), "arrows": stream.getvalue(), "parquet": parquet.getvalue()}
    for name, _ in PYARROW_FILES:
        stem, ext = name.removeprefix("window_").split(".")
        wd = {"128mib": 17 << 3, "256mib": 18 << 3}[stem]
        (DIR / name).write_bytes(declare(written[ext], wd))


if __name__ == "__main__":
    DIR.mkdir(parents=True, exist_ok=True)
    with_cli()
    with_pyarrow()
