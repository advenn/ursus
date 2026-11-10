"""The hive_infer group: the type a hive partition key's values infer
(internal/source/hive/hive_test.go's TestInfer, step 503). Each case writes one Parquet
file a value, at k=<value>/<row>.parquet below a directory, and scans the directory
with hive partitioning: the key column's type and values are the answer. The paths
are the case's params, so the Go test writes the same layout from the recording;
a slash in a value is percent-encoded on disk, as both readers decode it.

The Go test's one key of five date layouts is five keys here, one a layout, and the
five together a sixth, date_layouts_mixed: Polars 2.1 reads a key of dates by its
first value's layout and refuses the third, where ursus reads each value by its own,
as it reads CSV dates (feature-gaps §9 says so of CSV, not of a hive key): a finding
of step 503, recorded and left to the maintainer.
"""

from __future__ import annotations

import pathlib
import tempfile

import polars as pl

from . import Case, case
from .gen import Gen


def hive(name: str, vals: list[str], **opts) -> Case:
    paths = [f"k={v.replace('/', '%2F')}/{i}.parquet" for i, v in enumerate(vals)]
    df = pl.DataFrame({"i": pl.Series(list(range(len(vals))), dtype=pl.Int64)})

    def query(df: pl.DataFrame) -> pl.DataFrame:
        with tempfile.TemporaryDirectory() as root:
            for i, rel in enumerate(paths):
                p = pathlib.Path(root) / rel
                p.parent.mkdir(parents=True, exist_ok=True)
                df[i:i + 1].write_parquet(p)
            return pl.scan_parquet(root, hive_partitioning=True).collect().sort("i")

    return case(name, df, query, params={"paths": paths}, **opts)


def cases() -> list[Case]:
    g = Gen(5033)
    ints = [str(v) for v in g.ints(60, pl.Int64, lo=-5000, hi=5000, null_rate=0, edges=False).to_list()]
    dates = [d.isoformat() for d in g.dates(50, null_rate=0, edges=False).to_list()]
    floats = [repr(v) for v in g.floats(50, null_rate=0, edges=False, ties=False).to_list()] + ["1e+300", "-0.0", "inf"]
    return [
        hive("ints", ["1", "-2", "007", "-0", "00"]),
        hive("int128_past_int64", ["9223372036854775808", "1"]),
        hive("int128_min", ["-170141183460469231731687303715884105728"]),
        hive("floats", ["1.5", "2"]),
        hive("float_forms", ["2.5e3", "1e3", "inf", "-inf", "NaN", "1.", ".5", "-1.5E-3"]),
        hive("bools", ["true", "FALSE", "True"]),
        hive("date_iso", ["2024-01-02", "2024-01-03"]),
        hive("date_short", ["2024-1-2"]),
        hive("date_slashes", ["2024/01/02"]),
        hive("date_dots", ["2024.01.02"]),
        hive("date_dmy", ["02-01-2024"]),
        hive("date_layouts_mixed", ["2024-01-02", "2024-1-2", "2024/01/02", "2024.01.02", "02-01-2024"]),
        hive("datetimes", ["2024-01-02 03:04:05", "2024-01-02T03:04", "2024-01-02 3:04", "2024/1/2 03:04:05",
                           "2024-01-02T03:04:05.123456789"]),
        hive("datetimes_zoned", ["2024-01-02T03:04:05Z", "2024-01-02T03:04:05.5+0530", "2024-01-02 03:04:05+01:00"]),
        # Text, each of these.
        hive("text_plus", ["+5"]),
        hive("text_nan_lower", ["nan"]),
        hive("text_hex", ["0x10"]),
        hive("text_underscore", ["1_000"]),
        hive("text_comma", ["1,5"]),
        hive("text_yes", ["yes"]),
        hive("text_bare_exponent", ["1.5e"]),
        hive("text_hour_alone", ["2024-01-02T03"]),
        hive("text_month_13", ["2024-13-01"]),
        hive("text_feb_30", ["2024-02-30"]),
        hive("text_int_beside_x", ["1", "x"]),
        hive("text_date_beside_x", ["2024-01-02", "x"]),
        hive("text_bool_beside_1", ["true", "1"]),
        hive("text_zoned_beside_naive", ["2024-01-02T03:04:05Z", "2024-01-02 03:04:05"]),
        hive("int_like_a_date", ["20240102"]),
        hive("two_digit_year", ["24-01-02"]),
        hive("gen_ints", ints),
        hive("gen_dates", dates),
        hive("gen_floats", floats),
    ]
