"""Compare every engine's answer against the duckdb reference.

Correctness is a gate, not a footnote. A fast wrong answer is not a benchmark
result, so `report.py` strikes through the timings of any engine/query pair that
fails here and the summary counts it as a failure.

What is compared:

  * shape — same number of rows and columns
  * columns — same names (order is normalised; a SELECT that lists columns in a
    different order is not an error)
  * values — floats within rtol, everything else exactly, after sorting both
    sides by every column so that an engine which does not guarantee group order
    is not punished for it

PDS-H answers are compared in full. h2o answers are one-row checksums produced
by the runners; the same code compares them.
"""

from __future__ import annotations

import csv
import math
from dataclasses import dataclass
from datetime import datetime
from decimal import Decimal
from pathlib import Path

from .settings import Config, RunSpec

RTOL = 1e-6
ATOL = 1e-9

VALIDATION_HEADER = ["engine", "suite", "query", "size", "io", "valid", "detail"]


@dataclass
class Verdict:
    valid: bool
    detail: str = ""


def _sorted_rows(table) -> list[tuple]:
    """Canonical row order: sort by every column, with nulls last."""
    columns = sorted(table.column_names)
    data = [table.column(name).to_pylist() for name in columns]
    rows = list(zip(*data)) if data else []

    def key(row):
        return tuple((value is None, "" if value is None else str(value)) for value in row)

    return sorted(rows, key=key)


def _as_date(value):
    """Collapse a midnight datetime to its date; leave everything else alone."""
    if isinstance(value, datetime) and (value.hour, value.minute, value.second,
                                        value.microsecond) == (0, 0, 0, 0):
        return value.date()
    return value


def _close(a, b) -> bool:
    if a is None or b is None:
        return a is None and b is None
    if isinstance(a, bool) or isinstance(b, bool):
        return a == b
    # ursus widens an integer Sum to Int128, which Parquet can only carry as
    # DECIMAL(38, 0), so a column duckdb wrote as BIGINT comes back as a Decimal
    # on the other side. Same number, different carrier.
    if isinstance(a, Decimal) or isinstance(b, Decimal):
        a, b = float(a), float(b)
    # pandas has no date type, so a DATE column round-trips as a midnight
    # timestamp. Same calendar day, different carrier — not a wrong answer.
    a, b = _as_date(a), _as_date(b)
    if isinstance(a, (int, float)) and isinstance(b, (int, float)):
        if isinstance(a, float) and math.isnan(a):
            return isinstance(b, float) and math.isnan(b)
        return math.isclose(float(a), float(b), rel_tol=RTOL, abs_tol=ATOL)
    return a == b


def compare(got_path: Path, want_path: Path) -> Verdict:
    import pyarrow.parquet as pq

    if not want_path.exists():
        return Verdict(False, f"no reference answer at {want_path.name}")
    if not got_path.exists():
        return Verdict(False, "engine produced no answer")

    got, want = pq.read_table(got_path), pq.read_table(want_path)

    if set(got.column_names) != set(want.column_names):
        only_got = sorted(set(got.column_names) - set(want.column_names))
        only_want = sorted(set(want.column_names) - set(got.column_names))
        return Verdict(
            False,
            f"columns differ (extra: {only_got or '-'}, missing: {only_want or '-'})",
        )
    if got.num_rows != want.num_rows:
        return Verdict(False, f"{got.num_rows} rows, reference has {want.num_rows}")

    got_rows, want_rows = _sorted_rows(got), _sorted_rows(want)
    for index, (a, b) in enumerate(zip(got_rows, want_rows)):
        for column, (x, y) in enumerate(zip(a, b)):
            if not _close(x, y):
                name = sorted(got.column_names)[column]
                return Verdict(False, f"row {index}, column {name}: {x!r} != {y!r}")
    return Verdict(True)


def run(cfg: Config, spec: RunSpec, console) -> int:
    suite = cfg.suites[spec.suite]
    queries = spec.resolved_queries(suite)
    engines = cfg.engines_for(spec.suite, list(spec.engines) or None)
    reference_dir = cfg.paths.answers_dir(spec.suite, spec.size)
    answer_root = cfg.paths.results / "answers"

    console.rule(f"[bold]validating[/] {suite.label} at {spec.size:g} {suite.unit}")

    rows: list[dict] = []
    failed = 0
    checked = 0
    for engine in engines:
        for query in queries:
            supported, _ = engine.supports(spec.suite, query, spec.size)
            if not supported:
                continue
            got = (
                answer_root / engine.name / spec.suite / spec.size_slug / spec.io
                / f"{query}.parquet"
            )
            if not got.exists():
                # The engine never ran (or errored); runner.py already recorded
                # why. Nothing to validate.
                continue
            verdict = compare(got, reference_dir / f"{query}.parquet")
            checked += 1
            rows.append({
                "engine": engine.name, "suite": spec.suite, "query": query,
                "size": spec.size, "io": spec.io,
                "valid": int(verdict.valid), "detail": verdict.detail,
            })
            if not verdict.valid:
                failed += 1
                console.print(f"  [red]WRONG[/] {engine.label:<12} {query:<5} {verdict.detail}")

    path = cfg.paths.results / "validation.csv"
    existing = path.exists()
    with path.open("a", newline="") as fh:
        writer = csv.DictWriter(fh, fieldnames=VALIDATION_HEADER)
        if not existing:
            writer.writeheader()
        writer.writerows(rows)

    if failed:
        console.print(f"\n[red]{failed} of {checked} answers disagree with the reference[/]")
        return 1
    console.print(f"[green]all {checked} answers match the duckdb reference[/]")
    return 0
