"""Turn timings.csv into something a human can act on.

Three outputs, from the same aggregation:

  results/REPORT.md        per-suite tables, geomean, speedup vs the baseline
  results/plots/*.png|svg  grouped bars, log scale, runtime and peak memory
  results/dashboard.html   one self-contained page, sortable, light and dark

Aggregation rules: for each (engine, suite, query, size, io) the most recent run
wins, and its reported time is the median across iterations. Median rather than
minimum because the minimum flatters whichever engine got the quietest slice of
the machine, and this suite already goes to some trouble to make the slices
comparable.

Anything that failed validation is kept in the table and struck through. Hiding
it would turn a wrong answer into a missing one.
"""

from __future__ import annotations

import csv
import html
import json
import math
import subprocess
from collections import defaultdict
from datetime import date
from dataclasses import dataclass, field
from pathlib import Path

from .settings import Config

BASELINE = "polars"  # what the speedup column is relative to, when present


@dataclass
class Cell:
    engine: str
    suite: str
    query: str
    size: float
    io: str
    seconds: float | None = None
    rows: int = -1
    peak_rss_bytes: int = 0
    startup_s: float = 0.0
    status: str = "error"
    error: str = ""
    valid: bool | None = None
    samples: list[float] = field(default_factory=list)

    @property
    def key(self) -> tuple:
        return (self.engine, self.suite, self.query, self.size, self.io)

    @property
    def ms(self) -> float | None:
        return None if self.seconds is None else self.seconds * 1000

    @property
    def usable(self) -> bool:
        return self.status == "ok" and self.seconds is not None and self.valid is not False


def _median(values: list[float]) -> float | None:
    if not values:
        return None
    ordered = sorted(values)
    mid = len(ordered) // 2
    if len(ordered) % 2:
        return ordered[mid]
    return (ordered[mid - 1] + ordered[mid]) / 2


def load(cfg: Config) -> list[Cell]:
    path = cfg.paths.timings
    if not path.exists():
        return []

    latest: dict[tuple, str] = {}
    grouped: dict[tuple, list[dict]] = defaultdict(list)
    with path.open(newline="") as fh:
        for row in csv.DictReader(fh):
            key = (row["engine"], row["suite"], row["query"], float(row["size"]), row["io"])
            stamp = row["timestamp"]
            if key not in latest or stamp > latest[key]:
                latest[key] = stamp
                grouped[key] = []
            if stamp == latest[key]:
                grouped[key].append(row)

    verdicts = _load_validation(cfg)

    cells: list[Cell] = []
    for key, rows in grouped.items():
        head = rows[0]
        samples = [float(r["seconds"]) for r in rows if r["seconds"]]
        cells.append(
            Cell(
                engine=key[0], suite=key[1], query=key[2], size=key[3], io=key[4],
                seconds=_median(samples),
                rows=int(head["rows"]),
                peak_rss_bytes=int(head["peak_rss_bytes"]),
                startup_s=float(head["startup_s"]),
                status=head["status"],
                error=head["error"],
                valid=verdicts.get(key),
                samples=samples,
            )
        )
    return cells


def _load_validation(cfg: Config) -> dict[tuple, bool]:
    path = cfg.paths.results / "validation.csv"
    if not path.exists():
        return {}
    verdicts: dict[tuple, bool] = {}
    with path.open(newline="") as fh:
        for row in csv.DictReader(fh):
            key = (row["engine"], row["suite"], row["query"], float(row["size"]), row["io"])
            verdicts[key] = row["valid"] == "1"
    return verdicts


def geomean(values: list[float]) -> float | None:
    positive = [v for v in values if v and v > 0]
    if not positive:
        return None
    return math.exp(sum(math.log(v) for v in positive) / len(positive))


# ------------------------------------------------------------------ tables ---


def _label(cfg: Config, engine: str) -> str:
    return cfg.engines[engine].label if engine in cfg.engines else engine


def _cell_text(cell: Cell | None) -> str:
    if cell is None:
        return "·"
    if cell.status == "unsupported":
        return "n/a"
    if cell.status in ("timeout", "oom"):
        return cell.status.upper()
    if cell.status != "ok" or cell.ms is None:
        return "ERR"
    text = f"{cell.ms:,.0f}"
    return f"~~{text}~~" if cell.valid is False else text


