"""Execute engines and record what happened.

One subprocess per (engine, query). The driver builds the command line, pins the
environment, wraps the whole thing in a resource budget, waits, and reads the
JSON the runner wrote. It never times anything itself — see engines/py/_common.py
for why the measurement lives inside the engine process.

Every outcome is recorded, including the bad ones. An engine that cannot express
a query, times out, or is OOM-killed produces a row in timings.csv with the
reason. Silence would be indistinguishable from a fast result.
"""

from __future__ import annotations

import csv
import json
import os
import subprocess
import sys
import time
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path

from .cgroup import Budget, classify_exit
from .settings import Config, Engine, RunSpec, Suite

TIMINGS_HEADER = [
    "timestamp",
    "engine",
    "suite",
    "query",
    "size",
    "io",
    "threads",
    "mem_limit",
    "iteration",
    "seconds",
    "rows",
    "peak_rss_bytes",
    "startup_s",
    "status",
    "error",
]


@dataclass
class Launch:
    """Where one engine run's artefacts live."""

    json_path: Path
    answer_path: Path


def _slug(engine: Engine, spec: RunSpec, query: str) -> str:
    return f"{engine.name}__{spec.suite}__{query}__{spec.size_slug}__{spec.io}"


def _thread_env(threads: int) -> dict[str, str]:
    """Pin every runtime's worker count to the same number.

    Left alone, polars would use rayon's default, numpy would use OpenBLAS's,
    and Go would use GOMAXPROCS — three different numbers on the same box, which
    would make the comparison a comparison of default thread policies.
    """
    value = str(threads)
    return {
        "POLARS_MAX_THREADS": value,
        "RAYON_NUM_THREADS": value,
        "OMP_NUM_THREADS": value,
        "OPENBLAS_NUM_THREADS": value,
        "MKL_NUM_THREADS": value,
        "NUMEXPR_NUM_THREADS": value,
        "GOMAXPROCS": value,
    }


def build_argv(cfg: Config, engine: Engine, spec: RunSpec, query: str, launch: Launch,
               checksum_cols: tuple[str, ...]) -> list[str]:
    data_dir = cfg.paths.dataset_dir(spec.suite, spec.size)
    flags = [
        "--suite", spec.suite,
        "--query", query,
        "--data", str(data_dir),
        "--io", spec.io,
        "--iterations", str(spec.iterations),
        "--threads", str(spec.threads),
        "--out", str(launch.json_path),
        "--result", str(launch.answer_path),
    ]
    if checksum_cols:
        flags += ["--checksum-cols", ",".join(checksum_cols)]

    if engine.lang == "python":
        if not engine.entry:
            raise SystemExit(f"engine {engine.name}: python engines need an `entry`")
        return [sys.executable, "-m", engine.entry, *flags]

    binary = cfg.paths.gobin / (engine.binary or "runner")
    if not binary.exists():
        target = "make setup-cgo" if engine.binary == "runner-cgo" else "make setup-go"
        raise FileNotFoundError(f"{binary} not built — run `{target}`")
    return [str(binary), "--engine", engine.name, *flags]


def _record(rows: list[dict], spec: RunSpec, engine: Engine, query: str,
            payload: dict, stamp: str) -> None:
    """Flatten one runner's JSON into per-iteration timings.csv rows."""
    base = {
        "timestamp": stamp,
        "engine": engine.name,
        "suite": spec.suite,
        "query": query,
        "size": spec.size,
        "io": spec.io,
        "threads": spec.threads,
        "mem_limit": spec.mem_limit or "",
        "rows": payload.get("rows", -1),
        "peak_rss_bytes": payload.get("peak_rss_bytes", 0),
        "startup_s": payload.get("startup_s", 0.0),
        "status": payload.get("status", "error"),
        "error": (payload.get("error") or "").replace("\n", " ")[:300],
    }
    iterations = payload.get("iterations") or []
    if not iterations:
        rows.append({**base, "iteration": 0, "seconds": ""})
        return
    for index, seconds in enumerate(iterations):
        rows.append({**base, "iteration": index, "seconds": round(seconds, 6)})


def _append_timings(path: Path, rows: list[dict]) -> None:
    exists = path.exists()
    with path.open("a", newline="") as fh:
        writer = csv.DictWriter(fh, fieldnames=TIMINGS_HEADER)
        if not exists:
            writer.writeheader()
        writer.writerows(rows)


