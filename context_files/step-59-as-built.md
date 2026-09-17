# Step 59 — as built

**A List's offsets need not start at zero.** This step was planned as Arrow import.
The survey for it found a silent wrong answer reachable today with **no import
involved**, on the most ordinary paths in the engine, and it went first. Import is
step 60; the decisions made for it are recorded in §7 so they survive.

Two commits and this document.

Authoritative where it disagrees with [`step-58-as-built.md`](./step-58-as-built.md),
the vision docs and [`design/`](./design/).

`make test-all` exit 0 (90 package-ok lines = 18 packages × 5 SIMD configurations),
`make race` exit 0 (18), `make levels` and `go vet` clean in all three modules, PDS-H
SF=0.1 exit 0 with **22/22** — each asserted on its own exit code.

---

## 1. Measured before anything changed

All four through the public API, three of them with no file at all:

| query | got | want |
| --- | --- | --- |
| `Tail(2)` then `list.sum` | **163** | 100 |
| `df.Lazy().Collect(WithBatchSize(1))` over six rows | an empty list holding **eight** elements; row 2 holding row 6's | the frame unchanged |
| `list.sum` / `min` / `max` / `mean` at batch size 1 | **3163 / 1 / 2000 / 395.375 for every row**, the null list included | per-row answers, null for the null list |
| `Str().Split(",")` then `df.Lazy()` at batch size 1 | `["c","d","e","f","g","h","a","b","c","d","e"]` | `["c","d","e"]` |

`163` is the cleanest statement of the defect: row 5's `100`, plus `1+2+10+20+30` from
rows 1 and 2, which sit entirely **before** the window `Tail(2)` kept. `3163` is the
sum of every element in the column: each one-row batch fed its whole child into row 0.

## 2. How ordinary the path is

`Column.Slice` keeps a List's offsets absolute and its child whole — *"the offsets are
not rebased, so the child stays whole and the slice stays O(1)"*. Two kernels assumed
the opposite:

- **`listReduce`**, behind `list.sum/min/max/mean`, allocated a zero-filled group
  array over the **whole child** and handed every element to the accumulator. Elements
  outside the window stayed in group 0.
- **`concatList`**, behind **every `Collect`** — `exec.Collect` ends in
  `kernel.Concat` — shifted each part's end offsets by the previous parts' **whole
  child lengths** and concatenated **whole children**.

The memory source slices any stored batch larger than the batch size, and every eager
`DataFrame` method is `Lazy().op().Collect()`. So **any eager operation on a List frame
larger than 8192 rows** took this path at the default batch size. `Tail`, `Slice` and
`Head` reached it too, through `Batch.Slice`.

## 3. The root cause was three sentences in one file

`internal/data/column.go` stated the List invariant three times, and all three
contradicted `Column.Slice` a few hundred lines away:

- `NewList`: *"offs[0] == 0 and offs[n] == child.Len() — the ordinary Arrow
  invariant"*
- `NewStringParts`: the same, for strings
- `Child()`: *"the elements of EVERY row concatenated"*

The kernels believed the constructor. And none of the three is Arrow's rule — Arrow
reads offsets as absolute, and a sliced array's first offset is routinely non-zero.

The invariant is now stated **once**, on `ListAccessor.Window()`, which returns the
`[lo, hi)` of the child that belongs to a column. The three docs say that a
*constructed* List starts at zero and a *derived* one need not, and point there. Both
kernels restrict themselves to the window; `concatList` concatenates windowed children,
which fixes the contents and the doubled memory together.

**Strings were never affected.** Their concat reads each row through `Get` over
absolute offsets and rebuilds with `NewString`, which is also why the extensive string
batch-size testing never surfaced any of this.

## 4. Why nothing caught it — and the first explanation was wrong

Every List batch-size test read from **Parquet**, and the Parquet reader builds a fresh
List column per batch. The eighth recorded instance of *the fixture, not the test, is
the unit of coverage*.

The plan for this step said the blind spot was *"the offsets start at zero"*, and
predicted that a sweep slicing only at row 0 would go silent against the broken
kernel. **A tooth refuted that.** A slice at row 0 that stops short of the end still
failed, because `listReduce` absorbed the elements **after** the window as well as
before it.

The shape that actually hides the defect is a column whose **child is exactly its
window** — no head and no tail. That is precisely what Parquet builds for every batch.
With only that window, the broken kernel produced **zero** comparison failures. The
sweep's guard now names that property, not the one the plan guessed.

## 5. The sweep

The consumer set of the contract is enumerable, and it was enumerated:

| consumer | reading before | measured |
| --- | --- | --- |
| `listReduce` | broken | **broken — fixed** |
| `concatList` | broken | **broken — fixed** |
| `listRebuild` (7 functions), `listGet`, `listContains`, `listLen` | absolute ranges, then `Take` | sound |
| `takeList` | absolute ranges, then `Take` | sound |
| `physical/explode.go` | absolute ranges, then `Take` | sound |
| `concatStruct` | recurses into `concatList` | broken *transitively* — fixed with it |
| `arrowout` export | absolute offsets + whole child | sound; it is the oracle |

