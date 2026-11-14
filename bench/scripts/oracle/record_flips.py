"""Record what DuckDB and pyarrow read of each byte-flipped Parquet file (step 510).

Step 444 of context_files/v0.7.2-plan.md, from H2's flipdiff.py. For each file in
testdata/oracle/flips/, every byte is flipped in turn (XOR 0x5A) and the file read by
DuckDB and by pyarrow, each on one thread; the outcome of each offset is one letter:
S, the original's answer; D, another answer; E, an error. The originals' answer is
recorded too, a column a key. readfuzz's TestParquetByteFlipsAgainstDuckDBAndPyarrow
reads every flip with ursus and fails where ursus answers otherwise than the original
while both engines read the original, so CI needs no Python.

A standalone script, apart from the Polars groups of this package: from the
repository root, with DuckDB and pyarrow at the versions below installed,

    python bench/scripts/oracle/record_flips.py

writes testdata/oracle/flips/flips.json, deterministically.
"""

from __future__ import annotations

import hashlib
import io
import json
import math
import pathlib
import sys
import tempfile

import duckdb
import pyarrow
import pyarrow.parquet as pq

DUCKDB = "1.5.5"
PYARROW = "25.0.1"
FLIP = 0x5A

ROOT = pathlib.Path(__file__).resolve().parents[3]
DIR = ROOT / "testdata" / "oracle" / "flips"


def columns(rows: list[dict]) -> dict[str, list]:
    """The rows as a list a column, in the file's order of columns."""
    out: dict[str, list] = {}
    for r in rows:
        for k, v in r.items():
            out.setdefault(k, []).append(v)
    return out


def encode(v):
    """A value as JSON holds it exactly: a float that is not finite as a string."""
    if isinstance(v, float) and not math.isfinite(v):
        return repr(v)
    return v


def main() -> int:
    if duckdb.__version__ != DUCKDB or pyarrow.__version__ != PYARROW:
        print(f"duckdb {duckdb.__version__} and pyarrow {pyarrow.__version__} are installed; the "
              f"flips are recorded under {DUCKDB} and {PYARROW}", file=sys.stderr)
        return 2
    # The external file cache keeps a file's bytes by its path and modification time,
    # and a flip rewrites the same path within the same second: off, and a path a
    # flip, or DuckDB reads an earlier flip's bytes.
    con = duckdb.connect(config={"threads": 1, "enable_external_file_cache": False})
    pyarrow.set_cpu_count(1)
    pyarrow.set_io_thread_count(1)
    with tempfile.TemporaryDirectory() as tmp:
        n = 0

        def duck(b: bytes):
            nonlocal n
            n += 1
            path = pathlib.Path(tmp) / f"flip{n}.parquet"
            path.write_bytes(b)
            try:
                return con.execute(f"select * from read_parquet('{path}')").fetchall()
            except Exception:
                return None
            finally:
                path.unlink()

        def arrow(b: bytes):
            try:
                return pq.read_table(io.BytesIO(b), use_threads=False).to_pylist()
            except Exception:
                return None

        files = {}
        for f in sorted(DIR.glob("*.parquet")):
            orig = f.read_bytes()
            want_d, want_a = duck(orig), arrow(orig)
            if want_d is None or want_a is None:
                print(f"{f.name}: an engine cannot read the original", file=sys.stderr)
                return 1
            if [tuple(r.values()) for r in want_a] != want_d:
                print(f"{f.name}: DuckDB and pyarrow read the original apart", file=sys.stderr)
                return 1
            out = {"duckdb": [], "pyarrow": []}
            for i in range(len(orig)):
                b = bytearray(orig)
                b[i] ^= FLIP
                b = bytes(b)
                for engine, read, want in (("duckdb", duck, want_d), ("pyarrow", arrow, want_a)):
                    got = read(b)
                    out[engine].append("E" if got is None else "S" if got == want else "D")
            files[f.name] = {
                "sha256": hashlib.sha256(orig).hexdigest(),
                "original": {k: [encode(v) for v in vs] for k, vs in columns(want_a).items()},
                "duckdb": "".join(out["duckdb"]),
                "pyarrow": "".join(out["pyarrow"]),
            }
            print(f"{f.name}: {len(orig)} flips; DuckDB S/D/E "
                  f"{out['duckdb'].count('S')}/{out['duckdb'].count('D')}/{out['duckdb'].count('E')}, "
                  f"pyarrow {out['pyarrow'].count('S')}/{out['pyarrow'].count('D')}/{out['pyarrow'].count('E')}")
    doc = {
        "duckdb": DUCKDB,
        "pyarrow": PYARROW,
        "flip": f"each byte XOR 0x{FLIP:02X}",
        "generator": "bench/scripts/oracle/record_flips.py",
        "files": files,
    }
    (DIR / "flips.json").write_text(json.dumps(doc, indent=1, sort_keys=True, allow_nan=False) + "\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
