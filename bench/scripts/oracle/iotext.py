"""Helpers of the I/O groups (step 502, context_files/oracle-v2-scope.md §3): an input
text as a case's params, a frame or a text written to a temporary directory and read
back, and a dataset's file names as a text answer.

A case whose input is not a frame records it as a param: `csv`, `ndjson` or `json` the
text, or `csv_b64` where the bytes are not UTF-8 (oracle_test.go's in.bytes); a dataset
records its layout as the param `files`, each file's relative path to the name of the
input frame it holds, or to its text (in.texts), and the Go side writes the same
layout with its own writer. A query that returns a str is a written answer, compared
byte for byte (encode.py).

A reader's or a writer's keyword arguments are the param `options` (options below),
so the recording names what Polars was asked with, and the Go side passes the same
options (oracle_files_test.go's in.csvOptions and its kin), an argument it has no
spelling of failing the case. What a case asks of the frame it read is the params
`select`, `head` and `tail` (in.shaped).
"""

from __future__ import annotations

import base64
import os
import pathlib
import tempfile
from typing import Any, Callable

import polars as pl

from . import Case
from .encode import dtype_name


def text_params(name: str, data: bytes | str) -> dict[str, str]:
    """The input text as the param name, or, where its bytes are not UTF-8, as
    name_b64 in base64."""
    if isinstance(data, str):
        return {name: data}
    try:
        return {name: data.decode("utf-8")}
    except UnicodeDecodeError:
        return {name + "_b64": base64.b64encode(data).decode("ascii")}


def _is_dtype(v: Any) -> bool:
    return isinstance(v, pl.DataType) or (isinstance(v, type) and issubclass(v, pl.DataType))


def options(kw: dict[str, Any]) -> dict[str, Any]:
    """Keyword arguments as the param options: a type as ursus spells it, a schema
    (schema, hive_schema) as its [name, type] pairs in order, overrides by name, the
    rest as they are."""
    out: dict[str, Any] = {}
    for k, v in kw.items():
        if _is_dtype(v):
            out[k] = dtype_name(v)
        elif isinstance(v, dict) and k in ("schema", "hive_schema"):
            out[k] = [[n, dtype_name(t)] for n, t in v.items()]
        elif isinstance(v, dict):
            out[k] = {n: dtype_name(t) for n, t in v.items()}
        else:
            out[k] = v
    return out


def with_options(params: dict[str, Any], kw: dict[str, Any]) -> dict[str, Any]:
    """params, with the param options where there are keyword arguments."""
    return {**params, "options": options(kw)} if kw else params


def shaped(df: pl.DataFrame, select: str | None = None, head: int | None = None, tail: int | None = None) -> pl.DataFrame:
    """What a case asks of the frame it read: a column, then its first or last rows."""
    if select is not None:
        df = df.select(select)
    if head is not None:
        df = df.head(head)
    return df.tail(tail) if tail is not None else df


def shape_params(select: str | None = None, head: int | None = None, tail: int | None = None) -> dict[str, Any]:
    return {k: v for k, v in (("select", select), ("head", head), ("tail", tail)) if v is not None}


def csv_case(name: str, data: bytes | str, note: str = "", contains: str | list[str] = "",
             head: int | None = None, **kw: Any) -> Case:
    """read_csv of the text, under its options, the param options; head, if given, its
    first rows."""
    b = data.encode() if isinstance(data, str) else data
    return Case(name, {}, lambda: shaped(pl.read_csv(b, **kw), head=head),
                params=with_options({**text_params("csv", b), **shape_params(head=head)}, kw), note=note, contains=contains)


def listing(root: str | pathlib.Path) -> str:
    """Every file below root by its path from root, sorted, a line each: a dataset's
    layout as a text answer (partitionwrite_test.go's files)."""
    root = pathlib.Path(root)
    return "\n".join(sorted(str(p.relative_to(root)).replace(os.sep, "/") for p in root.rglob("*") if p.is_file()))


def in_dir(f: Callable[[pathlib.Path], Any]) -> Any:
    """f run over a fresh temporary directory, its answer collected before the
    directory goes."""
    with tempfile.TemporaryDirectory() as d:
        root = pathlib.Path(d)
        try:
            got = f(root)
            if isinstance(got, pl.LazyFrame):
                got = got.collect()
        except (pl.exceptions.PolarsError, TypeError, ValueError) as e:
            # A refusal that names the directory, another each run: the recording
            # holds <dir> for it, so that it is the same bytes every time.
            raise type(e)(str(e).replace(str(root), "<dir>")) from None
        return got


def write_files(root: pathlib.Path, files: dict[str, pl.DataFrame | str],
                write: Callable[[pl.DataFrame, pathlib.Path], None] | None = None) -> None:
    """Each file of a layout written below root: a frame by write (write_parquet by
    default), a str as its text."""
    for rel, content in files.items():
        p = root / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        if isinstance(content, str):
            p.write_text(content)
        elif write is not None:
            write(content, p)
        else:
            content.write_parquet(p)


def dataset(name: str, files: dict[str, pl.DataFrame], query: Callable[..., Any],
            write: Callable[[pl.DataFrame, pathlib.Path], None] | None = None,
            scan: dict[str, Any] | None = None, **opts: Any) -> Case:
    """A case over a directory of frames: its inputs are the frames, named f0, f1, ...
    by their file, and the param files maps each file's relative path to its frame's
    name; query takes the directory's root and the scan's keyword arguments, scan,
    which are the param options."""
    inputs = {f"f{i}": df for i, df in enumerate(files.values())}
    layout = {rel: f"f{i}" for i, rel in enumerate(files)}
    kw = scan or {}

    def q(**frames: pl.DataFrame) -> Any:
        def run(root: pathlib.Path) -> Any:
            write_files(root, {rel: frames[n] for rel, n in layout.items()}, write=write)
            return query(root, **kw)
        return in_dir(run)

    return Case(name, inputs, q, params=with_options({"files": layout}, kw), **opts)


def textfiles(name: str, files: dict[str, str], query: Callable[..., Any], scan: dict[str, Any] | None = None,
              **opts: Any) -> Case:
    """A case over a directory of text files, the param files each file's relative
    path to its text; query takes the directory's root and the scan's keyword
    arguments, scan, which are the param options."""
    kw = scan or {}

    def q() -> Any:
        def run(root: pathlib.Path) -> Any:
            write_files(root, files)
            return query(root, **kw)
        return in_dir(run)

    return Case(name, {}, q, params=with_options({"files": files}, kw), **opts)


def strip(root: pathlib.Path, col: str) -> pl.Expr:
    """A path column with the directory's root cut off, so a recording holds the
    file's relative path."""
    return pl.col(col).str.strip_prefix(str(root) + "/")
