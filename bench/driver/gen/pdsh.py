"""PDS-H (TPC-H) dataset generation.

Two steps, deliberately separate:

  1. `tpchgen-cli` writes the eight tables as Parquet. It is a pure-Rust dbgen
     that produces SF10 in seconds, which is why this suite does not vendor and
     compile the C `tpch-dbgen`.

  2. duckdb normalises the result. tpchgen emits the money columns as
     decimal128(15, 2) exactly as the TPC-H spec says. That is faithful but not
     what we want to measure: ursus's `numericTypes` omits Decimal, so the
     `GroupBy.Sum()` shorthand skips those columns and decimal arithmetic is
     unproven, and the Python engines each make a different silent choice about
     how to represent decimals. Casting money to DOUBLE up front means every
     engine reads byte-identical files and the timings compare arithmetic rather
     than type-conversion policy. Dates stay date32 — every engine handles those.

Both a `parquet/` and (on request) a `csv/` tree are produced from the same
normalised data, so the IO=csv runs measure the same rows.
"""

from __future__ import annotations

import shutil
import subprocess
import sys
import time
from pathlib import Path

from ..settings import Config, RunSpec
from . import load_manifest, write_manifest

TABLES = (
    "region",
    "nation",
    "supplier",
    "customer",
    "part",
    "partsupp",
    "orders",
    "lineitem",
)

# Row counts are fixed multiples of the scale factor, except nation and region.
# Used to prove the generator produced what it claimed.
ROWS_PER_SF = {
    "supplier": 10_000,
    "customer": 150_000,
    "part": 200_000,
    "partsupp": 800_000,
    "orders": 1_500_000,
    "lineitem": 6_001_215,
}
FIXED_ROWS = {"nation": 25, "region": 5}

FORMAT_VERSION = 2  # bump when normalisation changes; invalidates old manifests


def _tpchgen_binary() -> str:
    """Prefer the copy in the active venv; fall back to PATH."""
    venv_bin = Path(sys.executable).parent / "tpchgen-cli"
    if venv_bin.exists():
        return str(venv_bin)
    found = shutil.which("tpchgen-cli")
    if found:
        return found
    raise SystemExit("tpchgen-cli not found. Run `make setup` (it is a declared dependency).")


def _tpchgen(out_dir: Path, scale: float, console) -> None:
    out_dir.mkdir(parents=True, exist_ok=True)
    cmd = [
        _tpchgen_binary(),
        "parquet",
        "-s",
        str(scale),
        "-o",
        str(out_dir),
        "--quiet",
        "--no-progress",
    ]
    console.print(f"[dim]$ {' '.join(cmd)}[/]")
    subprocess.run(cmd, check=True)


def _normalise(raw: Path, parquet_dir: Path, csv_dir: Path | None, threads: int, console) -> dict:
    import duckdb

    parquet_dir.mkdir(parents=True, exist_ok=True)
    if csv_dir is not None:
        csv_dir.mkdir(parents=True, exist_ok=True)

    con = duckdb.connect()
    con.execute(f"SET threads TO {threads}")
    # Normalisation is a streaming copy; insertion order costs memory and buys
    # nothing because every engine re-sorts anyway.
    con.execute("SET preserve_insertion_order TO false")

    stats: dict[str, dict] = {}
    for table in TABLES:
        src = raw / f"{table}.parquet"
        if not src.exists():
            raise SystemExit(f"tpchgen did not produce {src}")

        described = con.execute(
            f"DESCRIBE SELECT * FROM read_parquet('{src.as_posix()}')"
        ).fetchall()
        projection = ", ".join(
            f'CAST("{name}" AS DOUBLE) AS "{name}"' if dtype.startswith("DECIMAL") else f'"{name}"'
            for name, dtype, *_ in described
        )
        select = f"SELECT {projection} FROM read_parquet('{src.as_posix()}')"

        dst = parquet_dir / f"{table}.parquet"
        con.execute(
            f"COPY ({select}) TO '{dst.as_posix()}' "
            "(FORMAT PARQUET, COMPRESSION ZSTD, ROW_GROUP_SIZE 122880)"
        )

        if csv_dir is not None:
            con.execute(
                f"COPY ({select}) TO '{(csv_dir / f'{table}.csv').as_posix()}' "
                "(FORMAT CSV, HEADER, DELIMITER ',')"
            )

        rows = con.execute(
            f"SELECT count(*) FROM read_parquet('{dst.as_posix()}')"
        ).fetchone()[0]
        stats[table] = {"rows": rows, "bytes": dst.stat().st_size}
        console.print(f"  {table:<10} {rows:>12,} rows  {dst.stat().st_size / 1e6:>8.1f} MB")

    con.close()
    return stats


def _check_rows(stats: dict, scale: float, console) -> list[str]:
    """TPC-H row counts are deterministic. Anything else means a broken generate."""
    problems = []
    for table, expected in FIXED_ROWS.items():
        got = stats[table]["rows"]
        if got != expected:
            problems.append(f"{table}: expected {expected} rows, got {got}")
    for table, per_sf in ROWS_PER_SF.items():
        got = stats[table]["rows"]
        expected = round(per_sf * scale)
        # lineitem is the one table whose cardinality is only approximately
        # proportional (1..7 line items per order), so it gets a tolerance.
        tolerance = 0.02 if table == "lineitem" else 0.0
        if abs(got - expected) > max(1, expected * tolerance):
            problems.append(f"{table}: expected ~{expected:,} rows, got {got:,}")
    for p in problems:
        console.print(f"[red]row count mismatch[/] {p}")
    return problems


def generate(cfg: Config, spec: RunSpec, console) -> int:
    root = cfg.paths.dataset_dir("pdsh", spec.size)
    want_csv = spec.io == "csv"

    existing = load_manifest(root)
    if (
        existing
        and existing.get("format_version") == FORMAT_VERSION
        and existing.get("scale") == spec.size
        and (existing.get("csv") or not want_csv)
    ):
        console.print(f"[green]data ready[/] {root} (manifest matches; delete to regenerate)")
        return 0

    console.rule(f"[bold]generating PDS-H SF{spec.size:g}[/]")
    raw = root / "_raw"
    started = time.monotonic()

    _tpchgen(raw, spec.size, console)
    stats = _normalise(
        raw,
        root / "parquet",
        (root / "csv") if want_csv else None,
        spec.threads,
        console,
    )
    problems = _check_rows(stats, spec.size, console)

    # The raw decimal tree is a build artefact; nothing reads it after this.
    shutil.rmtree(raw, ignore_errors=True)

    write_manifest(
        root,
        {
            "suite": "pdsh",
            "scale": spec.size,
            "format_version": FORMAT_VERSION,
            "csv": want_csv,
            "generator": "tpchgen-cli parquet -> duckdb normalise (decimal->double)",
            "tables": stats,
            "seconds": round(time.monotonic() - started, 2),
        },
    )
    console.print(f"[green]done[/] in {time.monotonic() - started:.1f}s -> {root}")
    return 1 if problems else 0