def _grid(cells: list[Cell]) -> tuple[list[str], list[str], dict[tuple[str, str], Cell]]:
    engines, queries = [], []
    grid: dict[tuple[str, str], Cell] = {}
    for cell in cells:
        if cell.engine not in engines:
            engines.append(cell.engine)
        if cell.query not in queries:
            queries.append(cell.query)
        grid[(cell.query, cell.engine)] = cell
    queries.sort(key=_query_order)
    return engines, queries, grid


def _query_order(name: str) -> tuple:
    """q1 < q2 < q10, gb1 < gb2 < gb10 < j1."""
    prefix = name.rstrip("0123456789")
    digits = name[len(prefix):]
    return (prefix, int(digits) if digits else 0)


def _provenance() -> str:
    """One line saying which ursus this report describes.

    REPORT.md is the one results file that is committed, and README.md links to
    it, so a reader meets these numbers with no way of knowing how old they are.
    It had no date and no commit for its whole life: the published tables
    described the engine as it stood at the min/max rewrite, while three later
    commits changed the CSV reader, the group-key table in six operators and the
    radix sort. Every one of those could move a number in here.

    Stale numbers are not the problem — regenerating them costs a full
    multi-engine run. Stale numbers that do not SAY they are stale are.
    """
    try:
        sha = subprocess.run(
            ["git", "rev-parse", "--short", "HEAD"],
            capture_output=True, text=True, check=True,
        ).stdout.strip()
        subject = subprocess.run(
            ["git", "log", "-1", "--format=%s"],
            capture_output=True, text=True, check=True,
        ).stdout.strip()
        return f"Produced from `{sha}` — {subject} — on {date.today().isoformat()}."
    except (subprocess.CalledProcessError, FileNotFoundError):
        # Not a checkout, or no git. Say so rather than omitting the line, which
        # is the state this function exists to end.
        return f"Produced on {date.today().isoformat()}; source revision unknown."


def markdown(cfg: Config, cells: list[Cell]) -> str:
    out: list[str] = [
        "# ursus benchmark results",
        "",
        _provenance(),
        "",
        "Median wall-clock over the timed iterations, in milliseconds; lower is better.",
        "IO is included in the measurement.",
        "",
        "| cell | meaning |",
        "|---|---|",
        "| `n/a` | the engine cannot express this query — see its notes in `config/engines.toml` |",
        "| `·` | not run |",
        "| `OOM` / `TIMEOUT` | stopped by the resource budget |",
        "| `ERR` | the engine failed; hover or see the failures list under each table |",
        "| ~~struck through~~ | disagreed with the duckdb reference. A wrong answer, not a fast one |",
        "",
        "**geomean is over the queries that engine passed**, so it is only comparable",
        "between engines with the same coverage — check the `queries passed` row before",
        "reading a speedup.",
        "",
    ]

    by_run = defaultdict(list)
    for cell in cells:
        by_run[(cell.suite, cell.size, cell.io)].append(cell)

    for (suite, size, io), group in sorted(by_run.items()):
        label = cfg.suites[suite].label if suite in cfg.suites else suite
        unit = cfg.suites[suite].unit if suite in cfg.suites else ""
        out += [f"## {label} — {size:g} {unit}, io={io}", ""]

        engines, queries, grid = _grid(group)
        descriptions = cfg.suites[suite].descriptions if suite in cfg.suites else {}

        header = ["query", *[_label(cfg, e) for e in engines]]
        if descriptions:
            header.append("what it exercises")
        out.append("| " + " | ".join(header) + " |")
        out.append("|" + "|".join(["---"] + ["--:"] * len(engines) + (["---"] if descriptions else [])) + "|")

        for query in queries:
            row = [query] + [_cell_text(grid.get((query, e))) for e in engines]
            if descriptions:
                row.append(descriptions.get(query, ""))
            out.append("| " + " | ".join(row) + " |")

        geo = {
            e: geomean([grid[(q, e)].ms for q in queries
                        if (q, e) in grid and grid[(q, e)].usable and grid[(q, e)].ms])
            for e in engines
        }
        out.append("| **geomean** | " + " | ".join(
            f"**{geo[e]:,.0f}**" if geo[e] else "—" for e in engines
        ) + (" | |" if descriptions else " |"))

        if BASELINE in geo and geo[BASELINE]:
            out.append("| **vs " + BASELINE + "** | " + " | ".join(
                f"{geo[e] / geo[BASELINE]:.2f}x" if geo[e] else "—" for e in engines
            ) + (" | |" if descriptions else " |"))

        covered = {e: sum(1 for q in queries if (q, e) in grid and grid[(q, e)].usable)
                   for e in engines}
        out.append("| **queries passed** | " + " | ".join(
            f"{covered[e]}/{len(queries)}" for e in engines
        ) + (" | |" if descriptions else " |"))
        out.append("")

        peak = {e: max((grid[(q, e)].peak_rss_bytes for q in queries
                        if (q, e) in grid and grid[(q, e)].usable), default=0)
                for e in engines}
        out += ["Peak resident memory across the suite (GB):", ""]
        out.append("| " + " | ".join(_label(cfg, e) for e in engines) + " |")
        out.append("|" + "|".join(["--:"] * len(engines)) + "|")
        out.append("| " + " | ".join(f"{peak[e] / 1e9:.2f}" if peak[e] else "—"
                                     for e in engines) + " |")
        out.append("")

        problems = [c for c in group if c.status not in ("ok", "unsupported")]
        if problems:
            out += ["<details><summary>failures</summary>", ""]
            for cell in sorted(problems, key=lambda c: (c.engine, _query_order(c.query))):
                out.append(f"- `{cell.engine}` **{cell.query}** — {cell.status}: {cell.error}")
            out += ["", "</details>", ""]

    return "\n".join(out) + "\n"


