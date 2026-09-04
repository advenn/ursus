"""pandas entry point.

pandas has no lazy layer, so `read` is part of the query and the whole table is
resident before any filter runs. That is the honest shape of the library and the
reason its ceilings in config/engines.toml are low: at SF10 the TPC-H tables do
not fit on a 16 GB machine no matter how the query is written.

Only the columns a query touches are read, because that is what a competent
pandas user would do and every other engine gets projection pushdown for free.
"""

from __future__ import annotations

import sys
from typing import Any, Callable

from engines.py._common import Args, run
from engines.py.queries import pandas_h2o, pandas_pdsh


def build(args: Args) -> Callable[[], Any]:
    import pandas as pd

    module = pandas_pdsh if args.suite == "pdsh" else pandas_h2o
    query = module.QUERIES.get(args.query)
    if query is None:
        raise NotImplementedError(f"pandas: {args.suite}/{args.query} not ported")

    columns = getattr(module, "COLUMNS", {}).get(args.query, {})
    dates = getattr(module, "DATE_COLUMNS", {})

    import pyarrow.parquet as pq

    def read(table: str) -> "pd.DataFrame":
        path = args.table_path(table)
        wanted = columns.get(table)
        if args.io == "csv":
            return pd.read_csv(
                path,
                usecols=wanted,
                parse_dates=[c for c in dates.get(table, ()) if wanted is None or c in wanted],
            )
        # Not pd.read_parquet: it defaults to date_as_object=True, which turns
        # every date32 column into an object column of datetime.date, and those
        # do not compare against a Timestamp. datetime64 is both what a pandas
        # user would want and what the CSV path already produces.
        return pq.read_table(path, columns=wanted).to_pandas(date_as_object=False)

    def once() -> Any:
        return query(read)

    return once


if __name__ == "__main__":
    sys.exit(run("pandas", build))
