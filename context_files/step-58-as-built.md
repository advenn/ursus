# Step 58 — as built

**Arrow export.** `df.Record()`, `df.ArrowSchema()` and `lf.CollectRecords()` hand
ursus's output to the rest of the Arrow ecosystem without copying it — the boundary
`dataframe-features.md` §10.4 calls *"the important one — the zero-copy FFI boundary
that lets any language consume our output"*, and which the v0.3 scope list omitted.

The first feature step since 47. It is worth saying what changed about the method:
the last ten steps found silent wrong answers **after** shipping. This one found its
silent wrong answer **during the survey, before a line was written**, and the code
was designed around it.

Authoritative where it disagrees with [`step-57-as-built.md`](./step-57-as-built.md),
the vision docs and [`design/`](./design/).

`make test-all` exit 0 (**90** package-ok lines = 18 packages × 5 SIMD
configurations), `make race` exit 0 (18), `make levels` and `go vet` clean in all
three modules — each asserted on its own exit code. **PDS-H SF=0.1 validates 22/22.**
No new dependency: arrow-go was already a direct require.

---

## 1. Why this is mostly free

ursus has stored Arrow's layout all along. `data.Column` holds `*memory.Buffer`s
allocated through `arrowx`, validity is an LSB-numbered bitmap, strings are int32
offsets plus a character buffer, lists are offsets plus a child. **A `data.Column` is
an Arrow array with a different Go type wrapped around the same bytes**, so the
export is a re-wrap and the bytes never move.

Two pieces of the design were already waiting for this and say so:

- `bitmap.View.Buffer()` exists, and its doc reads *"for handing to
  `array.NewData`"*. It normalises a non-zero bit offset and returns nil for the
  all-set form, *"which is what Arrow expects when null_count is zero."*
- `arrowx`'s package doc already names the obligation the **import** direction will
  have: *"when it does, Adopt must Retain and attach a runtime.AddCleanup."*

`internal/arrowout` sits at level 25, beside `internal/spill` and for the same
reason: both turn a Batch into a foreign representation and need exactly `data`,
`dtype`, `bitmap`, `arrowx` to do it.

---

## 2. The seam that would have been silent

`i128.Int128` is `{Hi int64, Lo uint64}`. `arrow/decimal128.Num` is
`{lo uint64, hi int64}`. **The word order is reversed.**

Both are 16 bytes, both are two's complement, and a `data.Column` holding Int128 has
exactly the byte width Arrow's Decimal128 wants — so the obvious zero-copy wrap
compiles, produces a structurally valid Decimal128 array, and is wrong by a factor
of 2^64.

It is not a corner. **Every integer `Sum` outputs Int128**, so this is the ordinary
group-by path:

```
GroupBy(region).Agg(Col(qty).Sum())   eu = 9
                                      exported zero-copy: 9 * 2^64
```

Arrow has no 128-bit *integer* type at all; `Decimal128(38, 0)` is how one is
spelled. Precision 38 covers ~1e38 against int128's 1.7e38, so the extreme ends of
the range sit outside the declared precision while remaining exactly representable in
the 128 bits Arrow stores — named in the code rather than left for a reader to hit.

The words are written explicitly, low then high, rather than through
`decimal128.New`, so the layout this depends on is stated in the file instead of
silently following a struct definition in another module.

### The second seam

ursus stores every `Time` as int64 ticks. Arrow puts seconds and milliseconds on
**Time32** and only microseconds and nanoseconds on Time64, so two of the four
resolutions narrow. **Step 57 is what makes that narrowing total**: a time of day is
now below 86,400,000 milliseconds, a thousandth of int32's range. The bounds check is
kept anyway, because a bare `int32()` here would be the third instance of the
construct step 49 removed from `rescaleTemporal` and step 57 removed from the Parquet
writer.

---

## 3. An ownership claim I made, then had to correct

`arrow.Buffer.Release()` at a zero refcount does `b.buf, b.length = nil, 0` — it nils
the slice header **inside** the shared Buffer, so anything else pointing at it stops
having data. Silently.

So every exported buffer is re-wrapped with `memory.NewBufferBytes`, which yields a
Buffer with no allocator and no parent and therefore an inert `Release`. Still
zero-copy; only the refcounting is severed.

**The first version of that comment claimed this prevents a caller's double-release
from emptying a frame they still hold. Toothing it proved otherwise, twice.**

- Handing out ursus's own buffers changed nothing, because `data.Column` keeps its
  `*memory.Buffer` fields unexported and offers only `RawFixed`/`RawOffsets`/
  `RawChars`, which return `[]byte`. The payload path **cannot** leak a live Buffer.