# ------------------------------------------------------------------- plots ---


def plots(cfg: Config, cells: list[Cell]) -> list[Path]:
    import matplotlib

    matplotlib.use("Agg")
    import matplotlib.pyplot as plt
    import numpy as np

    cfg.paths.plots.mkdir(parents=True, exist_ok=True)
    written: list[Path] = []

    by_run = defaultdict(list)
    for cell in cells:
        by_run[(cell.suite, cell.size, cell.io)].append(cell)

    for (suite, size, io), group in sorted(by_run.items()):
        engines, queries, grid = _grid(group)
        if not engines or not queries:
            continue

        width = 0.8 / len(engines)
        positions = np.arange(len(queries))
        fig, (ax_time, ax_mem) = plt.subplots(
            2, 1, figsize=(max(8, len(queries) * 0.9), 8), height_ratios=[2, 1]
        )

        for index, engine in enumerate(engines):
            times = [
                grid[(q, engine)].ms if (q, engine) in grid and grid[(q, engine)].usable else 0
                for q in queries
            ]
            mems = [
                grid[(q, engine)].peak_rss_bytes / 1e9
                if (q, engine) in grid and grid[(q, engine)].usable else 0
                for q in queries
            ]
            offset = (index - (len(engines) - 1) / 2) * width
            ax_time.bar(positions + offset, times, width, label=_label(cfg, engine))
            ax_mem.bar(positions + offset, mems, width, label=_label(cfg, engine))

        unit = cfg.suites[suite].unit if suite in cfg.suites else ""
        ax_time.set_yscale("log")
        ax_time.set_ylabel("median runtime (ms, log)")
        ax_time.set_title(f"{suite} — {size:g} {unit}, io={io} (missing bar = n/a or failed)")
        ax_time.set_xticks(positions, queries, rotation=45, ha="right")
        ax_time.legend(ncol=min(len(engines), 4), fontsize="small")
        ax_time.grid(axis="y", alpha=0.3)

        ax_mem.set_ylabel("peak RSS (GB)")
        ax_mem.set_xticks(positions, queries, rotation=45, ha="right")
        ax_mem.grid(axis="y", alpha=0.3)

        fig.tight_layout()
        # Not with_suffix: a scale factor of 0.1 makes ".1_parquet" look like a
        # suffix, and every plot would land on top of the same file.
        stem = f"{suite}_{size:g}_{io}".replace(".", "p")
        for extension in ("png", "svg"):
            path = cfg.paths.plots / f"{stem}.{extension}"
            fig.savefig(path, dpi=140)
            written.append(path)
        plt.close(fig)

    return written


