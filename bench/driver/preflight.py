"""The resource gate.

A benchmark run started on a machine that is already out of memory does not
produce a slow result; it produces a wrong one, because every engine ends up
measuring the page cache and the swap device instead of itself. This module
refuses to let that happen silently.

`check()` returns a Report. `main.py` prints it and exits non-zero when the
report is not viable, unless --force was passed.
"""

from __future__ import annotations

import shutil
import subprocess
from dataclasses import dataclass, field
from pathlib import Path

from .settings import Config, RunSpec, Suite

GIB = 1024**3

# Tools the harness needs, and what breaks without each.
TOOLS: dict[str, tuple[bool, str]] = {
    # name: (required, what it is used for)
    "uv": (True, "python venv and the python engines"),
    "go": (True, "building the Go runners"),
    "systemd-run": (False, "enforced per-run memory budget (falls back to unbounded)"),
    "taskset": (False, "CPU pinning (falls back to whatever the scheduler picks)"),
    "benchstat": (False, "micro-tier regression diffs (make micro-diff)"),
}


@dataclass
class MemInfo:
    total_gb: float
    available_gb: float
    used_gb: float
    swap_total_gb: float
    swap_used_gb: float


@dataclass
class Report:
    cores: int
    mem: MemInfo
    disk_free_gb: float
    required_free_gb: float
    required_disk_gb: float
    estimated_data_gb: float
    tools: dict[str, str | None]
    problems: list[str] = field(default_factory=list)
    warnings: list[str] = field(default_factory=list)
    hogs: list[tuple[str, float]] = field(default_factory=list)

    @property
    def viable(self) -> bool:
        return not self.problems


def read_meminfo(path: Path = Path("/proc/meminfo")) -> MemInfo:
    fields: dict[str, int] = {}
    for line in path.read_text().splitlines():
        key, _, rest = line.partition(":")
        value = rest.strip().split()
        if value:
            fields[key] = int(value[0])  # kB

    def gb(key: str) -> float:
        return fields.get(key, 0) * 1024 / GIB

    total = gb("MemTotal")
    available = gb("MemAvailable")
    swap_total = gb("SwapTotal")
    swap_free = gb("SwapFree")
    return MemInfo(
        total_gb=total,
        available_gb=available,
        used_gb=total - available,
        swap_total_gb=swap_total,
        swap_used_gb=swap_total - swap_free,
    )


def top_memory_consumers(n: int = 8) -> list[tuple[str, float]]:
    """Best-effort list of (name, RSS GiB), largest first."""
    try:
        import psutil
    except ImportError:
        return []

    rows: dict[str, float] = {}
    for proc in psutil.process_iter(["name", "memory_info"]):
        try:
            info = proc.info
            rss = info["memory_info"].rss / GIB
        except (psutil.NoSuchProcess, psutil.AccessDenied, AttributeError, TypeError):
            continue
        name = info["name"] or "?"
        rows[name] = rows.get(name, 0.0) + rss
    return sorted(rows.items(), key=lambda kv: -kv[1])[:n]


def _tool_versions() -> dict[str, str | None]:
    found: dict[str, str | None] = {}
    for name in TOOLS:
        path = shutil.which(name)
        if path is None:
            found[name] = None
            continue
        # `go --version` is not a thing; every other tool here takes --version.
        argv = [name, "version"] if name == "go" else [name, "--version"]
        try:
            out = subprocess.run(
                argv, capture_output=True, text=True, timeout=10
            ).stdout.strip()
        except (OSError, subprocess.SubprocessError):
            out = ""
        found[name] = out.splitlines()[0] if out else path
    return found


def memory_controller_delegated() -> bool:
    """Whether `systemd-run --user` can actually enforce MemoryMax here.

    Without the memory controller delegated to the user slice, the -p MemoryMax
    property is accepted and then ignored, which is worse than not asking for it.
    """
    import os

    path = Path(f"/sys/fs/cgroup/user.slice/user-{os.getuid()}.slice/cgroup.controllers")
    try:
        return "memory" in path.read_text().split()
    except OSError:
        return False


def check(cfg: Config, spec: RunSpec, suite: Suite) -> Report:
    import os

    mem = read_meminfo()
    cores = os.cpu_count() or 1

    data_root = cfg.paths.data
    probe = data_root if data_root.exists() else cfg.paths.root
    disk_free_gb = shutil.disk_usage(probe).free / GIB

    required_free = suite.required_free_gb(spec.size)
    required_disk = suite.required_disk_gb(spec.size)
    estimated_data = suite.estimated_gb(spec.size)

    report = Report(
        cores=cores,
        mem=mem,
        disk_free_gb=disk_free_gb,
        required_free_gb=required_free,
        required_disk_gb=required_disk,
        estimated_data_gb=estimated_data,
        tools=_tool_versions(),
    )

    if mem.available_gb < required_free:
        report.problems.append(
            f"{mem.available_gb:.1f} GiB RAM available, {required_free:.1f} GiB needed for "
            f"{suite.name} at {spec.size:g} {suite.unit}. A run started here measures swap."
        )
        report.hogs = top_memory_consumers()

    if disk_free_gb < required_disk:
        report.problems.append(
            f"{disk_free_gb:.1f} GiB free on {probe}, {required_disk:.1f} GiB needed for the "
            f"{suite.name} dataset and results."
        )

    for name, (required, purpose) in TOOLS.items():
        if report.tools[name] is None:
            msg = f"{name} not found — needed for {purpose}"
            (report.problems if required else report.warnings).append(msg)

    if mem.swap_used_gb > 0.5:
        report.warnings.append(
            f"{mem.swap_used_gb:.1f} GiB of swap is already in use. Even a run that fits in RAM "
            f"will contend with swap-in from whatever got paged out."
        )

    if report.tools["systemd-run"] is not None and not memory_controller_delegated():
        report.warnings.append(
            "the memory cgroup controller is not delegated to this user slice, so "
            "-p MemoryMax cannot be enforced; MEM_LIMIT will be ignored."
        )

    if spec.threads > cores:
        report.warnings.append(
            f"--threads {spec.threads} exceeds {cores} cores; engines will oversubscribe."
        )

    return report
