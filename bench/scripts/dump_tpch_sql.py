"""Regenerate engines/sql/pdsh/q*.sql from duckdb's tpch extension.

The extension reproduces the 22 queries from the TPC-H specification at the
validation substitution parameters. Dumping them to files once means the suite
does not need the extension at run time, and datafusion / chdb / duckdb-go all
read exactly the same query text duckdb does.

    make sql
"""

from __future__ import annotations

import pathlib
import sys

import duckdb

# The spec leaves one output column unnamed, so duckdb calls it `sum(l_quantity)`.
# Every engine's answer is validated against duckdb's by column name, and asking
# the dataframe ports to alias a column `sum(l_quantity)` would be absurd. The
# alias is purely cosmetic — it changes no rows and no types.
PATCHES = {
    18: [("    sum(l_quantity)\n", "    sum(l_quantity) AS col6\n")],
}

HEADER = """-- TPC-H query {nr}, at the validation substitution parameters.
-- Source: duckdb's `tpch` extension (tpch_queries()), which reproduces the
-- queries from the TPC-H specification verbatim. Regenerate with
--   make sql
-- Tables are referenced by bare name; each SQL engine registers views over
-- the normalised parquet (or csv) tree before running this.
"""


def main() -> int:
    out = pathlib.Path(__file__).resolve().parent.parent / "engines" / "sql" / "pdsh"
    out.mkdir(parents=True, exist_ok=True)

    con = duckdb.connect()
    con.execute("INSTALL tpch")
    con.execute("LOAD tpch")
    rows = con.execute("SELECT query_nr, query FROM tpch_queries() ORDER BY query_nr").fetchall()
    if len(rows) != 22:
        print(f"expected 22 queries, got {len(rows)}", file=sys.stderr)
        return 1

    for nr, query in rows:
        for old, new in PATCHES.get(nr, ()):
            if old not in query:
                print(f"q{nr}: patch target not found, skipping: {old!r}", file=sys.stderr)
                continue
            query = query.replace(old, new)
        (out / f"q{nr}.sql").write_text(HEADER.format(nr=nr) + query.strip() + "\n")
    print(f"wrote {len(rows)} files to {out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
