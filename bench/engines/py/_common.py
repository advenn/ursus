"""Shared harness for the Python engine runners.

Every engine — Python or Go — implements the same contract:

    runner --suite S --query Q --data DIR --io parquet|csv \\
           --iterations N --threads T --out RESULT.json --result ANSWER.parquet \\
           [--checksum-cols v1,v2]

and writes one JSON object describing what happened. The driver never times
anything itself; it reads these files. Keeping the measurement inside the engine
process is what makes a Go binary and a Python interpreter comparable: neither
is charged for the other's startup.

Timing rules, applied identically everywhere:

  * one un-timed warm-up iteration, then `--iterations` timed ones
  * the timed region is scan -> materialise, IO included; the query is rebuilt
    from scratch each iteration so nothing is cached between them
  * writing the answer for validation happens outside the timed region
  * `startup_s` (interpreter boot + imports) is reported separately, never
    folded into the query time
"""

from __future__ import annotations

import argparse
import json
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Callable

# Set as early as possible by each runner so import cost is attributed honestly.
_PROCESS_START = time.monotonic()

PDSH_TABLES = (
    "region",
    "nation",
    "supplier",
    "customer",
    "part",
    "partsupp",
    "orders",
    "lineitem",
)

H2O_TABLES = {
    "g1": ("groupby", "g1"),
    "x": ("join", "x"),
    "small": ("join", "small"),
    "medium": ("join", "medium"),
    "big": ("join", "big"),
}

# Which h2o tables each query touches. Loading only these keeps the eager
# engines from paying for tables the query never reads.
H2O_INPUTS = {
    "gb1": ("g1",), "gb2": ("g1",), "gb3": ("g1",), "gb4": ("g1",), "gb5": ("g1",),
    "gb6": ("g1",), "gb7": ("g1",), "gb8": ("g1",), "gb9": ("g1",), "gb10": ("g1",),
    "j1": ("x", "small"),
    "j2": ("x", "medium"),
    "j3": ("x", "medium"),
    "j4": ("x", "medium"),
    "j5": ("x", "big"),
}


@dataclass
class Args:
    suite: str
    query: str
    data: Path
    io: str
    iterations: int
    threads: int
    out: Path
    result: Path
    checksum_cols: tuple[str, ...] = ()

    @property
    def checksum_mode(self) -> bool:
        return bool(self.checksum_cols)

    def table_path(self, table: str) -> Path:
        """Where a named input table lives, for this suite and IO format."""
        suffix = "parquet" if self.io == "parquet" else "csv"
        if self.suite == "pdsh":
            return self.data / suffix / f"{table}.{suffix}"
        subdir, stem = H2O_TABLES[table]
        return self.data / subdir / f"{stem}.{suffix}"

    def inputs(self) -> tuple[str, ...]:
        """The tables this query reads."""
        if self.suite == "pdsh":
            return PDSH_TABLES
        return H2O_INPUTS[self.query]


def parse_args(argv: list[str] | None = None) -> Args:
    p = argparse.ArgumentParser()
    p.add_argument("--suite", required=True)
    p.add_argument("--query", required=True)
    p.add_argument("--data", required=True, type=Path)
    p.add_argument("--io", default="parquet")
    p.add_argument("--iterations", type=int, default=3)
    p.add_argument("--threads", type=int, default=0)
    p.add_argument("--out", required=True, type=Path)
    p.add_argument("--result", required=True, type=Path)
    p.add_argument("--checksum-cols", default="")
    ns = p.parse_args(argv)
    return Args(
        suite=ns.suite,
        query=ns.query,
        data=ns.data,
        io=ns.io,
        iterations=ns.iterations,
        threads=ns.threads,
        out=ns.out,
        result=ns.result,
        checksum_cols=tuple(c for c in ns.checksum_cols.split(",") if c),
    )


def peak_rss_bytes() -> int:
    """VmHWM: the high-water mark of resident memory for this process.

    Read from /proc rather than getrusage so the Go and Python runners report
    the same quantity from the same source.
    """
    try:
        for line in Path("/proc/self/status").read_text().splitlines():
            if line.startswith("VmHWM:"):
                return int(line.split()[1]) * 1024
    except OSError:
        pass
    return 0


