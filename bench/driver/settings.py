"""Paths, configuration loading, and the run description shared by every step.

Environment overrides follow the same convention polars-benchmark uses, so the
two harnesses can be driven from one .env: PATH_* for locations, RUN_* for
execution knobs. Command-line flags win over the environment, which wins over
the defaults here.
"""

from __future__ import annotations

import os
import tomllib
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

BENCH_ROOT = Path(__file__).resolve().parent.parent
REPO_ROOT = BENCH_ROOT.parent


def _env_path(name: str, default: Path) -> Path:
    raw = os.environ.get(name)
    return Path(raw).expanduser().resolve() if raw else default


@dataclass(frozen=True)
class Paths:
    root: Path = BENCH_ROOT
    config: Path = field(default_factory=lambda: _env_path("PATH_CONFIG", BENCH_ROOT / "config"))
    data: Path = field(default_factory=lambda: _env_path("PATH_DATA", BENCH_ROOT / "data"))
    answers: Path = field(default_factory=lambda: _env_path("PATH_ANSWERS", BENCH_ROOT / "answers"))
    results: Path = field(default_factory=lambda: _env_path("PATH_RESULTS", BENCH_ROOT / "results"))
    engines: Path = field(default_factory=lambda: BENCH_ROOT / "engines")
    gobin: Path = field(default_factory=lambda: _env_path("PATH_GOBIN", BENCH_ROOT / "bin"))

    @property
    def raw(self) -> Path:
        """Per-run JSON emitted by each engine subprocess."""
        return self.results / "raw"

    @property
    def timings(self) -> Path:
        return self.results / "timings.csv"

    @property
    def plots(self) -> Path:
        return self.results / "plots"

    def dataset_dir(self, suite: str, size: float) -> Path:
        return self.data / suite / _size_slug(suite, size)

    def answers_dir(self, suite: str, size: float) -> Path:
        return self.answers / suite / _size_slug(suite, size)


def _size_slug(suite: str, size: float) -> str:
    """Directory name for a dataset size.

    PDS-H scale factors are small decimals (0.1, 1, 10); h2o row counts are
    large powers of ten. Both need to round-trip through a filename without
    colliding, so they get different formats.
    """
    if suite == "h2o":
        return f"n{int(size):d}"
    return f"sf{size:g}"


@dataclass(frozen=True)
class Engine:
    name: str
    lang: str
    kind: str
    label: str
    enabled: bool
    suites: tuple[str, ...]
    entry: str | None = None
    binary: str | None = None
    tag: str | None = None
    group: str | None = None
    max_scale: float | None = None
    max_rows: float | None = None
    skip_queries: tuple[str, ...] = ()
    notes: str = ""

    def ceiling_for(self, suite: str) -> float | None:
        return self.max_rows if suite == "h2o" else self.max_scale

    def supports(self, suite: str, query: str, size: float) -> tuple[bool, str]:
        """Whether this engine should attempt (suite, query) at `size`.

        Returns (ok, reason). A False here becomes status=unsupported in the
        results, which is a real datapoint — not an error.
        """
        if suite not in self.suites:
            return False, f"{self.label} has no {suite} implementation"
        if query in self.skip_queries:
            return False, f"{self.label} cannot express {query}"
        ceiling = self.ceiling_for(suite)
        if ceiling is not None and size > ceiling:
            unit = "rows" if suite == "h2o" else "SF"
            return False, f"{self.label} is capped at {ceiling:g} {unit} (asked for {size:g})"
        return True, ""


@dataclass(frozen=True)
class Suite:
    name: str
    label: str
    unit: str
    queries: tuple[str, ...]
    gb_per_unit: float
    min_free_gb_per_unit: float
    min_free_gb_floor: float
    min_disk_gb_per_unit: float
    descriptions: dict[str, str] = field(default_factory=dict)

    def estimated_gb(self, size: float) -> float:
        return self.gb_per_unit * size

    def required_free_gb(self, size: float) -> float:
        return max(self.min_free_gb_floor, self.min_free_gb_per_unit * size)

    def required_disk_gb(self, size: float) -> float:
        return max(1.0, self.min_disk_gb_per_unit * size)


@dataclass(frozen=True)
class Config:
    paths: Paths
    engines: dict[str, Engine]
    suites: dict[str, Suite]

    def engines_for(self, suite: str, selected: list[str] | None) -> list[Engine]:
        if selected:
            missing = [n for n in selected if n not in self.engines]
            if missing:
                known = ", ".join(sorted(self.engines))
                raise SystemExit(f"unknown engine(s): {', '.join(missing)}\nknown: {known}")
            return [self.engines[n] for n in selected]
        return [e for e in self.engines.values() if e.enabled and suite in e.suites]


def _load_toml(path: Path) -> dict[str, Any]:
    with path.open("rb") as fh:
        return tomllib.load(fh)


def load_config(paths: Paths | None = None) -> Config:
    paths = paths or Paths()

    raw_engines = _load_toml(paths.config / "engines.toml")["engines"]
    engines = {
        name: Engine(
            name=name,
            lang=spec["lang"],
            kind=spec["kind"],
            label=spec.get("label", name),
            enabled=bool(spec.get("enabled", True)),
            suites=tuple(spec.get("suites", ())),
            entry=spec.get("entry"),
            binary=spec.get("binary"),
            tag=spec.get("tag"),
            group=spec.get("group"),
            max_scale=spec.get("max_scale"),
            max_rows=spec.get("max_rows"),
            skip_queries=tuple(spec.get("skip_queries", ())),
            notes=spec.get("notes", ""),
        )
        for name, spec in raw_engines.items()
    }

    suites_doc = _load_toml(paths.config / "suites.toml")
    descriptions = suites_doc.get("queries", {})
    suites = {
        name: Suite(
            name=name,
            label=spec["label"],
            unit=spec["unit"],
            queries=tuple(spec.get("queries", ())),
            gb_per_unit=float(spec["gb_per_unit"]),
            min_free_gb_per_unit=float(spec["min_free_gb_per_unit"]),
            min_free_gb_floor=float(spec["min_free_gb_floor"]),
            min_disk_gb_per_unit=float(spec["min_disk_gb_per_unit"]),
            descriptions=dict(descriptions.get(name, {})),
        )
        for name, spec in suites_doc["suites"].items()
    }

    return Config(paths=paths, engines=engines, suites=suites)


@dataclass(frozen=True)
class RunSpec:
    """One invocation of the harness: what to run, how big, under what budget."""

    suite: str
    size: float
    io: str = "parquet"
    threads: int = os.cpu_count() or 1
    iterations: int = 3
    timeout: int = 600
    mem_limit: str | None = None
    engines: tuple[str, ...] = ()
    queries: tuple[str, ...] = ()
    force: bool = False

    @property
    def size_slug(self) -> str:
        return _size_slug(self.suite, self.size)

    def resolved_queries(self, suite: Suite) -> list[str]:
        if not self.queries:
            return list(suite.queries)
        unknown = [q for q in self.queries if q not in suite.queries]
        if unknown:
            raise SystemExit(
                f"unknown quer{'y' if len(unknown) == 1 else 'ies'} for {suite.name}: "
                f"{', '.join(unknown)}\nknown: {', '.join(suite.queries)}"
            )
        return list(self.queries)