- **`internal/physical/listoffsets_test.go`** — all 14 `.list` functions through
  `Eval`, on six windows, asserting `f(full.Slice(k,m)) == f(full).Slice(k,m)`. The
  function list is derived through step 56's `allCallFns`, so a list function added
  later is swept the day it is named. **The oracle is arrow-go's `array.Equal`** over
  `arrowout` exports: an implementation that is not ursus, reading offsets as absolute
  exactly as Arrow specifies, so it cannot share a blind spot with the kernels.
- **`internal/kernel/listconcat_test.go`** — `Concat` of consecutive slices, `Take`
  from a slice, a Struct holding a List, a List of Lists, and the exact-window length.
- **`listbatch_test.go`** — §1's four measurements as regressions, plus `Explode`.

Several windows are not thoroughness for its own sake. With the kernel reverted,
`list.min` over `[1,4)` still **passed**: row 1 is `[1,1,2]`, the absorbed `[5,6]` are
larger, and the minimum is coincidentally right. `max` and `sum` over the same window
failed.

## 6. Teeth

| tooth | result |
| --- | --- |
| revert `concatList` | **bites** — on the Parquet path and the file-free `Split` path |
| revert `listReduce` | **bites** — at batch sizes 1 and 2, and on `Tail` |
| revert `listReduce`, against the sweep | **bites** — 18 failures across sum, min, max and mean |
| slice only at row 0, kernel broken | **did not go silent** — §4; the plan's explanation was wrong |
| only the child-equals-window shape, kernel broken | **zero comparison failures**; the guard fires alone |
| rebase offsets but keep whole children | **every value test passes**; the child is 55 elements for 11 across five slices |
| fully broken `concatList`, against the Struct test | **bites** — reached through `concatStruct`, not vacuous |

The second-to-last is worth reading twice. With whole children kept, every part slices
**one shared parent**, so rebased offsets land on the first copy of its child and read
the right elements. A 5× memory blow-up with every answer correct — invisible to any
value assertion, which is why the length is asserted on its own.

## 7. Decided for step 60 — Arrow import

- **API:** `ScanArrow(open func() (array.RecordReader, error))` and
  `ScanArrowRecords(recs ...arrow.Record)`. No source in ursus reads a one-shot stream,
  and the rule is written three times — `ScanCSVReader` takes `[]byte` because *"a
  source is opened more than once, once for inference, once per execution, and a
  Reader cannot be rewound"*. A self-join opens a source twice within one query.
  `ursus-api.md:1021` sketches `ScanArrow(rdr array.RecordReader)` and must change.
- **Always nullable.** `arrow.Field.Nullable`'s zero value is `false`, Go producers
  leave it unset while writing nulls, arrow-go never checks, and `rule_simplify` folds
  `x AND lit(false)` to `lit(false)` exactly when `x` is declared non-nullable.
- **Copy on import.** Zero-copy is unsafe with today's types. `bitmap.View`,
  `StringAccessor` and `ListAccessor` hold raw `[]byte` and cannot anchor an owner;
  strings from `Get` reach user code; and the allocator behind a `*memory.Buffer` is
  unexported and undetectable. C Data memory is freed through `ArrowArrayRelease` on the
  last release, and an IPC record is valid only until the next `Next()`.
- **Two design claims are unsafe as written.** `design/foundation.md` attaches
  `runtime.AddCleanup(b, …)` to a `*Batch`, but `Batch.Slice` and every `Rename` create
  objects that never reference `b`. `arrowx`'s *"Adopt must Retain and attach a
  runtime.AddCleanup"* is insufficient on its own for the same reason. Both are
  corrected when import lands.
- **The type map is total over 45 `arrow.Type` values**, including `LIST_VIEW` and
  `LARGE_LIST_VIEW`.
- **Int128** round-trips through an `ursus:`-namespaced field metadata key, which
  survives both IPC and C Data. Plain Decimal128(38,0) stays Decimal.
- **Time** values outside a day are wrapped on import, as `Cast` does; arrow-go never
  range-checks them.
- **Import must rebase List offsets when it copies** — a sliced foreign array carries a
  non-zero `Data().Offset()` and absolute offsets. This step's contract, arriving from
  outside.

## 8. Still open

- `internal/data/unsafe.go` says *"every buffer comes from arrowx and is 64-byte
  aligned"* — already false, since `Column.Slice` uses `memory.SliceBuffer`. No kernel
  depends on alignment (`arrowx.IsAligned` has no callers), so it is a prose claim.
- `listSort` and `listUnique` build a comparator or key encoder over the **whole**
  child. Correct — the sweep confirms it — but wasteful on a narrow slice of a large
  column.
- `data.BufferID` is `*byte`, described as *"meaningful to the garbage collector"* —
  true for Go memory, false for C memory. Matters only if zero-copy import is attempted.
- `Datetime ± Duration` wraps int64 silently; the byte-flip spill sweep, deferred a
  seventh time; `Optimizer.Verify` off in the golden inventory and `Explain`; UDF name
  uniqueness, where `==` on two `UDFImpl`s panics; List/Struct absent from the shared
  contract fixture; `listCallOut` returning `Float64` for `list.mean` without checking
  the element type; ~37 weak `errors.Is` assertions; `Writer.Write`'s unwrapped error;
  `callCache` eviction; `Pivot` awaiting a decision; and a benchmark suite thirty-six
  commits stale.
