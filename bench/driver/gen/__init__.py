"""Dataset generation.

Both generators are idempotent: they write a manifest.json next to the data and
skip the work when an existing manifest matches the requested size and format.
Delete the directory (or `make clean-data`) to force regeneration.
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

from ..settings import Config, RunSpec


def manifest_path(root: Path) -> Path:
    return root / "manifest.json"


def load_manifest(root: Path) -> dict[str, Any] | None:
    path = manifest_path(root)
    if not path.exists():
        return None
    try:
        return json.loads(path.read_text())
    except (OSError, json.JSONDecodeError):
        return None


def write_manifest(root: Path, payload: dict[str, Any]) -> None:
    manifest_path(root).write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n")


def run(cfg: Config, spec: RunSpec, console) -> int:
    if spec.suite == "pdsh":
        from . import pdsh

        return pdsh.generate(cfg, spec, console)
    if spec.suite == "h2o":
        from . import h2o

        return h2o.generate(cfg, spec, console)
    if spec.suite == "micro":
        console.print("[dim]micro suite needs no generated data[/]")
        return 0
    console.print(f"[red]unknown suite[/] {spec.suite}")
    return 2
