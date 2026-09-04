"""Build the reference results every other engine is validated against.

duckdb produces them, by running exactly the same code path a timed duckdb run
takes — same runner, same SQL, same checksum rules — with one iteration and the
output redirected into answers/. Generating the reference through the ordinary
runner rather than a bespoke script is deliberate: a reference produced by
special-case code would be free to disagree with the thing it is meant to check.

Timings from this pass are written to results/raw/ like any other but are not
appended to timings.csv, because a one-iteration cold run is not a measurement.
"""

from __future__ import annotations

from dataclasses import replace

from .cgroup import Budget
from .settings import Config, RunSpec


def run(cfg: Config, spec: RunSpec, console) -> int:
    from . import runner

    if spec.suite not in cfg.suites:
        console.print(f"[red]unknown suite[/] {spec.suite}")
        return 2
    if "duckdb" not in cfg.engines:
        console.print("[red]no duckdb engine registered[/] — cannot build reference answers")
        return 2

    suite = cfg.suites[spec.suite]
    queries = spec.resolved_queries(suite)
    target = cfg.paths.answers_dir(spec.suite, spec.size)
    missing = [q for q in queries if not (target / f"{q}.parquet").exists()]
    if not missing:
        console.print(f"[green]answers ready[/] {target}")
        return 0

    console.rule(f"[bold]building reference answers[/] ({len(missing)} of {len(queries)})")

    # The reference is a correctness artefact, not a timing one: one iteration,
    # no memory ceiling (an OOM here is a broken reference, not a datapoint).
    reference_spec = replace(spec, iterations=1, queries=tuple(missing), engines=("duckdb",))
    budget = Budget(mem_limit=None, cpus=spec.threads, enforce=False)

    # answer_root is laid out as <root>/<engine>/<suite>/<slug>/<query>.parquet,
    # so pointing it two levels up puts duckdb's output exactly where
    # Paths.answers_dir expects to find it.
    answer_root = cfg.paths.answers / "_by_engine"
    code = runner.run(cfg, reference_spec, budget, console,
                      answer_root=answer_root, only_engine="duckdb",
                      record_timings=False)

    produced = answer_root / "duckdb" / spec.suite / spec.size_slug / spec.io
    target.mkdir(parents=True, exist_ok=True)
    moved = 0
    for query in missing:
        src = produced / f"{query}.parquet"
        if src.exists():
            src.replace(target / f"{query}.parquet")
            moved += 1
    console.print(f"[green]{moved}[/] reference answers in {target}")
    return code if moved == len(missing) else 1
