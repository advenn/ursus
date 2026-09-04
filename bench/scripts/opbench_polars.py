"""polars twin of opbench/main.go — same data, same operations, no file IO."""

import statistics
import time

import numpy as np
import polars as pl

ROWS = 5_000_000
JOIN_ROWS = 100_000
ITERATIONS = 3


def build():
    i = np.arange(ROWS, dtype=np.int64)
    labels = np.array([f"label{n:03d}" for n in range(100)])
    left = pl.DataFrame(
        {
            "id": i,
            "key": (i * 2654435761) % JOIN_ROWS,
            "grp": (i * 7919) % 100,
            "qty": i % 101,
            "val": ((i * 2654435761) % 1000000).astype(np.float64) / 1000.0,
            "label": labels[i % 100],
        }
    )
    r = np.arange(JOIN_ROWS, dtype=np.int64)
    right = pl.DataFrame({"key": r, "payload": r.astype(np.float64) * 1.5})
    return left, right


def run(name, fn):
    fn()  # warm-up
    timings = []
    for _ in range(ITERATIONS):
        started = time.perf_counter()
        fn()
        timings.append((time.perf_counter() - started) * 1000)
    print(f"{name:<16} {statistics.median(timings):9.1f} ms")


def main():
    left, right = build()

    run("filter", lambda: left.lazy().filter(pl.col("qty") > 50).collect())
    run("project_arith", lambda: left.lazy()
        .select((pl.col("val") * pl.col("qty")).alias("total")).collect())
    run("groupby_100", lambda: left.lazy()
        .group_by("grp").agg(pl.sum("val").alias("s")).collect())
    run("groupby_100k", lambda: left.lazy()
        .group_by("key").agg(pl.sum("val").alias("s")).collect())
    run("groupby_string", lambda: left.lazy()
        .group_by("label").agg(pl.sum("val").alias("s")).collect())
    run("sort", lambda: left.lazy()
        .select("id", "val").sort("val", descending=True).collect())
    run("join", lambda: left.lazy()
        .select("key", "val").join(right.lazy(), on="key", how="inner").collect())


if __name__ == "__main__":
    main()
