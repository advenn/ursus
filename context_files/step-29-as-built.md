# Step 29 — as built

**A List column can be read from Parquet and carried through a query.** The
fourth payload shape, the repetition-level machine, `Take`, `Concat` and
rendering. Second v0.3 step.

Authoritative where it disagrees with [`step-28-as-built.md`](./step-28-as-built.md),
the vision docs and [`design/`](./design/).

**1426 test cases green** — 1421 before this step — under `GOEXPERIMENT=simd` ×
`GODEBUG=simd={512,256,128,0}`, with the experiment off, and under `-race`.
`make levels` and `go vet` clean in all three modules. PDS-H SF=0.1 still
validates 22/22.

```
tags: List(Int64) read from Parquet, projected beside a flat column

  ┌─────┬──────────┐
  │ id  │ tags     │
  ├─────┼──────────┤
  │ 1   │ [10, 11] │
  │ 2   │ []       │   <- present but empty
  │ 3   │ null     │   <- null list
  │ 4   │ [12]     │
  └─────┴──────────┘
```

Step 28 made nested columns *visible and refused*. This makes one of them
readable.

---

## 1. The fourth payload shape

`data.Column` documented three, and the doc was the contract:

```
fixed-width types  → fixed        (a flat values buffer)
Bool               → bits         (a packed bitmap)
String, Binary     → offs + chars (int32 offsets plus a character buffer)
List               → offs + child (the same offsets, into a child COLUMN)   <- new
```

`offs` is REUSED rather than a second offsets field being invented, so the
n+1-entries-with-a-leading-zero discipline and everything that already knows it —
`Slice` included — carry over. What differs is only what the offsets index into: a
flat character buffer for String, a whole `*Column` for List, which is what lets
the element be any type the child can be.

Two things this touched that were easy to miss:

- **`Slice`** re-windows offsets and keeps the payload; it had to keep the child
  too. Because offsets are not rebased, the child stays whole and the slice stays
  O(1) — the same trick the character buffer already used.
- **`Buffers()`** deduplicated within a column using a `[5]BufferID` array, sized
  for the five slots a column has. A List yields its child's buffers as well, and
  a child may itself be a List, so the count is bounded by nesting depth rather
  than five. A single list column would have run off the end of that array; it is
  a slice now.

---

## 2. Rows do not line up with levels

Every other reader here can treat one level as one row. A repeated column cannot:
`ReadBatch`'s budget is in LEVELS, one row holds as many levels as it has
elements, and a row may straddle two calls. That is why lists were not readable.

`listCol` keeps a **persistent cursor** into a block of levels, refills a block at
a time, and closes a row only when the next `rep == 0` arrives or the chunk ends.
`open` survives a refill, so a row longer than the block is assembled across
calls.

`file.RecordReader` would have handled row boundaries for free, and was
considered. It needs `LevelInfo`, an `arrow.DataType` and page-reader wiring — API
surface this reader deliberately does not use, since the package doc's whole
argument for the low-level path is avoiding pqarrow's machinery. The cursor is
about forty lines and depends on nothing new.

The four row states, which definition level alone distinguishes:

```
def == 3   an element is present
def == 2   a NULL element inside a present list
def == 1   a present but EMPTY list      <- consumes no element
def == 0   a NULL list                   <- consumes no element
```

The last two are the trap: both append nothing and leave the offset unmoved, and
only the validity bit separates them — the same shape as an empty string against a
null string, failing the same way.

**Scope**: fixed-width elements. `List(String)` needs `byteArrayCol`'s offsets
machinery underneath `listCol`'s level machine, which is a second problem rather
than a bigger one, and it is refused by name rather than half-read.

---

## 3. Carrying it

A column that can be read but not moved is not usable — a scan feeds `Take` and
`Concat` before anything reaches the caller.

**`takeList`** builds the child selection first and gathers it with ONE recursive
`Take`, rather than slicing per row. A row's elements are contiguous, so the whole
gather is one flat index list — which also means every element type is handled by
the recursion instead of another type switch.

**`concatList`** shifts every part's offsets past the elements already
accumulated, and concatenates the children through `concatColumn` for the same
reason.

Everything else keeps refusing and mostly already did: `spill.go` names
`TypeList` explicitly, and the arithmetic, comparison and aggregate kernels fall
through to their unsupported defaults. Sorting by a list, grouping by one and
joining on one all stay refused.

---

## 4. A bug the accessor's contract caused

`ListAccessor.Get` first returned `(0, 0, false)` for a null row, on the reasoning
that a null row's range is meaningless. It is not: a null list consumes no
elements, so its offsets are real and empty, and **any caller walking a column end
to end needs them**. `concatList` read the zeroed range and collapsed every offset
after a null row.

`Get` now always returns the real range, with `ok` reporting validity — so an
empty list and a null list return the SAME range and `ok` is the only thing that
separates them. That is the distinction stated once, in the one place every
caller goes through.

---

## 5. Teeth, including one that did not fire until the test was fixed

**Treat `def == 1` as a null list.** Fails at every level — the unit tests, and
the rendered frame showing `null` where `[]` belongs.

**Drop `takeList`'s child gather.** `row id=4 lost its list`. The test filters to
a NON-IDENTITY subset for exactly this reason: filtering to everything leaves the
offsets unchanged and would pass with the selection ignored entirely.

**Drop `concatList`'s offset shift — and this one PASSED.** The existing test
filtered to `id > 2`, and the first surviving row had a *null* list, whose child
is empty, so the shift was zero and removing it changed nothing. The test was
wrong, not the teeth. `TestListConcatShiftsOffsets` reads every row at batch size
1 so a two-element list sits in the first part; without the shift row 4 renders
`[10]` instead of `[12]` — it reads the first batch's elements, which is
plausible data and no error. Third step running where a teeth check found a hole
in a test rather than in the code.

---

## 6. Verification

`internal/source/parquet/list_test.go` writes def/rep levels by hand and drives
the state machine: every row state round-trips; a row longer than
`listLevelBlock` is reassembled across a refill (a fixture of short lists cannot
produce that case, and would pass with `open` reset on every refill); and batch
size is invariant.

`list_test.go` at the root does it through the public API: scan, project beside a
flat column, filter, collect, render.

Plus the full gate and **PDS-H SF=0.1 revalidated 22/22**, since `Take` and
`Concat` are on every query's path.

`git status bench/results/REPORT.md`: untouched.

---

## 7. What is still open

The arc, in order:

1. **`List(String)`** — the element decoder, §2.
2. **`Explode`**, then the **`.list` namespace** (37 methods). This is where lists
   become genuinely useful rather than merely readable.
3. **Struct**, which needs N children rather than one, and **Array**/**Map**.
4. **Writing** a List column, to Parquet or CSV.

Unchanged: **Pivot/Unpivot**, SQL, cloud stores, plan serialization. And the
performance ledger, none of which this touched — **join reordering** (no cost
model exists; PDS-H is 10.7x polars at SF=1 and the multi-way joins are worst),
**join parallelism** (8 threads slower than 1), **memory** (ursus is the most
memory-hungry engine in the field, 4.7x polars at SF=1), and the measurement
problem that makes all three hard to work on — the laptop's noise floor is ~30%.
