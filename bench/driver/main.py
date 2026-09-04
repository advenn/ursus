"""CLI entry point: python -m driver.main <command> [flags].

Commands
    preflight   resource + tooling gate
    gen         generate (and normalise) datasets
    answers     build the duckdb reference results
    run         execute engines and record timings
    validate    compare engine results against the reference
    report      render REPORT.md, plots and dashboard.html
"""

from __future__ import annotations

import argparse
import sys

from rich.console import Console
from rich.table import Table

from .cgroup import Budget
from .preflight import Report, check
from .settings import Config, RunSpec, load_config

console = Console(stderr=True, highlight=False)


# ------------------------------------------------------------------ flags ---


def _add_common(p: argparse.ArgumentParser) -> None:
    p.add_argument("--suite", default="pdsh", help="pdsh | h2o | micro")
    p.add_argument("--scale", type=float, default=1.0, help="PDS-H scale factor")
    p.add_argument("--rows", type=float, default=1e7, help="h2o row count")
    p.add_argument("--io", default="parquet", choices=["parquet", "csv"])
    p.add_argument("--threads", type=int, default=0, help="0 = all cores")
    p.add_argument("--iterations", type=int, default=3)
    p.add_argument("--timeout", type=int, default=600, help="seconds, per query")
    p.add_argument("--mem-limit", default=None, help="systemd byte spec, e.g. 8G")
    p.add_argument("--engines", default="", help="comma list; empty = all enabled")
    p.add_argument("--queries", default="", help="comma list; empty = all")
    p.add_argument("--force", action="store_true", help="ignore a failing preflight")


def _split(value: str) -> tuple[str, ...]:
    return tuple(x.strip() for x in value.split(",") if x.strip())


def _spec(args: argparse.Namespace) -> RunSpec:
    import os

    size = args.rows if args.suite == "h2o" else args.scale
    return RunSpec(
        suite=args.suite,
        size=size,
        io=args.io,
        threads=args.threads or (os.cpu_count() or 1),
        iterations=args.iterations,
        timeout=args.timeout,
        mem_limit=args.mem_limit,
        engines=_split(args.engines),
        queries=_split(args.queries),
        force=args.force,
    )


# -------------------------------------------------------------- preflight ---


def _print_report(rep: Report, cfg: Config, spec: RunSpec) -> None:
    suite = cfg.suites[spec.suite]
    console.rule(f"[bold]preflight[/] — {suite.label} at {spec.size:g} {suite.unit}")

    t = Table(box=None, pad_edge=False, show_header=False)
    t.add_column(style="dim", width=22)
    t.add_column()
    t.add_row("cores", str(rep.cores))
    t.add_row(
        "memory",
        f"{rep.mem.available_gb:.1f} GiB available of {rep.mem.total_gb:.1f} GiB "
        f"(need {rep.required_free_gb:.1f})",
    )
    if rep.mem.swap_total_gb:
        t.add_row(
            "swap", f"{rep.mem.swap_used_gb:.1f} GiB used of {rep.mem.swap_total_gb:.1f} GiB"
        )
    t.add_row(
        "disk",
        f"{rep.disk_free_gb:.1f} GiB free (need {rep.required_disk_gb:.1f}, "
        f"dataset ~{rep.estimated_data_gb:.1f})",
    )
    for name, version in rep.tools.items():
        t.add_row(name, version or "[red]not found[/]")
    console.print(t)

    for w in rep.warnings:
        console.print(f"[yellow]warning[/] {w}")

    if rep.problems:
        console.print()
        for p in rep.problems:
            console.print(f"[red]blocked[/] {p}")
    if rep.hogs:
        console.print("\n[dim]largest memory consumers:[/]")
        for name, gb in rep.hogs:
            if gb >= 0.1:
                console.print(f"  {gb:6.1f} GiB  {name}")

    if rep.viable:
        console.print("\n[green]ok[/] — machine can run this honestly")


def cmd_preflight(cfg: Config, spec: RunSpec) -> int:
    if spec.suite not in cfg.suites:
        console.print(f"[red]unknown suite[/] {spec.suite}")
        return 2
    rep = check(cfg, spec, cfg.suites[spec.suite])
    _print_report(rep, cfg, spec)
    if not rep.viable:
        if spec.force:
            console.print("\n[yellow]--force given: continuing anyway. Results are not "
                          "trustworthy and the report will say so.[/]")
            return 0
        console.print(
            "\nRefusing to run. Free memory and retry, lower the scale "
            "(`make bench SF=0.1`), or pass FORCE=1 to override."
        )
        return 1
    return 0


# ------------------------------------------------------------------ other ---


def cmd_gen(cfg: Config, spec: RunSpec) -> int:
    from . import gen

    return gen.run(cfg, spec, console)


def cmd_answers(cfg: Config, spec: RunSpec) -> int:
    from . import answers

    return answers.run(cfg, spec, console)


def cmd_run(cfg: Config, spec: RunSpec) -> int:
    from . import runner

    budget = Budget(mem_limit=spec.mem_limit, cpus=spec.threads)
    return runner.run(cfg, spec, budget, console)


def cmd_validate(cfg: Config, spec: RunSpec) -> int:
    from . import validate

    return validate.run(cfg, spec, console)


def cmd_report(cfg: Config, spec: RunSpec) -> int:
    from . import report

    return report.run(cfg, console)


COMMANDS = {
    "preflight": cmd_preflight,
    "gen": cmd_gen,
    "answers": cmd_answers,
    "run": cmd_run,
    "validate": cmd_validate,
    "report": cmd_report,
}


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="driver", description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    for name in COMMANDS:
        p = sub.add_parser(name)
        _add_common(p)

    args = parser.parse_args(argv)
    cfg = load_config()
    spec = _spec(args)

    cfg.paths.results.mkdir(parents=True, exist_ok=True)
    cfg.paths.raw.mkdir(parents=True, exist_ok=True)

    return COMMANDS[args.command](cfg, spec)


if __name__ == "__main__":
    sys.exit(main())
