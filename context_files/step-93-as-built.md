# Step 93 — as built

**Release hygiene for 0.4: the record corrected, and the benchmark's memory sampler
no longer stops the world.** This is `v0.4-scope.md` item 12, the first step of the
road to 0.4. Item 11, tagging v0.3.2, is a release, and is left to the maintainer.

## 1. The record

- **The v0.3.0 changelog's known limitations** listed the q7 regression and a budget
  that was only explicit. Both were fixed in v0.3.1, and each now says so. The
  release's own notes are otherwise unchanged; they describe v0.3.0.
- **Object stores** were "planned for 0.4" in the changelog and twice in the README.
  They are now deferred past 0.4 by decision (`v0.4-scope.md` §4), and all three
  places say so. The seam, `ScanParquetFrom` and `ScanCSVFrom`, is unchanged.
- **The h2o group-by labels used "cardinality" backwards.**
  - gb1 and gb4, which group into 100 groups, were "large-cardinality".
  - gb3 and gb5, which group into N/100 groups (100,000 at 10M rows), were
    "small-cardinality".

  The labels described the size of each group, and called it cardinality. Every
  label now gives its group count, read from the generator
  (`bench/driver/gen/h2o.py:93-95`, K = 100), in `bench/config/suites.toml` and in
  the tracked `REPORT.md`.
- **`micro-opbench.log`,** which the scope called stale, is ignored by
  `bench/.gitignore` and was never in the repository. Nothing to retire.

## 2. The sampler

The Go engines record peak live heap by sampling every 5 ms while a query runs
(`bench/engines/go/engine/engine.go`, `heapSampler`). It called
`runtime.ReadMemStats`, which **stops the world**, under the query it was timing.

Measured in a scratch program: eight goroutines allocating and holding 400 MB,
three rounds each, with no sampler, with `ReadMemStats`, and with
`runtime/metrics`.

| sampler | slowest single call | wall time |
| --- | --- | --- |
| `runtime.ReadMemStats` | 10–21 ms | within noise of none |
| `runtime/metrics.Read` | 16 µs – 2 ms | within noise of none |

- **The wall-time effect was not measurable at this size**, so no earlier number is
  claimed to have been wrong. But a 21 ms stop-the-world pause inside a timed query
  is the instrument perturbing what it measures, and it needs no flag to avoid.
- **The sampler now reads `/memory/classes/heap/objects:bytes` plus
  `/memory/classes/heap/unused:bytes`.** The same run checked that their sum equals
  `HeapInuse` to the byte (1,673,838,592 on both), so the reported column means what
  it did.
- **`Stop` still calls `ReadMemStats` once,** after the query, for `HeapSys`,
  `TotalAlloc` and `NumGC`.

## 3. Tests and teeth

**The bench engine module had no tests.**

**`TestHeapSamplerSeesALiveAllocation`** holds 64 MB across ten ticks and requires
the reported peak to include it. CI does not run the bench module's tests, so the
gate script now does:

```
cd bench/engines/go && go test ./engine/
```

| tooth | result |
| --- | --- |
| only the unused-heap metric is read | **bites:** the peak is 986,224 bytes, against 67,108,864 held |
| the peak is never raised | **bites:** the peak is 0 |

**Gate:** the root module's code is unchanged, so the full gate was not re-run.
`go vet ./...` and `go test ./engine/` pass in `bench/engines/go`.