# --------------------------------------------------------------- dashboard ---

_CSS = """
:root{--bg:#fff;--fg:#16181d;--muted:#6b7280;--line:#e5e7eb;--accent:#2563eb;
--bad:#dc2626;--ok:#15803d;--chip:#f3f4f6}
@media (prefers-color-scheme:dark){:root{--bg:#0f1115;--fg:#e6e8ec;--muted:#9aa1ad;
--line:#262b33;--accent:#60a5fa;--bad:#f87171;--ok:#4ade80;--chip:#1a1e25}}
*{box-sizing:border-box}
body{margin:0;padding:2rem 1.25rem;background:var(--bg);color:var(--fg);
font:15px/1.55 ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif}
main{max-width:1200px;margin:0 auto}
h1{font-size:1.6rem;margin:0 0 .25rem}
h2{font-size:1.15rem;margin:2.5rem 0 .5rem}
p.sub{color:var(--muted);margin:0 0 2rem}
.scroll{overflow-x:auto;border:1px solid var(--line);border-radius:10px}
table{border-collapse:collapse;width:100%;font-variant-numeric:tabular-nums}
th,td{padding:.5rem .7rem;text-align:right;border-bottom:1px solid var(--line);
white-space:nowrap}
th:first-child,td:first-child{text-align:left;position:sticky;left:0;background:var(--bg)}
th{cursor:pointer;user-select:none;font-weight:600;color:var(--muted);
font-size:.82rem;text-transform:uppercase;letter-spacing:.04em}
th:hover{color:var(--accent)}
tbody tr:hover td{background:var(--chip)}
td.best{color:var(--ok);font-weight:600}
td.na{color:var(--muted)}
td.bad{color:var(--bad)}
td.bad span{text-decoration:line-through}
tfoot td{font-weight:600;border-top:2px solid var(--line)}
.desc{color:var(--muted);font-size:.85rem;text-align:left;white-space:normal;
max-width:26rem}
.legend{color:var(--muted);font-size:.85rem;margin:.6rem 0 0}
"""

_JS = """
document.querySelectorAll('table').forEach(function(table){
  table.querySelectorAll('th').forEach(function(th, index){
    th.addEventListener('click', function(){
      var body = table.tBodies[0];
      var rows = Array.prototype.slice.call(body.rows);
      var asc = th.dataset.asc !== 'true';
      th.dataset.asc = asc;
      rows.sort(function(a, b){
        var x = a.cells[index].dataset.v, y = b.cells[index].dataset.v;
        var nx = parseFloat(x), ny = parseFloat(y);
        var both = !isNaN(nx) && !isNaN(ny);
        if (both) return asc ? nx - ny : ny - nx;
        return asc ? String(x).localeCompare(y) : String(y).localeCompare(x);
      });
      rows.forEach(function(r){ body.appendChild(r); });
    });
  });
});
"""


