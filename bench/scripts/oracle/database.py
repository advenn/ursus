"""The database group: read_database, a column's type its values', met as Polars meets
them (database_test.go, step 353), each Polars 2.1.0's answer.

Two connections answer the same question. Through SQLite, each case's values are
inserted into a table of one untyped column, so SQLite keeps each value's own storage
class; through rows, a DB-API connection of no database whose every query answers its
values as they are, a column x, as the Go side's fake database/sql driver does, a
Boolean, an instant and an integer past Int64 are asked too, which sqlite3 cannot give
Polars. Each is read with infer_schema_length=None, every row typing the column as
ursus reads every row (feature-gaps.md §9), and the read_database arguments are the
param options. The param values is the column: a bytes value as {"b64": ...}, an
instant as {"datetime": its ISO text}, naive or with its offset; the Go side feeds the
same values through its fake driver, an integer past Int64 as a uint64, a naive
instant as a time.Time in UTC, an offset's in a fixed zone of it.

Not here, with the reason: a Go driver's own types (uint64 alone, int32, float32, a
Valuer, a struct, text not UTF-8) and the types a driver names (INT4, NUMERIC, DATE,
TIME, ...), which no Python value is; an instant beside text, which Polars refuses and
ursus meets at text; a float in an Int64 override, which Polars truncates; and an
override of no column, which Polars ignores (all feature-gaps.md §9); and the
10,000-row column, a Go check of batches.
"""

from __future__ import annotations

import base64
import datetime as dt
import sqlite3
from typing import Any

import polars as pl

from . import Case
from .gen import Gen
from .iotext import with_options

CON = sqlite3.connect(":memory:")
EVERY_ROW = {"infer_schema_length": None}


def enc(v: Any) -> Any:
    if isinstance(v, bytes):
        return {"b64": base64.b64encode(v).decode("ascii")}
    if isinstance(v, dt.datetime):
        return {"datetime": v.isoformat()}
    return v


def db(name: str, values: list, note: str = "", contains: str = "", **kw) -> Case:
    """The values through SQLite."""
    table = "t_" + name
    kw = {**EVERY_ROW, **kw}

    def q() -> pl.DataFrame:
        CON.execute(f'DROP TABLE IF EXISTS "{table}"')
        CON.execute(f'CREATE TABLE "{table}"(x)')
        CON.executemany(f'INSERT INTO "{table}" VALUES (?)', [(v,) for v in values])
        CON.commit()
        return pl.read_database(f'SELECT x FROM "{table}" ORDER BY rowid', connection=CON, **kw)

    return Case(name, {}, q, params=with_options({"values": [enc(v) for v in values]}, kw), note=note, contains=contains)


class Rows:
    """A DB-API 2 connection whose every query answers rows of one column, x, its
    values as they are."""

    def __init__(self, values: list) -> None:
        self.values = values

    def cursor(self) -> "_Cursor":
        return _Cursor(self.values)

    def close(self) -> None:
        pass


class _Cursor:
    def __init__(self, values: list) -> None:
        self._rows = [(v,) for v in values]
        self.description = None

    def execute(self, query: str, *args: Any, **kw: Any) -> "_Cursor":
        self.description = [("x", None, None, None, None, None, None)]
        return self

    def fetchall(self) -> list:
        rows, self._rows = self._rows, []
        return rows

    def fetchmany(self, n: int) -> list:
        rows, self._rows = self._rows[:n], self._rows[n:]
        return rows

    def close(self) -> None:
        pass


def rows(name: str, values: list, note: str = "", contains: str = "") -> Case:
    """The values through rows, as they are."""
    return Case(name, {}, lambda: pl.read_database("SELECT x", connection=Rows(values), **EVERY_ROW),
                params=with_options({"values": [enc(v) for v in values]}, EVERY_ROW), note=note, contains=contains)


