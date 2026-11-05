"""python -m bench.scripts.oracle [group ...]: record each group's answers.

Run from the repository root. Without arguments every group is written; the output
is deterministic (keys sorted where their order means nothing, indent 1, a trailing
newline), so CI regenerates it and `git diff --exit-code -- testdata/oracle` proves
every recorded answer is Polars 2.1.0's.
"""

from __future__ import annotations

import importlib
import json
import sys

import polars as pl

from . import GROUPS, POLARS, ROOT
from .encode import record


def main(argv: list[str]) -> int:
    if pl.__version__ != POLARS:
        print(f"polars {pl.__version__} is installed; the oracle is recorded under {POLARS} "
              f"(pip install -r bench/scripts/oracle/requirements.txt)", file=sys.stderr)
        return 2
    groups = argv or GROUPS
    for g in groups:
        if g not in GROUPS:
            print(f"no group {g!r}; the groups are {', '.join(GROUPS)}", file=sys.stderr)
            return 2
    out_dir = ROOT / "testdata" / "oracle"
    out_dir.mkdir(parents=True, exist_ok=True)
    for g in groups:
        mod = importlib.import_module(f".{g}", __package__)
        cases = mod.cases()
        names = [c.name for c in cases]
        if len(set(names)) != len(names):
            dup = sorted({n for n in names if names.count(n) > 1})
            print(f"{g}: the names {dup} are used twice", file=sys.stderr)
            return 2
        doc = {
            "polars": pl.__version__,
            "generator": f"bench/scripts/oracle/{g}.py",
            "cases": [record(c) for c in cases],
        }
        path = out_dir / f"{g}.json"
        path.write_text(json.dumps(doc, indent=1, sort_keys=True, allow_nan=False) + "\n")
        refused = sum(1 for c in doc["cases"] if "error" in c["want"])
        print(f"wrote {len(cases)} cases ({refused} refusals) to {path.relative_to(ROOT)}")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
