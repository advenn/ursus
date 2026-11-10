"""The partitionwrite group: a dataset written by keys, each key's directory and file
named as Polars names them, its values escaped where Polars escapes them, a null the
default partition, an empty text k=; files of at most so many rows; CSV, IPC and NDJSON
datasets (partitionwrite_test.go, step 367), each Polars 2.1.0's layout.

The answer is the dataset's file names, a line each, or the dataset read back; the
params name the keys, whether they are kept, the rows a file holds at most, and the
format. Not here, with the reason: more keys than files held open, a List key, a key
twice and every column a key, which Polars 2.1 writes or panics on where ursus refuses
(feature-gaps.md §9 for the open files); a writer of one stream; a failed query's
temporary files; and an empty answer's directory, a filesystem's matter.
"""

from __future__ import annotations

import datetime as dt

import polars as pl

from . import Case
from .gen import Gen
from .iotext import in_dir, listing, strip

KEYS = pl.DataFrame({
    "k": ["a/b", "c=d", "e f", "g%h", "ü", ":x", "", None, "#?&+", "a/b"],
    "j": pl.Series([1, 1, 2, 2, 1, 1, 2, 2, 1, 2], dtype=pl.Int64),
    "v": pl.Series(range(10), dtype=pl.Int64),
})
TS = dt.datetime(2024, 1, 2, 3, 4, 5, 123456)
KINDS = pl.DataFrame({"ts": pl.Series([TS], dtype=pl.Datetime("us")), "f": [-0.0], "v": pl.Series([1], dtype=pl.Int64)}).select(
    pl.col("ts").dt.date().alias("d"), pl.col("ts").alias("naive"),
    pl.col("ts").dt.replace_time_zone("UTC").dt.convert_time_zone("Asia/Tokyo").alias("tz"),
    pl.col("ts").cast(pl.Datetime("ns")).alias("ns"), pl.col("f"), pl.lit(True).alias("b"),
    pl.lit(1.5).cast(pl.Decimal(5, 2)).alias("dec"), pl.col("ts").dt.time().alias("tm"), pl.col("v"))
SEVEN = pl.DataFrame({"k": pl.Series([1, 2, 1, 1, 1, 1, 2], dtype=pl.Int64), "v": pl.Series(range(7), dtype=pl.Int64)})
THREE = pl.DataFrame({"k": ["b", "a", "b"], "v": pl.Series([1, 2, 3], dtype=pl.Int64)})


def params(keys, include_key, max_rows, fmt) -> dict:
    out: dict = {"format": fmt}
    if keys is not None:
        out["keys"] = keys
    if include_key is not None:
        out["include_key"] = include_key
    if max_rows is not None:
        out["max_rows"] = max_rows
    return out


def sink(name: str, df: pl.DataFrame, keys: list[str] | None = None, include_key: bool | None = None, max_rows: int | None = None,
         fmt: str = "parquet", back=None, contains: str = "", note: str = "") -> Case:
    """The dataset's file names, or, with back, what back reads from its root."""

    def q(df: pl.DataFrame):
        def run(root):
            out = root / "out"
            getattr(df.lazy(), "sink_" + fmt)(pl.PartitionBy(str(out), key=keys, include_key=include_key, max_rows_per_file=max_rows))
            return back(out) if back else listing(out)
        return in_dir(run)

    return Case(name, {"df": df}, q, params=params(keys, include_key, max_rows, fmt), contains=contains, note=note)


def read_back(out):
    return pl.scan_parquet(str(out)).sort("v")


def with_paths(out):
    return pl.scan_parquet(str(out), include_file_paths="path").with_columns(strip(out, "path")).sort("v")


def cases() -> list[Case]:
    g = Gen(5032)
    n = 150
    ks = g.strings(n, alphabet="ab /=%:#?&+ü.", maxlen=4, edges=False, name="k").to_list()
    ks[0:3] = ["", None, "a/b"]  # not the text __HIVE_DEFAULT_PARTITION__, whose directory is the null's: two writers, one file
    gen = pl.DataFrame({"k": pl.Series(ks, dtype=pl.String), "j": g.ints(n, lo=0, hi=2, null_rate=0, edges=False, name="j"),
                        "v": pl.Series(range(n), dtype=pl.Int64)})
    return [
        sink("a_hive_dataset", KEYS, ["k", "j"]),
        sink("a_hive_dataset_read_back", KEYS, ["k", "j"], back=read_back),
        sink("a_files_columns", KEYS, ["k", "j"], back=lambda out: pl.read_parquet(out / "k=a%2Fb" / "j=2" / "00000000.parquet"),
             note="a file of its own holds its keys"),
        sink("a_files_columns_without_keys", KEYS, ["k", "j"], include_key=False,
             back=lambda out: pl.read_parquet(out / "k=a%2Fb" / "j=2" / "00000000.parquet")),
        sink("without_keys_read_back", KEYS, ["k", "j"], include_key=False, back=lambda out: read_back(out).select("k", "j", "v")),
        sink("a_keys_name_escaped", pl.DataFrame({"a b/c": pl.Series([1], dtype=pl.Int64), "v": pl.Series([2], dtype=pl.Int64)}), ["a b/c"],
             include_key=False, note="Polars writes the key's name as it is, a slash in it making a directory; ursus escapes it as a value (feature-gaps.md §9)"),
        *[sink("a_key_of_" + key, KINDS, [key]) for key in ["d", "naive", "tz", "ns", "f", "b", "dec"]],
        sink("a_key_of_tm", KINDS, ["tm"], note="Polars' path drops a time's fraction; ursus keeps it (feature-gaps.md §9)"),
        sink("files_of_at_most_two_rows", SEVEN, ["k"], max_rows=2),
        sink("files_of_at_most_two_rows_read_back", SEVEN, ["k"], max_rows=2, back=with_paths,
             note="a key's rows in their order across files, numbered on"),
        sink("files_of_at_most_three_rows_no_key", SEVEN, max_rows=3),
        sink("files_of_at_most_three_rows_no_key_read_back", SEVEN, max_rows=3, back=with_paths),
        sink("a_csv_dataset", THREE, ["k"], fmt="csv"),
        sink("an_ipc_dataset", THREE, ["k"], fmt="ipc"),
        sink("an_ndjson_dataset", THREE, ["k"], fmt="ndjson"),
        sink("an_unknown_key_refused", THREE, ["x"], contains=""),
        # --- generated -------------------------------------------------------------------
        sink("a_hive_dataset_gen", gen, ["k", "j"]),
        sink("a_hive_dataset_gen_read_back", gen, ["k", "j"], back=read_back),
    ]