def cases() -> list[Case]:
    g = Gen(5033)
    ints = g.ints(100, null_rate=0).to_list()
    floats = g.floats(100, edges=False, null_rate=0).to_list()
    mixed = [x for pair in zip(ints, floats) for x in pair]
    mixed[3::7] = [None] * len(mixed[3::7])
    return [
        db("integers", [1, None]),
        db("floats", [1.5, None]),
        db("text", ["a", None]),
        db("bytes", [b"\x01\x02", None]),
        db("an_integer_and_a_float", [1, 2.5]),
        db("a_large_integer_and_a_float", [(1 << 53) + 1, 2.5]),
        db("an_integer_and_text", [1, "a"]),
        db("a_float_and_text", [2.5, "a"]),
        db("bytes_and_text", [b"\x01", "a"]),
        db("no_value", [None, None]),
        db("a_null_then_an_integer", [None, 1]),
        db("a_float_after_150_integers", list(range(150)) + [1.5]),
        Case("arguments", {}, lambda: pl.read_database("SELECT ? AS a0, ? AS a1", connection=CON, execute_options={"parameters": (5, "p")}),
             params={"args": [5, "p"]}),
        db("override_int32", [1], schema_overrides={"x": pl.Int32}),
        db("override_text_of_an_integer_and_text", [1, "a"], schema_overrides={"x": pl.String}),
        db("override_decimal_of_a_float", [1.25], schema_overrides={"x": pl.Decimal(10, 2)}),
        # --- refused, as Polars refuses them -----------------------------------------------
        db("bytes_and_an_integer_refused", [b"\x01", 1], contains="Binary"),
        db("bytes_text_and_an_integer_refused", [b"\x01", "a", 1], contains="Int64"),
        Case("a_name_twice_refused", {}, lambda: pl.read_database("SELECT 1 AS a, 2 AS a", connection=CON), contains="more than once"),
        db("override_past_int32_refused", [3_000_000_000], schema_overrides={"x": pl.Int32}, contains=""),
        # --- generated -------------------------------------------------------------------
        db("integers_gen", g.ints(200).to_list()),
        db("floats_gen", g.floats(196, edges=False).to_list() + [0.0, 1e300, 1e-300, 2.0 ** 53], note="no NaN, which SQLite stores as NULL"),
        db("text_gen", g.strings(200).to_list()),
        db("bytes_gen", g.binary(100).to_list()),
        db("integers_and_floats_gen", mixed),
        db("integers_text_and_nulls_gen", [None if i % 7 == 3 else (i if i < 150 else f"t{i}") for i in range(300)]),
        # --- through rows: what sqlite3 cannot give Polars ---------------------------------
        rows("booleans", [True, False]),
        rows("a_boolean_and_an_integer", [True, 2]),
        rows("a_boolean_and_text", [True, "a"]),
        rows("an_instant", [dt.datetime(2020, 1, 2, 3, 4, 5, 6)], note="a naive instant is a Datetime(us)"),
        rows("an_instant_with_an_offset", [dt.datetime(2020, 1, 1, tzinfo=dt.timezone(dt.timedelta(hours=5)))],
             note="an instant with an offset is its instant in UTC"),
        rows("an_int64_and_past_one", [1, 1 << 63]),
        rows("an_instant_and_a_boolean_refused", [dt.datetime(2020, 1, 2), True], contains="Bool"),
        rows("booleans_gen", g.bools(120).to_list()),
        rows("instants_gen", g.datetimes(120, "us", edges=False).to_list()),
        rows("instants_with_an_offset_gen", [None if v is None else v.replace(tzinfo=dt.timezone(dt.timedelta(hours=5, minutes=30)))
                                             for v in g.datetimes(120, "us", edges=False).to_list()]),
        rows("integers_past_int64_gen", [None if v is None else (1 << 63) + abs(v) % (1 << 63) for v in g.ints(80).to_list()] + [1],
             note="each past Int64 and within a uint64, as a Go driver gives it, and a 1"),
    ]
