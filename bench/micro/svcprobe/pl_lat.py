# Polars' twin of svcprobe's lat mode (bench/micro/svcprobe): the same four queries over the
# same frames, collected lazily, the median of each in microseconds.
import polars as pl, numpy as np, time
def frame(n):
    i = np.arange(n, dtype=np.uint64)
    r = i * np.uint64(0x9E3779B97F4A7C15)
    k = ((r >> np.uint64(40)) % np.uint64(100)).astype(np.int64)
    x = ((r >> np.uint64(20)) % np.uint64(10_000)).astype(np.float64) / 7
    s = ["s%d" % v for v in ((r >> np.uint64(50)) % np.uint64(1000)).tolist()]
    return pl.DataFrame({"k": k, "x": x, "s": s})
d = pl.DataFrame({"k": np.arange(100, dtype=np.int64), "name": ["name%d" % i for i in range(100)]})
qs = [("filter+select", lambda df: df.lazy().filter(pl.col("x") > 500.0).select("k", (pl.col("x")*2.0).alias("y"))),
      ("group-by sum, mean", lambda df: df.lazy().group_by("k").agg(pl.col("x").sum().alias("sum"), pl.col("x").mean().alias("mean"))),
      ("join 100-row dim", lambda df: df.lazy().join(d.lazy(), on="k")),
      ("sort, head 10", lambda df: df.lazy().sort("x").head(10))]
for n, iters in [(1000, 2000), (10000, 500), (100000, 100)]:
    df = frame(n)
    for name, q in qs:
        ts = []
        for _ in range(iters):
            t0 = time.perf_counter(); q(df).collect(); ts.append(time.perf_counter() - t0)
        ts.sort()
        print(f"polars {n:7d} rows  {name:20s} median {ts[len(ts)//2]*1e6:8.1f} µs", flush=True)
