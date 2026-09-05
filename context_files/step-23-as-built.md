# Step 23 — as built

`map[string]int32` replaced by `kernel.KeyTable` in the six operators that
identify groups. **Every affected benchmark got faster and the group-by allocates
17.7x less**, but the headline finding of this step is about MEASUREMENT: the
first before/after comparison said the change was a 1.2–1.4x regression, and it
was wrong.

Authoritative where it disagrees with [`step-22-as-built.md`](./step-22-as-built.md),
the vision docs and [`design/`](./design/).

**1411 test cases green** — 495 top-level and 916 subtests (1404 before this step)
— under `GOEXPERIMENT=simd` × `GODEBUG=simd={512,256,128,0}`, with the experiment
off, and under `-race`. `make levels` and `go vet` clean, in the root module and
in `bench/micro`. 200 files, ~60.7k lines excluding `bench/`.

```
matched 8-sample runs, mean ns/op          before      after

  GroupByHighCardinality                  113.4 ms    96.3 ms    1.18x
  GroupByStringKey                        205.9 ms   177.2 ms    1.16x
  GroupByLowCardinality                   113.8 ms   103.7 ms    1.10x
  JoinInner                               293.4 ms   271.8 ms    1.08x
  WindowMean                              186.5 ms   178.3 ms    1.05x
  WindowRank                              410.4 ms   393.3 ms    1.04x

allocations per op
  GroupByStringKey                         327,957     18,485    17.7x
  GroupByHighCardinality                   148,386     17,398     8.5x
  JoinInner                                 67,209     50,745     1.3x
```

---

## 1. The measurement was nearly the wrong way round

The first comparison was a 3-sample baseline against a 3-sample after-run. It
reported everything 1.17x to 1.44x **slower**, and it was consistent enough
across six benchmarks to look real.

It was machine drift. The same unchanged code, measured twice:

```
GroupByStringKey, before-state, 3 samples:  202.7  161.2  173.7   median 173.7
GroupByStringKey, before-state, 8 samples:  201.8 207.5 201.0 213.3
                                            205.4 204.0 207.4 206.7   median 205.4
```

**18% apart, on identical code.** The 3-sample run happened while the laptop was
cool; the after-run did not. Six benchmarks all pointed the same way because they
all ran later, not because the change was slow.

What settled it was matched sampling: `git stash -u` to get a genuinely pristine
tree, eight samples of each state, compare means. Under that, every benchmark
improves.

Two things made the false result survivable. The profile disagreed with the
wall-clock from the start — `hashAggSink.Consume` went from 1.41s cumulative to
1.08s, and `GetOrInsert` at 0.73s was already beating the map's
`mapaccess2_faststr` + `mapassign_faststr` at 1.04s. And the allocation counts,
which are deterministic and cannot drift, had dropped 17.7x.

**When wall-clock and a profile disagree on this machine, the profile is right.**
A median of three proves nothing here; step 18 recorded a contaminated baseline
and this step recorded a merely unlucky one.

---

## 2. What the table actually is

`internal/kernel/keytable.go`, beside the `groupkey.go` encoder that feeds it.
Open addressing, linear probing, power-of-two slots, growth at 3/4 load. Four
slices: `slots` (ids by slot, -1 empty), and `hashes`/`offs`/`arena` indexed by
id.

The profile that justified it, on `BenchmarkGroupByStringKey`, inside
`hashAggSink.Consume`:

| | before | after |
| --- | --: | --: |
| the table | `mapaccess2` 0.86s + `mapassign` 0.18s | `GetOrInsert` 0.73s |
| `GroupKeyEncoder.Encode` | 0.09s | 0.16s |
| `sumAcc.AddBatch` | 0.07s | 0.07s |
| **Consume, cumulative** | **1.41s** | **1.08s** |

Three differences, in the order they mattered:

1. **One hash and one probe for get-or-insert.** Go has no fused form, so
   `ids[string(k)]` followed by `ids[string(k)] = id` hashes and probes twice on
   every new key. The probe that missed here already knows the slot to fill.
2. **The hash is stored per id**, so a probe rejects on eight bytes before
   touching the key.
3. **Keys in one arena**, not a separately allocated string each. This is the
   17.7x: inserting a key no longer allocates.

`probeHash` reads eight bytes at a time and is **deliberately not `HashKey`**.
`HashKey` is FNV-1a byte-at-a-time — one multiply per byte — against the Go map's
hardware AES, and reusing it would have handed back the win. The two answer
different questions and the code now says so in both places: `HashKey` decides
which **spill file** a key is routed to and must be stable across processes;
`probeHash` decides only a **probe slot**, and since ids come from first-seen
order and `KeyAt` reads back by id, nothing outside the file can observe it.

In the event `probeHash` is 0.07s of the profile — not the bottleneck either way.

### `KeyAt` deleted a materialisation

Both `Merge` implementations inverted the map to walk keys in id order:

```go
oKeys := make([]string, len(o.ids))
for k, oid := range o.ids { oKeys[oid] = k }
```

A whole `[]string` plus a map walk, purely because a Go map cannot be indexed by
value. `KeyAt(id)` is `arena[offs[id]:offs[id+1]]`.

