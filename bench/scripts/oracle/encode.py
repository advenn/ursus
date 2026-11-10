"""A Polars frame to the oracle's JSON, and a case to its record.

The format (context_files/oracle-v2-scope.md §2): a frame is its columns in order,
each a name, its type spelled as ursus prints it (dtype.DataType.String()) and its
values as JSON. A null is null. An integer is a JSON integer; an Int128 and a
Decimal's unscaled integer are strings, past what a JSON reader keeps exactly. A
float is a JSON number when finite and not -0, else "NaN", "inf", "-inf" or "-0.0";
Python's repr round-trips every double, and a Float32 is written as the double it
equals. A Date is its days, a Datetime, Duration or Time its ticks in the unit its
type names. Binary is base64. A Categorical or Enum value is its text. A List or
Array is a JSON list, a Struct a JSON object keyed by field name.
"""

from __future__ import annotations

import base64
import json
import math
from typing import Any

import polars as pl

from . import Case


def quote_name(s: str) -> str:
    """dtype.QuoteName: a field name or category quoted only where the rendering
    would be ambiguous."""
    if s == "" or s.strip() != s or any(ch in s for ch in ',:()"\\!') or not s.isprintable():
        return json.dumps(s)
    return s


def dtype_name(dt: pl.DataType) -> str:
    """ursus's spelling of a Polars type."""
    if isinstance(dt, pl.Datetime):
        if dt.time_zone:
            return f"Datetime({dt.time_unit}, {dt.time_zone})"
        return f"Datetime({dt.time_unit})"
    if isinstance(dt, pl.Duration):
        return f"Duration({dt.time_unit})"
    if isinstance(dt, pl.Decimal):
        return f"Decimal({dt.precision or 38}, {dt.scale})"
    if isinstance(dt, pl.Enum):
        return "Enum(" + ", ".join(quote_name(c) for c in dt.categories.to_list()) + ")"
    if isinstance(dt, pl.List):
        return f"List({dtype_name(dt.inner)})"
    if isinstance(dt, pl.Array):
        inner = dt.inner if len(dt.shape) == 1 else pl.Array(dt.inner, dt.shape[1:])
        return f"Array({dtype_name(inner)}, {dt.shape[0]})"
    if isinstance(dt, pl.Struct):
        return "Struct(" + ", ".join(f"{quote_name(f.name)}: {dtype_name(f.dtype)}" for f in dt.fields) + ")"
    if dt == pl.Boolean:
        return "Bool"
    if dt == pl.String:
        return "String"
    if dt == pl.Time:
        return "Time(ns)"
    if dt == pl.Categorical:
        return "Categorical"
    if dt in (pl.Binary, pl.Date, pl.Null) or dt.is_integer() or dt.is_float():
        return str(dt).replace("UInt", "Uint")  # ursus spells Uint8, as its TypeID does
    raise TypeError(f"no oracle spelling for {dt!r}")


def _textual(dt: pl.DataType) -> pl.DataType:
    """dt with every Categorical and Enum a String, so to_list gives the text and
    not a code."""
    if dt == pl.Categorical or isinstance(dt, pl.Enum):
        return pl.String
    if isinstance(dt, pl.List):
        return pl.List(_textual(dt.inner))
    if isinstance(dt, pl.Array):
        return pl.Array(_textual(dt.inner), dt.shape)
    if isinstance(dt, pl.Struct):
        return pl.Struct({f.name: _textual(f.dtype) for f in dt.fields})
    return dt


def encode_value(v: Any, dt: pl.DataType) -> Any:
    """One physical value (a tick count, an unscaled integer, bytes, a list, a
    dict) in its JSON form, guided by the logical type."""
    if v is None:
        return None
    if dt.is_float():
        if math.isnan(v):
            return "NaN"
        if math.isinf(v):
            return "inf" if v > 0 else "-inf"
        if v == 0 and math.copysign(1.0, v) < 0:
            return "-0.0"
        return float(v)
    if isinstance(dt, pl.Decimal) or dt == pl.Int128:
        return str(v)
    if dt == pl.Binary:
        return base64.b64encode(v).decode("ascii")
    if isinstance(dt, pl.List):
        return [encode_value(e, dt.inner) for e in v]
    if isinstance(dt, pl.Array):
        inner = dt.inner if len(dt.shape) == 1 else pl.Array(dt.inner, dt.shape[1:])
        return [encode_value(e, inner) for e in v]
    if isinstance(dt, pl.Struct):
        return {f.name: encode_value(v[f.name], f.dtype) for f in dt.fields}
    return v


def encode_series(s: pl.Series) -> list:
    """A column's values. to_physical gives a temporal as its ticks and a Decimal as
    its unscaled integer, into lists and structs too; a Float32's double is itself."""
    dt = s.dtype
    phys = s.cast(_textual(dt)).to_physical()
    return [encode_value(v, dt) for v in phys.to_list()]


def encode_frame(df: pl.DataFrame) -> dict:
    return {"columns": [{"name": c, "dtype": dtype_name(df[c].dtype), "values": encode_series(df[c])} for c in df.columns]}


def record(c: Case) -> dict:
    """A case's record: its inputs, options and Polars' answer, or Polars' refusal."""
    out: dict[str, Any] = {
        "name": c.name,
        "inputs": {k: encode_frame(v) for k, v in c.inputs.items()},
        "order": c.order,
        "float": c.float,
    }
    if c.params:
        out["params"] = c.params
    if c.note:
        out["note"] = c.note
    try:
        got = c.query(**c.inputs)
        if isinstance(got, (str, bytes)):
            # A written answer (step 502): write_csv's or write_ndjson's text, or a
            # dataset's file names, compared byte for byte.
            out["want"] = {"text": got if isinstance(got, str) else got.decode("utf-8")}
            return out
        if isinstance(got, pl.LazyFrame):
            got = got.collect()
        elif isinstance(got, pl.Series):
            got = got.to_frame()
        if c.order == "set":
            # Polars' row order is its hash table's, another each run; the test
            # compares the rows as a set, so the recording sorts them to be stable.
            got = got.sort(got.columns, nulls_last=True)
        out["want"] = encode_frame(got)
    except (pl.exceptions.PolarsError, TypeError, ValueError) as e:
        first = str(e).strip().splitlines()[0] if str(e).strip() else ""
        # contains is a phrase, or (step 502) a list of them, each of which ursus's
        # refusal names: its reason and its place.
        out["want"] = {"error": {"polars": f"{type(e).__name__}: {first}", "contains": c.contains}}
    return out
