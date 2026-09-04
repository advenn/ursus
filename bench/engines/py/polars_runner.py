"""polars entry point.

The lazy API throughout: scan_* + collect(), which is the shape ursus was
modelled on and therefore the fairest comparison available. Building the
LazyFrame happens inside the timed region, same as every other engine, because
for a lazy engine that step reads file metadata and is part of the query.
"""

from __future__ import annotations

import sys
from typing import Any, Callable

from engines.py._common import Args, run
from engines.py.queries import polars_h2o, polars_pdsh


def build(args: Args) -> Callable[[], Any]:
    import polars as pl

    # Thread count comes from POLARS_MAX_THREADS, which polars reads once at
    # import time — the driver sets it in the child's environment.
    module = polars_pdsh if args.suite == "pdsh" else polars_h2o
    query = module.QUERIES.get(args.query)
    if query is None:
        raise NotImplementedError(f"polars: {args.suite}/{args.query} not ported")

    def scan(table: str) -> "pl.LazyFrame":
        path = args.table_path(table)
        if args.io == "csv":
            # The TPC-H date columns are plain YYYY-MM-DD; without this they
            # would arrive as strings and the date predicates would not push
            # down, which would flatter polars for the wrong reason.
            return pl.scan_csv(path, try_parse_dates=True)
        return pl.scan_parquet(path)

    def once() -> Any:
        return query(scan).collect()

    return once


if __name__ == "__main__":
    sys.exit(run("polars", build))