---

## 3. Three things found while converting

**`joinBuildSink.Merge` was nondeterministic.** It did `for k, oid := range o.ids`
— ranging a Go map — so the ids this sink assigned to the other's new keys varied
run to run, and so did *which* duplicate key `duplicateKeyErr` happened to name
when Validate is on. `hashAggSink.Merge` had a careful comment explaining why it
must visit in id order; the join simply did not. Both are id-ordered now.

**`releaseTable` freed the accounting, not the memory.** Its doc says the resident
table and its keys "are dead the moment the resident flush ends", and it zeroed
`keyBytes`. But the sink and the table shared one `ids` map, so dropping
`p.t` left every key reachable from `p.sink.ids` — the ledger said released and
the heap disagreed. It now calls `Reset()`, which is safe because `Consume` is
over before any partition replays.

**`hashAggSink` never accounted its group table at all** — not before this step
and not now. `join.go` charged `idsBytesPerKey` per key; the aggregate charged
only its materialised key columns and accumulators. `KeyTable.NBytes()` makes it
one line to fix, but adding it changes when a limited query spills, which is a
behaviour change that wants its own step and its own spill tests. Recorded, not
smuggled in.

`idsBytesPerKey` itself is gone, renamed `seenBytesPerKey = 44` and retargeted at
the probe side's `map[string]struct{}`, which is the only estimate left. The
build side is exact now.

---

## 4. Teeth, including one that did not fire

**Ids by slot index instead of insertion order.** The differential fails on the
first row: `id = 56, map says 0`. Everything downstream indexes by id —
accumulator storage, `firstSeen`, both `Merge` remaps — so this is the single
most dangerous thing to get wrong and the least visible.

**`KeyAt` off by one.** Four of the seven unit tests fail, and so does
`internal/physical` — the operator suite reaches it through the `Merge` paths.

**A length-blind key comparison — which did NOT fail, and that is the finding.**
Replacing `bytes.Equal` with `bytes.HasPrefix` leaves the entire suite green,
because `probeHash` mixes in the length: `"ab"` and `"ab\x00"` get different
hashes and are separated before the comparison is reached at all. The hash
comparison is what actually does the work; `bytes.Equal` is the backstop against a
full 64-bit collision, which no feasible fixture can produce.

So the comparison is load-bearing — merging two groups silently is exactly the
failure it prevents — and genuinely untestable without injecting a weak hash into
the hot path. The code says so rather than implying the test covers it. That is
the third teeth check in three steps to report something other than what it was
aimed at.

---

## 5. New tests

`internal/kernel/keytable_test.go`, seven cases. The central one is a
**differential against `map[string]int32`** over 20,000 lookups from a
200-key corpus — same argument `radix_test.go` makes against the comparator sort:
identical ids in identical order, not merely a consistent grouping. The corpus
includes empty keys and `0x00` bytes, because the group-key encoder writes `0x00`
as its null tag and keys are therefore not NUL-safe.

The rest pin growth across many doublings, `KeyAt` round-trips, prefix keys,
forced slot collisions, `Get` not inserting (the frozen join table is read
without a lock), and `Reset` actually releasing.

---

## 6. One stale comment

`bench/micro/micro_test.go`, above `BenchmarkGroupByThreads`: *"Parallel
aggregation is implemented but not wired up, so this is the ceiling until it
is."* Step 17 wired it up six steps ago — `aggBreaker` calls `newParallelSink`,
and `aggWorkers` returns `opts.Threads` when its two gates pass, which this query
does not trip. The benchmark was describing its own subject wrongly.

---

## 7. What is still open

- **The window sink is single-threaded.** `windowSink.Merge` is refused, so h2o
  gb8 runs on one core against polars' eight: 27,973ms vs 1,371ms, **20.4x, the
  worst ratio in the suite** and near-worst in absolute time. The largest
  addressable structural gap left. Merge needs a group-id remap, a per-row id
  remap and batch ordering — the code calls it "strictly harder" than the
  aggregate case step 17 solved.
- **The arrow-go Parquet floor, measured.** PDS-H q6 is the scan-bound query and
  the report carries arrow-go as the floor: duckdb 32ms, polars 7ms, **arrow-go
  56ms**, ursus 100ms. About 8x of the scan gap is arrow-go's reader, which ursus
  is built on and does not control; the remaining 1.8x is ursus's plan, evaluator
  and pipeline. No operator work recovers the first part.
- **String kernels allocate per value** — 2.1M in `StringSliceUpper`. The
  allocations are inside `applyStr` (`strings.ToUpper`, `[]rune(s)`,
  `string(r[start:end])`), so this needs append-into-buffer string functions, not
  the `NewStringParts` swap step 22 used.
- **`hashAggSink` does not account its group table** (§3).
- **`nuniqueAcc`'s `sets []map[string]struct{}`** — a set per group, the same
  family as this step but a different shape.
- **`reverseSink.Merge` has no test**; `argExtremumAcc` and `positionAcc` still
  hold a `*data.Column` per group.
- **CSE**, **`JoinWhere`**, **nested types**, string sort keys.
