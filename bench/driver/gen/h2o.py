"""h2o.ai db-benchmark dataset generation.

Ported from `h2oai/db-benchmark/_data/groupby-datagen.R` and `join-datagen.R`.
The structure — cardinalities, value ranges, the 0.9/0.1/0.1 key split that
gives the join tables their overlap — is reproduced exactly. The *values* are
not: R's `set.seed(108)` plus its Mersenne-Twister draws cannot be reproduced
from numpy, so the bytes differ from the published h2o datasets. That is fine
for this suite's purpose, because every engine reads the same generated files
and the distributions are identical. It does mean our absolute numbers are not
directly comparable to the numbers on the h2o leaderboard, which is stated in
bench/README.md.

Files written (parquet always, csv when IO=csv):

  groupby/g1.{parquet,csv}      N rows, id1..id6 + v1..v3
  join/x.{parquet,csv}          N rows      (LHS)
  join/small.{parquet,csv}      N/1e6 rows
  join/medium.{parquet,csv}     N/1e3 rows
  join/big.{parquet,csv}        N rows
"""

from __future__ import annotations

import time
from pathlib import Path

import numpy as np

from ..settings import Config, RunSpec
from . import load_manifest, write_manifest

SEED = 108
K = 100  # group cardinality factor, as in the published 1e2 datasets
FORMAT_VERSION = 1


def _write(table, path_base: Path, want_csv: bool) -> dict:
    """Write a pyarrow Table as parquet (always) and csv (on request)."""
    import pyarrow.csv as pacsv
    import pyarrow.parquet as pq

    path_base.parent.mkdir(parents=True, exist_ok=True)
    pq_path = path_base.with_suffix(".parquet")
    pq.write_table(table, pq_path, compression="zstd", row_group_size=122880)
    written = {"rows": table.num_rows, "bytes": pq_path.stat().st_size}
    if want_csv:
        csv_path = path_base.with_suffix(".csv")
        pacsv.write_csv(table, csv_path)
        written["csv_bytes"] = csv_path.stat().st_size
    return written


def _labels(prefix_width: int, count: int) -> np.ndarray:
    """`id001`-style labels, as the R generator's sprintf produces them."""
    return np.array([f"id{i:0{prefix_width}d}" for i in range(1, count + 1)])


def _apply_id_nas(rng: np.random.Generator, values: np.ndarray, pct: int) -> np.ndarray:
    """NA a `pct` fraction of the *distinct* values, as the R generator does.

    Note this is not the same as NA-ing pct% of rows: a whole group disappears
    at a time, which is what makes the null handling in the groupby queries
    interesting.
    """
    if pct <= 0:
        return values
    unique = np.unique(values)
    n_na = int(len(unique) * (pct / 100))
    if n_na == 0:
        return values
    doomed = rng.choice(unique, size=n_na, replace=False)
    mask = np.isin(values, doomed)
    out = values.astype(object)
    out[mask] = None
    return out


def _generate_groupby(root: Path, n: int, nas: int, sort: bool, want_csv: bool, console) -> dict:
    import pyarrow as pa

    rng = np.random.default_rng(SEED)
    n_large = n // K  # distinct values for id3 / id6

    console.print(f"  groupby: {n:,} rows, K={K}, {n_large:,} large-group keys")

    id1_labels = _labels(3, K)
    id3_labels = _labels(10, n_large)

    columns = {
        "id1": id1_labels[rng.integers(0, K, n)],
        "id2": id1_labels[rng.integers(0, K, n)],
        "id3": id3_labels[rng.integers(0, n_large, n)],
        "id4": rng.integers(1, K + 1, n, dtype=np.int32),
        "id5": rng.integers(1, K + 1, n, dtype=np.int32),
        "id6": rng.integers(1, n_large + 1, n, dtype=np.int32),
        "v1": rng.integers(1, 6, n, dtype=np.int32),
        "v2": rng.integers(1, 16, n, dtype=np.int32),
        "v3": np.round(rng.uniform(0, 100, n), 6),
    }

    if nas > 0:
        for name in ("id1", "id2", "id3", "id4", "id5", "id6"):
            columns[name] = _apply_id_nas(rng, columns[name], nas)
        n_na = int(n * (nas / 100))
        if n_na:
            for name in ("v1", "v2", "v3"):
                idx = rng.choice(n, size=n_na, replace=False)
                col = columns[name].astype(float)
                col[idx] = np.nan
                columns[name] = col

    table = pa.table(columns)
    if sort:
        table = table.sort_by([(f"id{i}", "ascending") for i in range(1, 7)])

    return _write(table, root / "groupby" / "g1", want_csv)


def _split_xlr(rng: np.random.Generator, n: int) -> dict[str, np.ndarray]:
    """The 0.9 common / 0.1 left-only / 0.1 right-only key split.

    A permutation of 1..1.1n cut into three: the first 90% appear on both sides,
    the next 10% only on the left, the last 10% only on the right. That is what
    gives the h2o joins their fixed 90% match rate.
    """
    key = rng.permutation(int(n * 1.1)) + 1
    return {
        "x": key[: int(n * 0.9)],
        "l": key[int(n * 0.9) : n],
        "r": key[n : int(n * 1.1)],
    }


