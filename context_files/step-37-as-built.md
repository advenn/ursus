# Step 37 — as built

**An in-memory frame is more than one batch now, and `WithThreads` finally does
something on it.** `memsrc` honoured no batch size, so every `ursus.Frame(...)` and
every `df.Lazy()` was a single batch — one unit of work, one worker, whatever the
caller asked for.

Authoritative where it disagrees with [`step-36-as-built.md`](./step-36-as-built.md),
the vision docs and [`design/`](./design/).

**1529 test cases green** — 1517 before this step — under `GOEXPERIMENT=simd` ×
`GODEBUG=simd={512,256,128,0}`, with the experiment off, and under `-race`
(`make test-all` exit 0, 75 package-ok lines across five configurations;
`make race` exit 0, 15). `make levels` and `go vet` clean in all three modules.
PDS-H SF=0.1 validates 22/22 for ursus.

```
opbench join, 5M ⋈ 100k, a plain in-memory frame

  before  -threads 8  ==  -threads 1     (one batch, one worker, by construction)
  after   -threads 1     841 ms
          -threads 8     225 ms          3.7x
```

---

## 1. The defect

`memsrc.Open` ignored `spec.BatchSize` and handed back the batches it was built
with. Both public ways in build exactly one — `ursus.Frame(Values(...))` through
`FromColumns`, `DataFrame.Lazy()` through `FromBatch` — so an in-memory frame was one
batch of however many rows. CSV and Parquet both honoured the batch size; this was
memsrc's alone.

Two documented knobs were untrue on this source. `WithBatchSize` did nothing. And
`WithThreads`, whose doc calls `WithThreads(1)` "the knob to reproduce the serial
operator tree exactly", made no difference at all, because there was never more than
one job to distribute — including after an ordinary `Collect` → `.Lazy()` round trip,
which is a normal thing to write.

Step 36 found this while trying to measure the join and worked around it twice
(`parChunked` in the tests, `chunkLeft` in opbench). Both are gone.

---

## 2. Splitting is only cheap because `Column.Slice` stopped copying

`Slice` already shared for every payload shape except one. Bool and validity
re-window their views; String and List re-window the offsets and keep the character
buffer or child column whole. **Fixed-width copied**, with a doc explaining that
Column carries no offset into its values buffer and adding one "would put a
`+ c.offset` in every kernel's inner loop to buy nothing today".

It buys something now, and it needs no offset field. `memory.SliceBuffer` produces a
buffer whose base **is** the sliced range, so `Values[T]` — which reads from the
start of the buffer — sees the right rows without knowing anything happened.

Two things made it safe, both checked rather than assumed:

- **Nothing writes into an existing column's payload.** The only two
  `data.Reinterpret` call sites are `dispatch.go`, writing into a buffer it just
  allocated, and `spill.go`, reading offsets.
- **A sub-buffer may be aligned to less than 64 bytes**, which `internal/arrowx`'s
  own doc already requires kernels to tolerate: *"ursus may observe … `memory.SliceBuffer`
  … Kernels must treat 64-byte alignment as an optimization hint, never as a
  precondition."* The contract was written for this case six steps before it arrived.

---

## 3. What the measurement says, including about itself

Four matched pairs on the shipped code, and three with the copy restored:

| | 1 thread | 8 threads | parallel speedup |
| --- | --: | --: | --: |
| **shared `Slice`** | 841 ms | **225 ms** | **3.73x** |
| copying `Slice` | 1,420 ms | 265 ms | 5.37x |

Read carefully, because the higher ratio belongs to the slower code. The copy is
serial work in `Next`, so it inflates both columns and inflates the single-threaded
one more. The number that matters is absolute: **sharing is worth about 1.2x at eight
threads.** The single-thread comparison spans 992–1,489 ms and is too noisy to quote,
and saying so is the point — this is the machine step 23 measured 18% apart on
unchanged code.

**Step 36's 1.3x was an artefact of its own workaround.** `chunkLeft` built 64 coarse
frames through a `Slice` that still copied; the real fix gives 610 zero-copy batches
at the default batch size. The honest summary of the two steps together is that the
join parallelises about 3.7x, and step 36 could not see it because the thing it was
working around was the thing that mattered.

---

## 4. Two teeth that did not bite, and why each is worth recording

**`n > size` versus `n >= size` on the tail** changes nothing, because the loop that
skips spent batches absorbs the difference: `>=` emits the final full batch and leaves
`off == Rows()`, which the skip loop then steps over. The off-by-one is real but
unobservable — so the teeth that DO bite are removing the remaining-rows guard
(panics: `Column.Slice out of range`) and removing the skip loop (a zero-row batch is
emitted, and a consumer that reads `Rows() == 0` as end-of-stream would truncate).

**Slicing the validity from 0 instead of from the offset** passed against the obvious
fixture. The null pattern was `i%2 == 0` and the offset was 2 — a multiple of the
period, so both windows give the same bits. With an irregular pattern
(`T,T,F,T,F,F`) it fails on the first row. This is the third fixture in three steps
to be blind to the bug it was written for; the pattern is always the same shape, a
fixture whose structure happens to be symmetric under the mistake.

---

## 5. Verification

`internal/source/memsrc/memsrc_test.go` is new — the package had no tests at all —
and covers the split arithmetic (a short tail, an exact multiple with no empty ninth
batch, a size larger than the frame, a size of one, an empty frame), the default when
no size is asked for, that batches already smaller are **not** merged, that empty
batches are skipped rather than returned, and that `Projections()` still records the
pushdown that this source exists to demonstrate. Every case checks the values, not
just the counts: a split that loses or repeats a row gets the shape right.

`internal/data/data_test.go` pins the slice as a view — asserting the sub-buffer's
first byte **is** the parent's byte at the offset — and that validity moves with the
values.

`parallel_test.go` runs its join cases on a plain `parFrame` again, with the
workaround deleted, at 1/2/3/4/8/16 threads.

And the whole suite is a batch-size invariance sweep that had never run: every query
over in-memory data now crosses batch boundaries where it used to see one batch.
Nothing moved, which is what the contract promised and nothing had checked.

---

## 6. What is still open

- **Coalescing small batches.** Splitting a large one was the defect; merging small
  ones is a different policy with its own argument.
- **The join build side** (`joinBuildSink.Merge`, implemented, tested, still never
  called), **Right/Full parallel probe**, and **`KeyTable.Get`'s memory latency** —
  step 36's list, unchanged.
- **The 4.7x memory number**, still unexplained and still un-profiled. `PeakRSS` reads
  `VmHWM`, a high-water mark, and Go's GC target is an untested hypothesis for part of
  it. The best next step, and the one the published numbers most need.
- Unchanged: nested writing and `as_struct`, `.list` set operations, Map/Array,
  Pivot/Unpivot, SQL, cloud stores, join reordering.