def execute_one(cfg: Config, engine: Engine, spec: RunSpec, query: str, budget: Budget,
                checksum_cols: tuple[str, ...], answer_root: Path, console) -> dict:
    """Run one (engine, query) and return the payload the runner produced."""
    slug = _slug(engine, spec, query)
    launch = Launch(
        json_path=cfg.paths.raw / f"{slug}.json",
        # io is in the path so a csv run does not overwrite the parquet run's
        # answer for the same query.
        answer_path=(
            answer_root / engine.name / spec.suite / spec.size_slug / spec.io / f"{query}.parquet"
        ),
    )
    launch.json_path.unlink(missing_ok=True)

    try:
        argv = build_argv(cfg, engine, spec, query, launch, checksum_cols)
    except FileNotFoundError as exc:
        # A runner that was never built is a configuration fact, not a failure
        # of the engine. Reporting it as `unsupported` keeps a missing cgo build
        # from looking like 22 crashes.
        return {"status": "unsupported", "error": str(exc)}

    env = {**os.environ, **_thread_env(spec.threads), "GOEXPERIMENT": "simd"}
    wrapped = budget.wrap(argv, scope_name=f"ursusbench-{slug}")

    started = time.monotonic()
    try:
        completed = subprocess.run(
            wrapped, env=env, cwd=cfg.paths.root, timeout=spec.timeout,
            capture_output=True, text=True,
        )
    except subprocess.TimeoutExpired:
        return {"status": "timeout", "error": f"exceeded {spec.timeout}s"}
    elapsed = time.monotonic() - started

    if launch.json_path.exists():
        payload = json.loads(launch.json_path.read_text())
        # A runner that wrote `ok` but whose process then died still failed.
        failure = classify_exit(completed.returncode)
        if failure and payload.get("status") == "ok":
            payload["status"] = failure
            payload["error"] = (completed.stderr or "").strip()[-300:] or f"exit {completed.returncode}"
        return payload

    status = classify_exit(completed.returncode) or "error"
    tail = (completed.stderr or completed.stdout or "").strip().splitlines()
    return {
        "status": status,
        "error": (tail[-1] if tail else f"no output, exit {completed.returncode}"),
        "wall_s": round(elapsed, 3),
    }


def run(cfg: Config, spec: RunSpec, budget: Budget, console,
        answer_root: Path | None = None, only_engine: str | None = None,
        record_timings: bool = True) -> int:
    suite: Suite = cfg.suites[spec.suite]
    queries = spec.resolved_queries(suite)
    engines = (
        [cfg.engines[only_engine]] if only_engine
        else cfg.engines_for(spec.suite, list(spec.engines) or None)
    )
    answer_root = answer_root or (cfg.paths.results / "answers")

    checksums = _checksum_map(cfg, spec.suite)
    stamp = datetime.now(timezone.utc).isoformat(timespec="seconds")

    console.rule(f"[bold]{suite.label}[/] at {spec.size:g} {suite.unit} — {budget.describe()}")

    rows: list[dict] = []
    failures = 0
    for engine in engines:
        for query in queries:
            ok, reason = engine.supports(spec.suite, query, spec.size)
            if not ok:
                console.print(f"  [dim]{engine.label:<12} {query:<5} skipped — {reason}[/]")
                rows.append({
                    "timestamp": stamp, "engine": engine.name, "suite": spec.suite,
                    "query": query, "size": spec.size, "io": spec.io,
                    "threads": spec.threads, "mem_limit": spec.mem_limit or "",
                    "iteration": 0, "seconds": "", "rows": -1, "peak_rss_bytes": 0,
                    "startup_s": 0.0, "status": "unsupported", "error": reason,
                })
                continue

            payload = execute_one(
                cfg, engine, spec, query, budget,
                checksums.get(query, ()), answer_root, console,
            )
            _record(rows, spec, engine, query, payload, stamp)

            status = payload.get("status", "error")
            times = payload.get("iterations") or []
            if status == "ok" and times:
                median = sorted(times)[len(times) // 2]
                rss = payload.get("peak_rss_bytes", 0) / 1e9
                console.print(
                    f"  [green]ok[/]  {engine.label:<12} {query:<5} "
                    f"{median * 1000:>9.1f} ms   {rss:>5.2f} GB   {payload.get('rows', -1):>10,} rows"
                )
            elif status == "unsupported":
                console.print(f"  [dim]{engine.label:<12} {query:<5} unsupported — {payload.get('error')}[/]")
            else:
                failures += 1
                console.print(
                    f"  [red]{status}[/] {engine.label:<12} {query:<5} {payload.get('error')}"
                )

    if record_timings:
        _append_timings(cfg.paths.timings, rows)
        console.print(f"\n[dim]{len(rows)} rows appended to {cfg.paths.timings}[/]")
    return 1 if failures else 0


def _checksum_map(cfg: Config, suite: str) -> dict[str, tuple[str, ...]]:
    """Which columns get summed for a suite whose answers are too big to diff."""
    import tomllib

    with (cfg.paths.config / "suites.toml").open("rb") as fh:
        doc = tomllib.load(fh)
    return {q: tuple(cols) for q, cols in doc.get("checksum", {}).get(suite, {}).items()}
