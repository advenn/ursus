"""Write random Parquet files with pyarrow, for the footer walk's differential (step 521).

internal/source/parquet's TestFooterWalkDifferential walks the footer of each file in
testdata/oracle/footers/ and needs it to pass, as a footer pyarrow writes is one
arrow-go reads; then each strict prefix of it, and each of its lists claiming more
elements than the bytes after its header hold, must be refused. ursus's own writer is
arrow-go's, so these are the footers of another writer: each file a random schema of
pyarrow's types, flat and nested, in random row groups, under a random codec, with
the options that put lists and structs in a footer: statistics and their size
statistics, page indexes, sorting columns, a dictionary or none, V1 or V2 pages, and
key-value metadata, the schema's own and the Arrow schema pyarrow stores.

A standalone script, apart from the Polars groups of this package: from the
repository root, with pyarrow at the version below installed,

    python bench/scripts/oracle/write_footers.py

writes testdata/oracle/footers/pyarrow-NN.parquet, deterministically.
"""

from __future__ import annotations

import datetime
import decimal
import pathlib
import random
import sys

import pyarrow as pa
import pyarrow.parquet as pq

PYARROW = "25.0.1"
FILES = 12
SEED = 521

ROOT = pathlib.Path(__file__).resolve().parents[3]
DIR = ROOT / "testdata" / "oracle" / "footers"


def column(rng: random.Random, kind: str, rows: int) -> tuple[pa.DataType, list]:
    """A column of kind, rows long, a value in four null."""

    def maybe(v):
        return None if rng.random() < 0.25 else v

    if kind == "int64":
        return pa.int64(), [maybe(rng.randint(-10**12, 10**12)) for _ in range(rows)]
    if kind == "int32":
        return pa.int32(), [maybe(rng.randint(-1000, 1000)) for _ in range(rows)]
    if kind == "float64":
        return pa.float64(), [maybe(rng.uniform(-1e6, 1e6)) for _ in range(rows)]
    if kind == "bool":
        return pa.bool_(), [maybe(rng.random() < 0.5) for _ in range(rows)]
    if kind == "string":
        return pa.string(), [maybe("s%d" % rng.randint(0, 9)) for _ in range(rows)]
    if kind == "timestamp":
        base = datetime.datetime(2024, 1, 1)
        return pa.timestamp("us"), [maybe(base + datetime.timedelta(seconds=rng.randint(0, 10**7)))
                                    for _ in range(rows)]
    if kind == "decimal":
        return pa.decimal128(12, 3), [maybe(decimal.Decimal(rng.randint(-10**9, 10**9)) / 1000)
                                      for _ in range(rows)]
    if kind == "list":
        return pa.list_(pa.int64()), [maybe([rng.randint(0, 99) for _ in range(rng.randint(0, 3))])
                                      for _ in range(rows)]
    if kind == "struct":
        t = pa.struct([("a", pa.int64()), ("b", pa.string())])
        return t, [maybe({"a": rng.randint(0, 9), "b": "b%d" % rng.randint(0, 3)}) for _ in range(rows)]
    if kind == "map":
        t = pa.map_(pa.string(), pa.int32())
        return t, [maybe([("k%d" % j, rng.randint(0, 9)) for j in range(rng.randint(0, 2))])
                   for _ in range(rows)]
    if kind == "dictionary":
        t = pa.dictionary(pa.int32(), pa.string())
        return t, [maybe("d%d" % rng.randint(0, 4)) for _ in range(rows)]
    raise ValueError(kind)


KINDS = ["int64", "int32", "float64", "bool", "string", "timestamp", "decimal", "list", "struct",
         "map", "dictionary"]


def main() -> int:
    if pa.__version__ != PYARROW:
        print(f"pyarrow {pa.__version__} is installed; the footers are written under {PYARROW}",
              file=sys.stderr)
        return 2
    rng = random.Random(SEED)
    DIR.mkdir(parents=True, exist_ok=True)
    for i in range(FILES):
        rows = rng.randint(0, 40)
        arrays, fields = [], []
        for c in range(rng.randint(1, 6)):
            kind = rng.choice(KINDS)
            dt, vals = column(rng, kind, rows)
            fields.append(pa.field(f"c{c}_{kind}", dt))
            if kind == "dictionary":
                arrays.append(pa.array(vals, type=pa.string()).dictionary_encode())
            else:
                arrays.append(pa.array(vals, type=dt))
        meta = {b"writer": b"write_footers.py", b"file": str(i).encode()} if rng.random() < 0.5 else None
        table = pa.Table.from_arrays(arrays, schema=pa.schema(fields, metadata=meta))
        sortable = [j for j, f in enumerate(fields)
                    if not pa.types.is_nested(f.type) and not pa.types.is_dictionary(f.type)]
        sorting = None
        if sortable and rng.random() < 0.5:
            j = rng.choice(sortable)
            sorting = [pq.SortingColumn(j, descending=rng.random() < 0.5, nulls_first=rng.random() < 0.5)]
            table = table.sort_by([(fields[j].name, "descending" if sorting[0].descending else "ascending")])
        path = DIR / f"pyarrow-{i:02d}.parquet"
        pq.write_table(
            table, path,
            row_group_size=rng.randint(1, 16),
            compression=rng.choice(["none", "snappy", "zstd", "gzip"]),
            use_dictionary=rng.random() < 0.7,
            write_statistics=rng.random() < 0.8,
            write_page_index=rng.random() < 0.5,
            data_page_version=rng.choice(["1.0", "2.0"]),
            store_schema=rng.random() < 0.7,
            sorting_columns=sorting,
        )
        print(f"{path.name}: {rows} rows, {len(fields)} columns, {path.stat().st_size} bytes")
    return 0


if __name__ == "__main__":
    sys.exit(main())