@dataclass
class Outcome:
    engine: str
    args: Args
    iterations: list[float] = field(default_factory=list)
    rows: int = -1
    status: str = "ok"
    error: str | None = None
    startup_s: float = 0.0
    extra: dict[str, Any] = field(default_factory=dict)

    def emit(self) -> None:
        payload = {
            "engine": self.engine,
            "suite": self.args.suite,
            "query": self.args.query,
            "io": self.args.io,
            "threads": self.args.threads,
            "iterations": self.iterations,
            "rows": self.rows,
            "peak_rss_bytes": peak_rss_bytes(),
            "startup_s": round(self.startup_s, 4),
            "status": self.status,
            "error": self.error,
            "result_path": str(self.args.result) if self.status == "ok" else None,
            **self.extra,
        }
        self.args.out.parent.mkdir(parents=True, exist_ok=True)
        self.args.out.write_text(json.dumps(payload, indent=2) + "\n")


def to_arrow(answer: Any):
    """Normalise whatever an engine returned into a pyarrow Table."""
    import pyarrow as pa

    if isinstance(answer, pa.Table):
        return answer
    # polars DataFrame
    if hasattr(answer, "to_arrow"):
        result = answer.to_arrow()
        return result if isinstance(result, pa.Table) else pa.table(result)
    # pandas DataFrame
    if hasattr(answer, "to_dict") and hasattr(answer, "columns"):
        return pa.Table.from_pandas(answer, preserve_index=False)
    raise TypeError(f"cannot convert {type(answer)!r} to a pyarrow Table")


def checksum(table, columns: tuple[str, ...]):
    """Row count plus the sum of each named column, as a one-row table.

    Nulls are skipped, matching SQL sum() semantics, so an engine that
    represents a missing value differently from its neighbours does not fail
    validation for that reason alone.
    """
    import pyarrow as pa
    import pyarrow.compute as pc

    data: dict[str, Any] = {"rows": pa.array([table.num_rows], type=pa.int64())}
    for name in columns:
        if name not in table.column_names:
            raise KeyError(
                f"answer is missing checksum column {name!r}; "
                f"has {', '.join(table.column_names)}"
            )
        total = pc.sum(table.column(name))
        value = None if total.as_py() is None else float(total.as_py())
        data[f"sum_{name}"] = pa.array([value], type=pa.float64())
    return pa.table(data)


def write_answer(args: Args, answer: Any) -> int:
    """Persist the answer for validation. Returns its row count.

    Called outside the timed region: what this costs is not the engine's fault.
    """
    import pyarrow.parquet as pq

    table = to_arrow(answer)
    rows = table.num_rows
    if args.checksum_mode:
        table = checksum(table, args.checksum_cols)
    args.result.parent.mkdir(parents=True, exist_ok=True)
    pq.write_table(table, args.result, compression="zstd")
    return rows


def run(engine: str, build: Callable[[Args], Callable[[], Any]]) -> int:
    """Drive one engine end to end.

    `build` is given the parsed args and returns a zero-argument callable that
    executes the query once and returns its answer. It is called fresh for every
    iteration so no plan, frame or file handle survives between them.
    """
    args = parse_args()
    outcome = Outcome(engine=engine, args=args, startup_s=time.monotonic() - _PROCESS_START)

    try:
        once = build(args)

        once()  # warm-up: page cache, JIT, lazily-initialised thread pools

        answer = None
        for _ in range(args.iterations):
            started = time.perf_counter()
            answer = once()
            outcome.iterations.append(time.perf_counter() - started)

        outcome.rows = write_answer(args, answer)
    except NotImplementedError as exc:
        outcome.status = "unsupported"
        outcome.error = str(exc)
    except Exception as exc:  # noqa: BLE001 - any engine failure is a datapoint
        outcome.status = "error"
        outcome.error = f"{type(exc).__name__}: {exc}"

    outcome.emit()
    return 0 if outcome.status in ("ok", "unsupported") else 1
