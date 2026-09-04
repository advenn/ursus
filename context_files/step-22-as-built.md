# Step 22 — as built

The CSV reader's per-value string allocation, removed. **3,177,463 allocations
per scan become 25,310** — 126x fewer — with memory halved and the scan roughly
twice as fast.

Authoritative where it disagrees with [`step-20-as-built.md`](./step-20-as-built.md),
the vision docs and [`design/`](./design/).

**1404 test cases green** — 488 top-level and 916 subtests (1403 before this step)
— under `GOEXPERIMENT=simd` × `GODEBUG=simd={512,256,128,0}`, with the experiment
off, and under `-race`. `make levels` and `go vet` clean. 198 files, ~60.1k lines
excluding `bench/`.

```
BenchmarkScanCSV              1,048,576 rows, 7 columns, 3 of them String

                    before        after
  allocs/op      3,177,463       25,310      126x
  B/op         477,565,856  241,155,208      1.99x
  ns/op        ~1,298 ms      ~664 ms        1.95x

BenchmarkScanCSVTypedSchema (inference skipped)
  allocs/op      3,176,040       23,885      133x
```

The remaining 25,310 is 0.024 allocations per row — batch-level bookkeeping, not
per-value work. It is now *below* `ScanParquet`'s 41,766.

---

## 1. The defect, and why it was the same one twice

`stringBuilder.appendField` was one line:

```go
b.vals = append(b.vals, string(f)) // must copy: f aliases the scanner buffer
```

`string(f)` heap-allocates per value. Then `finish` handed the `[]string` to
`data.NewString`, which walked it and copied **every byte a second time** into an
offsets buffer and a character buffer. Two full copies of the file's string data
and one allocation per value, to produce a layout the builder could have written
directly.

This is exactly the defect step 18 removed from the Parquet reader's
`byteArrayCol`, and `data.NewStringParts` was added *in that step* to fix it. The
constructor's own doc names the Parquet case. The CSV reader has three string
columns in the benchmark fixture and was never revisited.

The fix is the same shape: accumulate `offs []int32` and `chars []byte`, and hand
them to `NewStringParts`.

### The leading zero, seeded once

Arrow's offsets have n+1 entries starting at zero. `byteArrayCol` seeds that zero
lazily (`if c.offs == nil`), because it only learns the row count inside `read`.
The CSV builder has no such constraint, so the zero is planted at construction:

```go
case dtype.TypeString:
    return &stringBuilder{offs: make([]int32, 1), valid: bitmap.NewBuilder(0)}, nil
```

and `finish` preserves it by truncating to `[:1]` rather than `[:0]`. The hot path
never tests for it, and `len()` is a plain `len(b.offs) - 1` with no guard.

### Reusing both buffers across batches

`fixedBuilder.finish` drops its slice (`b.vals = nil`) and says why: `NewFixed`
wraps without copying, so reuse would rewrite a batch the consumer still holds.

`stringBuilder` can do the opposite, because **`NewStringParts` copies** into
fresh arrow buffers (`column.go:204-207`). So `finish` keeps both slices with
their capacity intact:

```go
b.offs = b.offs[:1]
b.chars = b.chars[:0]
```

A multi-batch scan therefore grows its character buffer once instead of once per
batch. The two builders now sit next to each other doing opposite things for
opposite reasons, and both say which constructor's copying behaviour they depend
on — that is the fact to check first if either is ever changed.

### One field deleted

`stringBuilder.dt` was dead. It was assigned in `newBuilder` and read nowhere;
`finish` called `NewString`, which hardcodes `dtype.String`. Behaviour is
unchanged — `NewStringParts` hardcodes it too. Only `fixedBuilder` ever needed a
`dt`, because it is generic over the physical type and temporal columns are
`int32`/`int64` underneath.

---

## 2. The "remaining ~1M allocations" did not exist

The plan predicted this fix would remove ~2.1M of the 3.18M and left the other
~1M to be found by profiling, on the theory that it was `dtype.ParseTemporal`
probing `datetimeLayouts` once per row.

That premise was wrong, and reading the benchmark rather than the reader is what
showed it. `micro_test.go:93` declares the fixture's `ts` column as a **String**,
with a comment explaining why:

> including `ts` as a String, because ursus's CSV inference does not recognise
> dates. Declaring it as a Datetime instead would add timestamp parsing to the
> measurement and turn the comparison into something else.

So the fixture has **three** string columns, not two: 3 × 1,048,576 = 3,145,728,
against a measured 3,177,463. The string defect was not two-thirds of the
allocations, it was 99% of them. There was no temporal parsing in the benchmark
at all.

The profile confirmed it afterwards. Every entry in the top twelve by
`alloc_objects` belongs to **fixture generation** — `SinkParquet`, `WriteCSV`,
`FormatTemporal`, arrow-go's dictionary encoder — which is the one-time `load(b)`
setup. The `ScanCSV` read path does not appear at all.

**The pre-registered layout cache therefore did not land.** The plan made it
conditional on the profile showing layout probing; the profile shows none,
because nothing in this benchmark reads a temporal column. Shipping it would have
been an unmeasured change justified by a hypothesis that had already been
falsified. The underlying observation still stands and is still unaddressed: a
file whose date format sits late in `datetimeLayouts` pays every earlier failed
`time.Parse` on every row, and nothing measures that case. It needs a fixture
before it needs a fix.

---

## 3. Three teeth checks, one of which was a bad bug

Each was verified by reintroducing the defect and confirming the named test
fails.

**The leading zero.** `b.offs[:1]` → `b.offs[:0]`. Both
`TestScanCSVStringBufferIntegrity` and the pre-existing
`TestScanCSVBatchSizeInvariance` fail at batch sizes 1, 2 and 7, with

