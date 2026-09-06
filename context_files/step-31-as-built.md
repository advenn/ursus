# Step 31 — as built

**`List(String)` reads.** The level machine and the element accumulator are now
separate things, which is what made it a small change rather than a second copy of
the machine.

Authoritative where it disagrees with [`step-30-as-built.md`](./step-30-as-built.md),
the vision docs and [`design/`](./design/).

**1443 test cases green** — 1437 before this step — under `GOEXPERIMENT=simd` ×
`GODEBUG=simd={512,256,128,0}`, with the experiment off, and under `-race`.
`make levels` and `go vet` clean in all three modules. PDS-H SF=0.1 still
validates 22/22.

```
  id | tags
   1 | ["go", "rust"]
   2 | ["go"]
   3 | ["zig", "go"]

  .Explode("tags").GroupBy("tags").Agg(Count())   ->  go 3, rust 1, zig 1
```

---

## 1. The level machine was generic for no reason

`listCol` was `listCol[P, T]` — parameterised over the element's physical and
logical type — and of its fifty lines of repetition-level state machine, exactly
**three** were element-specific: the typed `ReadBatch` buffer, appending a present
value, and appending a null. The cursor, the row boundaries, the four row states
and the offsets are all element-agnostic and were being re-instantiated per type.

So the element side became a `listElems` interface, and `listCol` stopped being
generic. Two implementations:

- **`fixedElems[P, T]`** — what the generic `listCol` already did, moved.
- **`byteArrayElems`** — and this was already written, in `byteArrayCol`:
  characters appended into one arena with an offset pushed per element, finished
  through `data.NewStringParts(...).WithDType(elem)`. Step 22 built that shape to
  kill a per-value allocation; it is reused rather than rewritten.

The pay-off beyond String is that `List(Bool)` and `List(Decimal)` are each one
more accumulator now, not another instantiation of the whole machine. The refusal
names them so the gap is visible.

---

## 2. Two offset arrays, at two levels

This is the one thing the interface exists to keep straight, and it is where the
step could have gone quietly wrong.

A `List(String)` column has **two** offset arrays: the list's, counting ELEMENTS,
and the string child's, counting BYTES. `listCol` builds its offsets from
`elems.count()`, so `count()` has to return the element count — which for the
byte-array accumulator is `len(offs)-1`, not `len(chars)`.

Confusing them does not crash. The list's offsets start counting bytes, so row
lengths go wrong in proportion to how long the strings are — and a fixture of
single-character elements would pass, because there the two counts agree. The
teeth check uses `"a"`, `"bb"` and multi-byte UTF-8 for exactly that reason.

---

## 3. Nothing outside the reader changed, and that was the claim

Checked before writing anything, and it held: `takeList` gathers the child with
`kernel.Take`, `concatList` uses `concatColumn`, `renderCell` recurses, and
`explodeOp` takes from the child — all of which already handled String.
`data.NewList` derives its type from the child, so the constructor needed nothing.

The end-to-end test is the proof rather than the assertion: read a
`List(String)`, render it, explode it, group by the elements, filter after
exploding — no code outside `internal/source/parquet` was touched.

The gate widened too. `fieldType` tested `dt.Inner().IsFixedWidth()`, which String
fails because it has no entry in `bitWidths` — that is what produced *"only lists
of fixed-width elements can be read"*. It is now `readableElem`, which mirrors
`newListElems` and has to stay in step with it: one decides whether the column
gets a leaf, the other whether the leaf can be decoded.

`List(Binary)` came free — the same `*file.ByteArrayColumnChunkReader` serves both
and only the finishing dtype differs.

---

## 4. Verification

The existing list tests were the regression net for the refactor, and that is why
the machine could be restructured with confidence:
`TestListRoundTripsEveryRowState`, `TestListSpansTheReadBlock` and
`TestListIsBatchSizeInvariant` cover all four row states, a row spanning a level
refill, and batch-size invariance. They passed unchanged.

New, mirroring them for strings, and covering the two states only a
variable-length element type has:

- an **empty string** element, which is not an empty list and not a null;
- a **null element** inside a present list, which moves the element offset but
  not the byte offset;
- multi-byte UTF-8, because the child's offsets are byte offsets — the same trap
  step 22 caught in the CSV reader;
- a string row longer than `listLevelBlock`, for the cursor under the new
  accumulator.

**Teeth**: `count()` returning `len(chars)` fails every row-state test at every
batch size; dropping `appendNull`'s offset push does the same.

Plus the full gate and **PDS-H SF=0.1 revalidated 22/22**, since the Parquet
reader is on every query's path and part of it was restructured.

`gofmt` also caught two files it had been unhappy with, one of them
`radix_bench_test.go` from step 24. Both formatted. (`gofmt -l` reports
`internal/data/series.go` as an error rather than a diff — it cannot parse Go
1.27 generic methods — which is pre-existing and not a formatting problem.)

---

## 5. What is still open

The list arc has reached the point where lists are genuinely usable: read a
`List(Int64)` or `List(String)`, explode it, and the whole engine applies.

1. **The `.list` namespace** (37 methods) — `len`, `get`, `contains`, `sum`,
   `unique`, the set operations. This is the next real capability rather than
   plumbing, and it is what lets a caller work with a list WITHOUT exploding it.
2. **`List(Bool)`, `List(Decimal)`, nested `List(List(...))`** — one accumulator
   each now.
3. **Struct**, which needs N children rather than one; then **Array** and **Map**.
4. **Writing** a List column, to Parquet or CSV — still refused outright.

Unchanged: **Pivot/Unpivot**, SQL, cloud stores, plan serialization. And the
performance ledger, which this whole arc has not touched — **join reordering**
(no cost model exists; PDS-H is 10.7x polars at SF=1, worst on the multi-way
joins), **join parallelism** (8 threads slower than 1), **memory** (the most
memory-hungry engine in the field, 4.7x polars at SF=1), and the ~30% measurement
noise floor that makes all three hard to work on.
