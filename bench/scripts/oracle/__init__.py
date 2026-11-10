"""The recorded oracle: Polars' answers to ursus's questions, as data.

Step 496 (context_files/oracle-v2-scope.md §2). A group is a module here that returns
a list of cases, each a name, its input frames, a Polars query and its options; the
runner asks Polars 2.1.0 each question and writes the inputs and the answer to
testdata/oracle/<group>.json, where oracle_test.go in the repository root asks ursus
the same question by the case's name and compares. From the repository root:

    python -m bench.scripts.oracle              # every group
    python -m bench.scripts.oracle numodds      # one group

A case's inputs are literal or generated (gen.py, from a seed); either way they are
recorded, so the JSON is the whole truth and the Go test reads data, not code.
"""

from __future__ import annotations

import pathlib
from dataclasses import dataclass, field
from typing import Any, Callable

import polars as pl

# The Polars version every answer is recorded under. The runner refuses another.
POLARS = "2.1.0"

# The repository root: bench/scripts/oracle/__init__.py is three directories down.
ROOT = pathlib.Path(__file__).resolve().parents[3]

# The groups, in the order the runner writes them.
GROUPS = ["expressions", "format", "numodds", "rollmore", "dtmore",
          # step 497, expressions and math
          "scalar_byhand", "castround_byhand", "castedges_byhand", "castmore", "exprodds", "exprargs", "fold", "variadic",
          "math", "trig", "bits", "decimal_arith_byhand", "int128exact",
          # step 499: strings, lists, structs and enums
          "listns", "arrns", "listeval", "listmore", "listset", "liststruct", "structfields", "withfields",
          "enum_byhand", "expr_bin", "expr_cat", "strmany", "strcodec", "strsplit", "strptime",
          # step 498, the temporal wave
          "calendar", "tzreplace", "components", "daterange", "csvdates", "strtemporal", "temporalagg", "weeks",
          "dtcombine", "dtexact", "timearith", "businessdays_range", "interpby",
          # step 503, the sub-packages' data values
          "dtype", "decimalagg", "hive_infer", "selector", "castbool", "caststring"]


@dataclass
class Case:
    """One question: the inputs by name, the query over them, and how to compare.

    query is called with the inputs as keyword arguments (``lambda df: ...``) and
    returns a DataFrame, a LazyFrame or a Series. order is "rows" or "set" (Polars'
    row order unspecified: the test sorts both sides). float is "bits" (the default;
    NaN equals NaN, -0 is not 0) or "rel:1e-9"; a tolerance says why in note.
    contains is, for a question Polars refuses, the text ursus's refusal must name.
    """

    name: str
    inputs: dict[str, pl.DataFrame]
    query: Callable[..., Any]
    params: dict[str, Any] = field(default_factory=dict)
    order: str = "rows"
    float: str = "bits"
    note: str = ""
    contains: str = ""


def case(name: str, inputs: pl.DataFrame | dict[str, pl.DataFrame], query: Callable[..., Any], **opts: Any) -> Case:
    """A Case whose one input is named df, or whose inputs are given by name."""
    if isinstance(inputs, pl.DataFrame):
        inputs = {"df": inputs}
    return Case(name, inputs, query, **opts)
