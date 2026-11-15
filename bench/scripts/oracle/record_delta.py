"""Record pyarrow's verdict on step 531's forged delta-encoded pages.

Step 531 checks each delta-packed run of a DELTA_BINARY_PACKED,
DELTA_LENGTH_BYTE_ARRAY or DELTA_BYTE_ARRAY data page before arrow-go decodes it
(internal/source/parquet/deltapage.go). readfuzz's TestDeltaPagesAgainstPyarrow forges
pages of each kind of column chunk with each field of a run's header wrong, a
miniblock too wide, the page cut short, and the layouts the format allows, and holds
the check to pyarrow's verdict on each, which this records, so CI needs no Python.

Each forged file NAME--FORGERY.parquet has a twin NAME--plain.parquet, the same values
PLAIN. The verdict is one letter: S, read as the twin; D, read as other values; E, an
error, whose message is kept.

A standalone script, apart from the Polars groups of this package: from the
repository root, with pyarrow at the version below installed,

    URSUS_DELTA_FORGERIES=/tmp/delta go test -run TestDeltaPagesAgainstPyarrow ./internal/readfuzz
    python bench/scripts/oracle/record_delta.py /tmp/delta

writes testdata/oracle/delta/verdicts.json, deterministically.
"""

from __future__ import annotations

import hashlib
import json
import pathlib
import sys

import pyarrow
import pyarrow.parquet as pq

PYARROW = "25.0.1"
GENERATOR = (
    "URSUS_DELTA_FORGERIES=<dir> go test -run TestDeltaPagesAgainstPyarrow ./internal/readfuzz, "
    "then bench/scripts/oracle/record_delta.py <dir>"
)

ROOT = pathlib.Path(__file__).resolve().parents[3]
OUT = ROOT / "testdata" / "oracle" / "delta" / "verdicts.json"


def main() -> int:
    if pyarrow.__version__ != PYARROW:
        print(f"pyarrow {pyarrow.__version__} is installed; the verdicts are recorded under {PYARROW}",
              file=sys.stderr)
        return 1
    if len(sys.argv) != 2:
        print("usage: record_delta.py <dir of the forged files>", file=sys.stderr)
        return 2
    src = pathlib.Path(sys.argv[1])
    files = {}
    for path in sorted(src.glob("*.parquet")):
        if path.name.endswith("--plain.parquet"):
            continue
        twin = path.name.split("--")[0] + "--plain.parquet"
        want = pq.read_table(src / twin, use_threads=False)
        error = ""
        try:
            got = pq.read_table(path, use_threads=False)
            verdict = "S" if got.equals(want) else "D"
        except Exception as e:  # pyarrow's verdict, whatever it raises
            verdict, error = "E", f"{type(e).__name__}: {e}"
        files[path.name] = {
            "sha256": hashlib.sha256(path.read_bytes()).hexdigest(),
            "twin": twin,
            "pyarrow": verdict,
            "error": error,
        }
    OUT.parent.mkdir(parents=True, exist_ok=True)
    record = {"pyarrow": PYARROW, "generator": GENERATOR, "files": files}
    OUT.write_text(json.dumps(record, indent=1, sort_keys=True) + "\n")
    counts = {}
    for f in files.values():
        counts[f["pyarrow"]] = counts.get(f["pyarrow"], 0) + 1
    print(f"{len(files)} files: {counts}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