- Aiming at validity — the one path that does yield a live, allocator-backed Buffer,
  when a non-byte-aligned `Column.Slice` leaves a bit offset — still changed nothing,
  because `Record.Release` is itself refcounted, so releasing a record twice does not
  release its buffers twice, and this package holds a reference that stops the count
  reaching zero.

So the guard prevents nothing reachable today. It is kept, because it turns the
contract into a **sentence** — *"Release on an exported buffer is a no-op"* — that is
unconditionally true and safe to promise in the public doc, rather than a case
analysis that would rot. The comment now says exactly that, and the test asserts the
property directly by over-releasing a buffer rather than through a scenario that
cannot happen.

Shipping the first version would have been a prose claim nothing checks — the
category step 51 measured as this project's largest, at 21 of 68 findings.

---

## 4. What it unlocks

- **Arrow IPC / Feather output**, for the cost of the writer the caller already has.
  `TestRecordSurvivesAnIPCRoundTrip` does exactly that, and it doubles as an
  independent reader of the layout: if offsets, validity or widths were wrong in a
  way ursus's own accessors tolerate, that is where it shows.
- **Flight, ADBC, pqarrow, and any Go library that speaks `arrow.Record`.**
- **A nameable public type.** `DataFrame.Batch() *data.Batch` is an exported method
  returning an internal type — undocumentable and unreferenceable from outside the
  module. `Record()` is the supported answer to the same question.
- **The import direction**, which is the extension point: once `ScanRecords` exists,
  any format can feed ursus. `arrowx` already specifies what it must do.

---

## 5. Teeth

| tooth | result |
| --- | --- |
| export Int128 with the tempting zero-copy wrap | **bites** — `1` exports as 2^64 |
| send milliseconds to Time64 | **bites** — `Time(ms) exported as Time64, want Time32` |
| drop `Date` from the type map | **bites** — *"Date has a sample and no Arrow type"* |
| hand out ursus's own buffers | **did not bite** — §3, twice, and the doc changed rather than the test |
| over-release an exported validity buffer | **bites**, once aimed at the property instead of a scenario |

The Int128 tooth is worth reading for a second reason: the fixture's row 1 is `-1`,
whose two words are `{-1, ^0}` — **symmetric under the swap**, so it stays silent. The
test only catches the defect because row 2 has two distinct words, which the fixture
comment predicted before the tooth was run.

And `TestEveryCitedTestExists` caught a comment citing `TestEveryTypeIDIsMapped` when
the test is `TestEveryTypeIDIsMappedOrNamed` — the fifth step running that an
instrument has caught my own mistake.

---

## 6. What is still open

- **The import direction.** `ScanRecords` plus `arrowx.Adopt` with `Retain` and a
  `runtime.AddCleanup`, which is the half where a mistake is a use-after-free rather
  than a wrong answer — foreign memory's `Release` calls back into the producer.
- **Array, Enum, Categorical and Uint128 are refused by name.** Enum and Categorical
  are declared P1 in the feature table and are still not constructible from the
  public API at all, which is the deeper gap; the refusal here is a symptom.
- **`DataFrame.Batch() *data.Batch` and `ursus.Scan(plan.Source)`** remain exported
  methods over internal types. `Record()` gives the first a supported replacement;
  `Scan` needs the public Source interface, which is expensive for the reason the
  audit found — `BatchSource.Next` returns `*data.Batch` and `ScanSpec.Predicate` is
  `[]expr.Node`, so a user-implementable source means exporting the column layer and
  the IR.
- **`Datetime ± Duration` wraps int64 silently** — same family as step 57,
  deliberately deferred there and still open.
- **The byte-flip spill sweep**, deferred a sixth time.
- The standing list: `Optimizer.Verify` off in the golden inventory and in `Explain`;
  UDF name uniqueness unenforced, with `reflect.ValueOf(impl).Pointer()` the only way
  to compare two `UDFImpl`s because `kernel.ColumnUDF` is a func type and `==` on one
  panics; List/Struct absent from the shared contract fixture with ~32 predicted
  disagreements; `listCallOut` returning `Float64` for `list.mean` without inspecting
  the element type; ~37 weak `errors.Is` accept-assertions; `Writer.Write`'s
  unwrapped error; `callCache`'s missing eviction; `.list` set operations;
  `Expr`-level selection; `Pivot` awaiting a decision rather than work; and a
  benchmark suite now **thirty-three** commits stale on a machine that cannot run it.
