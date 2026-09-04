"""Shared runner for the SQL engines: duckdb, datafusion, chdb.

All three read the same query text from engines/sql/<suite>/<query>.sql, so the
only thing that differs between them is how a parquet or csv file is exposed
under a bare table name and how a result is pulled back as Arrow. That keeps the
comparison about execution rather than about who wrote the nicer SQL.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path
from typing import Any, Callable

sys.path.insert(0, str(Path(__file__).resolve().parent.parent.parent))

from engines.py._common import Args, run  # noqa: E402

SQL_ROOT = Path(__file__).resolve().parent.parent / "sql"


def read_sql(args: Args) -> str:
    path = SQL_ROOT / args.suite / f"{args.query}.sql"
    if not path.exists():
        raise NotImplementedError(f"no SQL for {args.suite}/{args.query}")
    return path.read_text()


def _scan_expr(args: Args, table: str) -> str:
    path = args.table_path(table).as_posix()
    if args.io == "csv":
        return f"read_csv_auto('{path}')"
    return f"read_parquet('{path}')"


# ------------------------------------------------------------------ duckdb --


def build_duckdb(args: Args) -> Callable[[], Any]:
    import duckdb

    sql = read_sql(args)

    def once() -> Any:
        con = duckdb.connect()
        if args.threads:
            con.execute(f"SET threads TO {args.threads}")
        con.execute("SET preserve_insertion_order TO false")
        for table in args.inputs():
            con.execute(f"CREATE OR REPLACE VIEW {table} AS SELECT * FROM {_scan_expr(args, table)}")
        result = con.execute(sql).fetch_arrow_table()
        con.close()
        return result

    return once


# -------------------------------------------------------------- datafusion --


def build_datafusion(args: Args) -> Callable[[], Any]:
    try:
        from datafusion import SessionContext
    except ImportError as exc:  # pragma: no cover - depends on optional group
        raise NotImplementedError(f"datafusion not installed: {exc}") from exc

    sql = read_sql(args)

    def once() -> Any:
        ctx = SessionContext()
        for table in args.inputs():
            path = str(args.table_path(table))
            if args.io == "csv":
                ctx.register_csv(table, path)
            else:
                ctx.register_parquet(table, path)
        return ctx.sql(sql).to_arrow_table()

    return once


# -------------------------------------------------------------------- chdb --


def build_chdb(args: Args) -> Callable[[], Any]:
    try:
        import chdb
    except ImportError as exc:  # pragma: no cover - depends on optional group
        raise NotImplementedError(f"chdb not installed: {exc}") from exc

    import io

    import pyarrow as pa

    from chdb import session as chdb_session

    sql = _clickhouse_dialect(read_sql(args))
    fmt = "CSVWithNames" if args.io == "csv" else "Parquet"

    # A stateful session, so the inputs can be registered as views under their
    # bare names exactly as duckdb and datafusion get them. Substituting the
    # table names into the query text instead would be wrong: TPC-H q9 aliases a
    # column `AS nation`, and a textual replacement cannot tell that apart from
    # the table of the same name.
    def once() -> Any:
        with chdb_session.Session() as session:
            for table in args.inputs():
                path = args.table_path(table).as_posix()
                session.query(
                    f"CREATE OR REPLACE VIEW {table} AS "
                    f"SELECT * FROM file('{path}', {fmt})"
                )
            raw = session.query(sql, "Arrow")
            payload = raw.bytes()
        buffer = io.BytesIO(payload)
        # ClickHouse's Arrow output is the random-access file format ("ARROW1"
        # magic), not the streaming one. Older builds emit a stream, so try the
        # file reader first and fall back rather than pinning a chdb version.
        try:
            return pa.ipc.open_file(buffer).read_all()
        except pa.ArrowInvalid:
            buffer.seek(0)
            return pa.ipc.open_stream(buffer).read_all()

    return once


# ClickHouse settings needed to run standard SQL. Neither changes what a query
# means:
#   joined_subquery_requires_alias  ClickHouse insists every joined subquery and
#       table function be aliased; the TPC-H text does not alias them, and its
#       own error message names this setting as the fix.
#   enable_analyzer  the newer query analyzer, which is what handles correlated
#       subqueries (q17, q20, q21, q22) at all.
_CLICKHOUSE_SETTINGS = "joined_subquery_requires_alias = 0, enable_analyzer = 1"

# Dialect rewrites, applied to the shared SQL before chdb sees it. Kept to
# things that are pure syntax: ClickHouse has no `extract(field FROM value)`,
# only the per-field functions.
_CLICKHOUSE_REWRITES = [
    (re.compile(r"\bextract\s*\(\s*year\s+from\s+([^)]+)\)", re.IGNORECASE), r"toYear(\1)"),
    (re.compile(r"\bextract\s*\(\s*month\s+from\s+([^)]+)\)", re.IGNORECASE), r"toMonth(\1)"),
    (re.compile(r"\bsubstring\s*\(\s*(\w+)\s+FROM\s+(\d+)\s+FOR\s+(\d+)\s*\)", re.IGNORECASE),
     r"substring(\1, \2, \3)"),
]


def _clickhouse_dialect(sql: str) -> str:
    """Make standard TPC-H SQL runnable on ClickHouse.

    chdb is the one engine here that does not accept the shared query text
    as-is. Rather than fork all 22 queries into a ClickHouse variant, the
    differences are applied mechanically and listed above, so it stays obvious
    that chdb is running the same query as everyone else.
    """
    for pattern, replacement in _CLICKHOUSE_REWRITES:
        sql = pattern.sub(replacement, sql)
    body = re.sub(r";\s*$", "", sql.strip())
    return f"{body}\nSETTINGS {_CLICKHOUSE_SETTINGS}"


BUILDERS = {
    "duckdb": build_duckdb,
    "datafusion": build_datafusion,
    "chdb": build_chdb,
}


def main(engine: str) -> int:
    return run(engine, BUILDERS[engine])