def dashboard(cfg: Config, cells: list[Cell]) -> str:
    by_run = defaultdict(list)
    for cell in cells:
        by_run[(cell.suite, cell.size, cell.io)].append(cell)

    parts = [
        "<title>ursus benchmark results</title>",
        f"<style>{_CSS}</style>",
        "<main>",
        "<h1>ursus benchmark results</h1>",
        "<p class='sub'>Median wall-clock per query, milliseconds, IO included. "
        "Lower is better; the fastest engine per row is highlighted. Click a column "
        "heading to sort.</p>",
    ]

    for (suite, size, io), group in sorted(by_run.items()):
        label = cfg.suites[suite].label if suite in cfg.suites else suite
        unit = cfg.suites[suite].unit if suite in cfg.suites else ""
        engines, queries, grid = _grid(group)
        descriptions = cfg.suites[suite].descriptions if suite in cfg.suites else {}

        parts.append(f"<h2>{html.escape(label)} — {size:g} {html.escape(unit)}, io={html.escape(io)}</h2>")
        parts.append("<div class='scroll'><table><thead><tr><th>query</th>")
        parts += [f"<th>{html.escape(_label(cfg, e))}</th>" for e in engines]
        if descriptions:
            parts.append("<th>what it exercises</th>")
        parts.append("</tr></thead><tbody>")

        for query in queries:
            row = [f"<tr><td data-v='{html.escape(query)}'>{html.escape(query)}</td>"]
            usable = [grid[(query, e)].ms for e in engines
                      if (query, e) in grid and grid[(query, e)].usable and grid[(query, e)].ms]
            best = min(usable) if usable else None
            for engine in engines:
                cell = grid.get((query, engine))
                if cell is None or cell.status == "unsupported":
                    row.append("<td class='na' data-v='inf'>n/a</td>")
                elif cell.status in ("timeout", "oom"):
                    row.append(f"<td class='bad' data-v='inf'>{cell.status.upper()}</td>")
                elif cell.status != "ok" or cell.ms is None:
                    row.append(f"<td class='bad' data-v='inf' title='{html.escape(cell.error)}'>ERR</td>")
                elif cell.valid is False:
                    row.append(
                        f"<td class='bad' data-v='{cell.ms:.3f}' title='failed validation'>"
                        f"<span>{cell.ms:,.0f}</span></td>"
                    )
                else:
                    css = "best" if best is not None and cell.ms <= best * 1.001 else ""
                    row.append(f"<td class='{css}' data-v='{cell.ms:.3f}'>{cell.ms:,.0f}</td>")
            if descriptions:
                text = html.escape(descriptions.get(query, ""))
                row.append(f"<td class='desc' data-v='{text}'>{text}</td>")
            row.append("</tr>")
            parts.append("".join(row))

        parts.append("</tbody><tfoot><tr><td>geomean</td>")
        for engine in engines:
            values = [grid[(q, engine)].ms for q in queries
                      if (q, engine) in grid and grid[(q, engine)].usable and grid[(q, engine)].ms]
            geo = geomean(values)
            parts.append(f"<td data-v='{geo or 0:.3f}'>{f'{geo:,.0f}' if geo else '—'}</td>")
        if descriptions:
            parts.append("<td></td>")
        parts.append("</tr><tr><td>queries passed</td>")
        for engine in engines:
            passed = sum(1 for q in queries
                         if (q, engine) in grid and grid[(q, engine)].usable)
            parts.append(f"<td data-v='{passed}'>{passed}/{len(queries)}</td>")
        if descriptions:
            parts.append("<td></td>")
        parts.append("</tr></tfoot></table></div>")
        parts.append(
            "<p class='legend'>n/a = the engine cannot express this query · "
            "· = not run · struck through = answer disagreed with the duckdb "
            "reference · geomean covers only the queries that engine passed, so "
            "compare it against the row above</p>"
        )

    parts.append("</main>")
    parts.append(f"<script>{_JS}</script>")
    return "\n".join(parts)


# ---------------------------------------------------------------- entrypoint -


def run(cfg: Config, console) -> int:
    cells = load(cfg)
    if not cells:
        console.print(f"[yellow]nothing to report[/] — {cfg.paths.timings} is empty")
        return 0

    report_path = cfg.paths.results / "REPORT.md"
    report_path.write_text(markdown(cfg, cells))

    dashboard_path = cfg.paths.results / "dashboard.html"
    dashboard_path.write_text(dashboard(cfg, cells))

    summary = cfg.paths.results / "summary.json"
    summary.write_text(json.dumps(
        [
            {
                "engine": c.engine, "suite": c.suite, "query": c.query, "size": c.size,
                "io": c.io, "median_ms": c.ms, "rows": c.rows,
                "peak_rss_bytes": c.peak_rss_bytes, "status": c.status, "valid": c.valid,
            }
            for c in sorted(cells, key=lambda c: (c.suite, c.size, _query_order(c.query), c.engine))
        ],
        indent=2,
    ) + "\n")

    written = plots(cfg, cells)

    console.print(f"[green]report[/]     {report_path}")
    console.print(f"[green]dashboard[/]  {dashboard_path}")
    console.print(f"[green]summary[/]    {summary}")
    for path in written:
        console.print(f"[dim]plot       {path}[/]")

    wrong = [c for c in cells if c.valid is False]
    if wrong:
        console.print(f"\n[red]{len(wrong)} answers failed validation[/] and are struck through")
    return 0