def _sample_all(rng: np.random.Generator, pool: np.ndarray, size: int) -> np.ndarray:
    """Draw `size` values covering every element of `pool` at least once."""
    if size < len(pool):
        raise ValueError(f"cannot cover {len(pool)} keys in {size} draws")
    extra = rng.choice(pool, size=size - len(pool), replace=True)
    out = np.concatenate([pool, extra])
    rng.shuffle(out)
    return out


def _id_labels(values: np.ndarray) -> np.ndarray:
    """`id4`/`id5`/`id6` are the integer keys re-encoded as strings, unpadded."""
    return np.char.add("id", values.astype(str))


def _generate_join(root: Path, n: int, want_csv: bool, console) -> dict:
    import pyarrow as pa

    rng = np.random.default_rng(SEED)
    n_small, n_medium = int(n / 1e6), int(n / 1e3)
    if n_small < 1:
        raise SystemExit(f"h2o join data needs N >= 1e6 (got {n:g})")

    console.print(f"  join: LHS {n:,} / small {n_small:,} / medium {n_medium:,} / big {n:,}")

    key1 = _split_xlr(rng, n_small)
    key2 = _split_xlr(rng, n_medium)
    key3 = _split_xlr(rng, n)

    lhs_pool = lambda k: np.concatenate([k["x"], k["l"]])  # noqa: E731
    rhs_pool = lambda k: np.concatenate([k["x"], k["r"]])  # noqa: E731

    stats: dict[str, dict] = {}

    x_id1 = _sample_all(rng, lhs_pool(key1), n).astype(np.int32)
    x_id2 = _sample_all(rng, lhs_pool(key2), n).astype(np.int32)
    x_id3 = _sample_all(rng, lhs_pool(key3), n).astype(np.int32)
    stats["x"] = _write(
        pa.table(
            {
                "id1": x_id1,
                "id2": x_id2,
                "id3": x_id3,
                "id4": _id_labels(x_id1),
                "id5": _id_labels(x_id2),
                "id6": _id_labels(x_id3),
                "v1": np.round(rng.uniform(0, 100, n), 6),
            }
        ),
        root / "join" / "x",
        want_csv,
    )
    del x_id1, x_id2, x_id3

    s_id1 = _sample_all(rng, rhs_pool(key1), n_small).astype(np.int32)
    stats["small"] = _write(
        pa.table(
            {
                "id1": s_id1,
                "id4": _id_labels(s_id1),
                "v2": np.round(rng.uniform(0, 100, n_small), 6),
            }
        ),
        root / "join" / "small",
        want_csv,
    )
    del s_id1

    m_id1 = _sample_all(rng, rhs_pool(key1), n_medium).astype(np.int32)
    m_id2 = _sample_all(rng, rhs_pool(key2), n_medium).astype(np.int32)
    stats["medium"] = _write(
        pa.table(
            {
                "id1": m_id1,
                "id2": m_id2,
                "id4": _id_labels(m_id1),
                "id5": _id_labels(m_id2),
                "v2": np.round(rng.uniform(0, 100, n_medium), 6),
            }
        ),
        root / "join" / "medium",
        want_csv,
    )
    del m_id1, m_id2

    b_id1 = _sample_all(rng, rhs_pool(key1), n).astype(np.int32)
    b_id2 = _sample_all(rng, rhs_pool(key2), n).astype(np.int32)
    b_id3 = _sample_all(rng, rhs_pool(key3), n).astype(np.int32)
    stats["big"] = _write(
        pa.table(
            {
                "id1": b_id1,
                "id2": b_id2,
                "id3": b_id3,
                "id4": _id_labels(b_id1),
                "id5": _id_labels(b_id2),
                "id6": _id_labels(b_id3),
                "v2": np.round(rng.uniform(0, 100, n), 6),
            }
        ),
        root / "join" / "big",
        want_csv,
    )
    return stats


def generate(cfg: Config, spec: RunSpec, console) -> int:
    n = int(spec.size)
    root = cfg.paths.dataset_dir("h2o", spec.size)
    want_csv = spec.io == "csv"

    existing = load_manifest(root)
    if (
        existing
        and existing.get("format_version") == FORMAT_VERSION
        and existing.get("rows") == n
        and (existing.get("csv") or not want_csv)
    ):
        console.print(f"[green]data ready[/] {root} (manifest matches; delete to regenerate)")
        return 0

    console.rule(f"[bold]generating h2o data, N={n:,}[/]")
    started = time.monotonic()

    tables = {"g1": _generate_groupby(root, n, nas=0, sort=False, want_csv=want_csv, console=console)}
    tables |= _generate_join(root, n, want_csv, console)

    for name, info in tables.items():
        console.print(f"  {name:<8} {info['rows']:>12,} rows  {info['bytes'] / 1e6:>8.1f} MB")

    write_manifest(
        root,
        {
            "suite": "h2o",
            "rows": n,
            "k": K,
            "nas": 0,
            "sorted": False,
            "seed": SEED,
            "format_version": FORMAT_VERSION,
            "csv": want_csv,
            "generator": "numpy port of h2oai/db-benchmark _data/*-datagen.R",
            "tables": tables,
            "seconds": round(time.monotonic() - started, 2),
        },
    )
    console.print(f"[green]done[/] in {time.monotonic() - started:.1f}s -> {root}")
    return 0