```
batch column "n" has 1 rows, but column "s" has 0
```

Batch size 1024 passes, because the file fits one batch and `finish` is never
called twice. That is what the batch-size loop is for.

**Byte offsets.** `int32(len(b.chars))` → `int32(utf8.RuneCount(b.chars))`.
`TestScanCSVStringBufferIntegrity` reports `"héll"` for `"héllo"`, `"日"` for
`"日本語"`, and a torn surrogate for the emoji pair. `TestCSVRoundTrip` — which
covers embedded commas, quotes and newlines — **passes**, because its data is
pure ASCII. That is the evidence the multi-byte case earns its place rather than
duplicating what was already there.

**A null that advances the offset.** The first attempt at this one was not a bug
at all:

```go
b.chars = append(b.chars, 0)              // appended BEFORE the offset
b.offs = append(b.offs, int32(len(b.chars)))
```

The tests passed, correctly. Nothing ever reads a null's bytes, and every later
value's window `[offs[i]:offs[i+1]]` is still right — the only cost is a wasted
byte. The dangerous version is the other order:

```go
b.offs = append(b.offs, int32(len(b.chars)))
b.chars = append(b.chars, 0)              // appended AFTER
```

which shifts the value *after* the null. Now the test fails with
`"\x00日本語"` where `"日本語"` was wanted — and `TestCSVRoundTripNullStrings`
misses it entirely, because its null is the last row and nothing follows it to be
shifted.

That is why the fixture puts a null **between two multi-byte values**. The
failure appears one row away from its cause, and `appendNull` now says so.

---

## 4. New test

`TestScanCSVStringBufferIntegrity` in `csv_test.go`, aimed at the layout rather
than at parsing, which is what the rest of that file covers. Seven rows through
four batch sizes:

| row | what it pins |
| --- | --- |
| `""` | empty is a value; offset does not advance |
| `"héllo"` | two-byte UTF-8 |
| `\N` → null | null between multi-byte values — catches a shifted offset one row later |
| `"日本語"` | three-byte UTF-8 |
| `"🌍🌎"` | four-byte UTF-8, surrogate-pair territory |
| 120,000 bytes | past the scanner's 64 KiB buffer, which grows by doubling with `make` + `copy` — the buffer *moves* mid-value |
| `"tail"` | the offsets recover after the large one |

Batch sizes 1, 2, 7 and 1024 exercise the cross-batch buffer reuse; the sizes are
chosen so the large value lands in different positions.

What was already covered and deliberately not duplicated: embedded commas, quotes
and newlines (`TestCSVRoundTrip`), null-vs-empty round tripping with a sentinel
(`TestCSVRoundTripNullStrings`), empty-is-not-null (`TestScanCSVEmptyStringIsNotNull`),
and the scanner's own differential against `encoding/csv` at buffer sizes down to
one byte (`scanner_diff_test.go`).

---

## 5. Three false statements, corrected

None of these change behaviour. All three were wrong in a repository people can
now `go get`.

**`internal/source/csv/writer.go`** — the default arm said *"Temporal, Enum and
nested types are refused rather than written as their storage integers"*, while
the case forty lines above formats all four temporal types. That case's own
comment even says temporal was a refusal "until now"; the default arm's list was
never updated to match. It now names Enum, Binary and the nested types, and
records that temporal used to be refused for exactly this reason and no longer
is.

A first draft of that correction said "Dictionary index", which is not a type
ursus has — the type is `Enum`. Caught by checking `dtype` before committing,
which is the same class of error being fixed.

**`bench/Makefile` and `bench/README.md`** — both repeated the claim step 21
disproved for the root README: *"GOEXPERIMENT=simd is MANDATORY for anything that
links ursus: package `simd` does not compile without it."* Only two files import
`simd`, both `//go:build goexperiment.simd && amd64` with `!(...)` scalar twins.
`export GOEXPERIMENT := simd` **stays** — a benchmark should measure the vector
path — but the justification is now "measures the vector path rather than the
fallback" rather than a compile requirement that does not exist.

---

## 6. A 34.6 MB binary in the public repository

`bench/micro/ursusmicro.test` — a compiled Go test binary — has been tracked
since the initial commit. It surfaced only because `go test -memprofile` writes
the test binary to the working directory, so `git status` showed it as *modified*
rather than untracked.

At 34.55 MB it was **99% of the repository's tracked bytes**; the next largest
tracked file is `bench/uv.lock` at 0.24 MB. The root `.gitignore` has had `*.test`
since step 21, which does not untrack a file already committed.

`git rm --cached` removes it going forward. **It remains in the git history**, so
every clone still pays for it. Excising it needs `filter-repo` and a force-push
to a public repository, which is a decision for the repository's owner, not a
side effect of a performance step.

---

## 7. What is still open

Unchanged from step 20 except where noted.

- **The serial join probe** — j1–j5 at 6–15x slower than polars, the largest
  remaining query-shaped gap. Riskiest to parallelise because the probe holds
  `matched`/`seen` state for Right, Full, Semi and Anti.
- **String kernels still allocate per value.** `StringSliceUpper` shows 2.1M
  allocations: a kernel producing strings builds them one at a time. This is the
  same defect a third time, and the third location — `data.NewStringParts` is
  already the answer. Of the three, this is now the cheapest and most obviously
  correct.
- **Temporal layout probing** has no fixture and therefore no measurement (§2).
- **The six `map[string]int32` hash tables**, ~11% of CPU.
- **`reverseSink.Merge` has no test**; `argExtremumAcc` and `positionAcc` still
  hold a `*data.Column` per group — the shape step 20 removed from min/max.
- **String sort keys** on the comparator fallback, **CSE**, **`JoinWhere`**,
  **nested types**.
