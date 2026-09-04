# Step 19 — as built

gb7's wrong answer. **The cause was a bug in arrow-go, not in ursus** — and not
where the evidence pointed.

Authoritative where it disagrees with [`step-18-as-built.md`](./step-18-as-built.md),
the vision docs and [`design/`](./design/).

**1386 test cases green** — 482 top-level and 904 subtests (1378 before this step)
— under `GOEXPERIMENT=simd` × `GODEBUG=simd={512,256,128,0}`, with the experiment
off, and under `-race`. `make levels` and `go vet` clean.

```
h2o gb7   sum_range_v1_v2 = 399879, 0 nulls        <- the duckdb reference, exactly
                     was = 399495, 96 nulls
```

**h2o is now 15/15 validating.** It was the only failing query in either suite.

---

## 1. The bug

`arrow-go v18.7.0`, `arrow/bitutil/bitmaps.go:535`, inside `alignedBitmapOp`:

```go
endMask := (lOffset + length%8)
```

That parses as `lOffset + (length % 8)`. It plainly means `(lOffset + length) % 8`
— *does this range end mid-byte?* Everything downstream follows from the typo:

```go
lastByteMask := byte(0)
if endMask != 0 {
        lastByteMask = TrailingBitmask[(lOffset+length)%8]   // TrailingBitmask[0] == 0xFF
}
out[nbytes-1] = (out[nbytes-1] & lastByteMask) | (op.opByte(...) &^ lastByteMask)
```

When a range starts at a non-zero offset and **ends on a byte boundary**,
`endMask` is spuriously non-zero, `lastByteMask` becomes `TrailingBitmask[0]` =
`0xFF`, and the final byte is masked out completely — left as whatever the
freshly allocated destination held, which is zeros.

So `BitmapAnd`, `BitmapOr` and `BitmapAndNot` **silently dropped the last eight
bits** of every result whose source offset was non-zero. With offset 0 the typo is
harmless: `endMask` degenerates to `length % 8`, which is the value the correct
expression would produce.

### How it reached a user

Every operand sliced out of a larger column carries a bit offset — which is every
batch after the first. h2o gb7 is `GroupBy(id3).Agg(Max(v1), Min(v2))` followed by
`max_v1 - min_v2`: 100,000 groups emitted in twelve 8192-row batches, and the
subtraction combines two validity bitmaps per batch. Eleven of the twelve batches
started at a non-zero offset, and each lost its last 8 rows.

**96 nulls = 12 runs of 8**, ending at 16383, 24575, … 98303, 99999. Every one the
tail of an output batch; the first batch, at offset 0, was always clean.

---

## 2. It was not where the evidence pointed

Step 17 recorded a lead: `kernel.assembleRows` computes a per-group validity
bitmap and never uses it, relying on `concatColumn` instead. That is a real smell,
it sat directly under Min/Max, and it was wrong as a diagnosis.

The bisect took four minutes and settled it:

| | |
| --- | --- |
| `Max(v)` alone | clean |
| `Min(w)` alone | clean |
| `First(v)`, `Last(v)` | clean |
| `Sum(v)` (control) | clean |
| **`Max(v) - Min(w)`** | **16 nulls, at 16376–16383 and 19992–19999** |

Only the combination failed, which exonerated the accumulators outright and put
the fault in the one thing the arithmetic adds: combining two validity bitmaps.
From there it reduced to three lines —

```go
a := allOnes(20000).Slice(8, 16)
And(a, a).CountSet()   // 8, want 16
```

— and then to the byte level: `BitmapAnd` over two all-ones bytes produced
`[11111111, 00000000]`.

The plan said "no fix lands before a failing test does", and this is why. The
plausible suspect would have produced a plausible patch to code that was not
broken.

---

## 3. The fix

`bitmap.And`, `Or`, `AndNot` and `Not` stop calling arrow-go and use ursus's own
`Words`/`AppendBits` machinery — one shared `zip` helper, eight lines:

```go
func zip(a, b View, op func(x, y uint64) uint64) View {
	out := NewBuilder(a.length)
	for pos := 0; pos < a.length; {
		n := min(a.length-pos, 64)
		out.AppendBits(op(a.word(a.offset+pos, n), b.word(b.offset+pos, n)), n)
		pos += n
	}
	return out.Finish()
}
```

That machinery exists for exactly this. `Words`' own doc calls it *"the
abstraction that makes offset handling a solved problem rather than a recurring
bug"*, and `Builder`'s explains that it was written after defect D14b — a
bit-packing bug *"invisible on the machine that wrote the code"*. This step is
that claim being cashed: the ops that bypassed it were the ones that broke.

`AppendBits` masks to the live bit count, so `^word`'s garbage above `n` needs no
handling in `Not`.

---

## 4. The blast radius was much wider than gb7

`bitmap.And` has ten callers: validity combination for every comparison and
arithmetic kernel (`combinedValidity`), three paths in `unary.go`, and the join's
`AndMulti`. `Not` backs the boolean `not` kernel. **Any of them, over a column
sliced at a non-zero bit offset, lost the last eight validity bits.**

gb7 is simply where a benchmark noticed. It needed three things at once — an
output larger than one batch, an operation combining two validity bitmaps, and a
checksum compared against another engine — and only gb7 had all three. TPC-H at
sf=0.1 produces small results that fit in a single batch at offset 0, which is why
22/22 passed throughout.

Re-checked after the fix, against the duckdb reference: **gb3, gb7, gb8, gb9 and
gb10 all match exactly**, including gb10's 10,000,000 rows. Nothing that was right
became wrong.

---

## 5. Verification

`make test-all` (four SIMD widths plus experiment-off), `make race`,
`make levels`, `go vet` — all clean.

| Test | What it catches |
| --- | --- |
| `TestCombinatorsEndingOnAByteBoundary` (new) | the defect at the unit level: `offset × length` swept exhaustively over 17 × 72 combinations, against the slow reference model. It also sweeps `Buffer()`, which normalises an offset away through `bitutil.CopyBitmap` — the same risk class, another arrow-go entry point taking an offset and a length. That one is clean at every residue, checked rather than assumed |
| `TestArithmeticOverAggregateSpanningBatches` (new) | the gb7 shape end to end — 20,000 groups, `max - min`, no row may be null |
| `TestValiditySurvivesEveryOutputBatch` (new) | the same property across `sub`, `add`, `mul`, `gt` and Kleene `and` |
| existing `TestCombinators` | unchanged, and see below |

**Teeth**: restoring arrow-go's `BitmapAnd` fails the unit sweep at
`And off=8 len=8` and reproduces gb7's exact signature end to end — *"16 rows are
null … first at [16376 … 16383, 19992 … 19999]"*.

### Why eighteen steps of testing missed it

`TestCombinators` already swept offsets `{0, 1, 5, 8, 13}` at `n=300`, explicitly
*"so the operations are exercised on unaligned inputs"*. It passed with the bug
present, for a reason that is pure arithmetic: the defect only bites when the
range **ends** on a byte boundary, and `(off+300) % 8` is 4, 5, 1, 4, 1 for those
five offsets. Never 0.

The bug was one constant away from being caught, and the test that would have
caught it was already written. That is why the new test sweeps residues
exhaustively rather than sampling: a hand-picked offset list cannot be shown to
cover a residue class, and this one did not.

---

## 6. What was NOT done, and why

The step-19 plan asked, conditionally, how far to go **if the root cause landed in
`extremumAcc`** — whose per-group `*data.Column` storage is why gb7 takes 28
seconds against polars' 1.4. The answer chosen was a rewrite to flat typed
storage.

**The condition did not hold.** `extremumAcc` is not the cause; `Max` and `Min`
alone were correct all along. Rewriting it would have been a ~200-line performance
change riding along on a correctness fix, authorised under a premise that turned
out to be false — so it was not done.

It remains worth doing, unchanged from step 17 §9: `extremumAcc` stores one heap
`*data.Column` per group, allocates a `map[int32]int` per batch and calls `Take`
plus `concatColumn` per group per batch, where `sumAcc` directly above it uses
flat typed slices. gb7 is 28 s at eight threads and 78 s at one. That is a
performance step with its own before/after, not a rider on this one.

---

## 7. Honest gaps

- **The arrow-go defect is unreported upstream.** It is present in v18.7.0 and
  affects `BitmapAnd`, `BitmapOr`, `BitmapAndNot`, `BitmapXor` and `BitmapXnor`,
  all of which route through `alignedBitmapOp`. ursus no longer calls them;
  everyone else still does.
- **`bench/results/REPORT.md` predates steps 17–19** for ursus and still shows
  gb7 struck through. Refreshing it needs a quiet machine and a full `make bench`.
- **`extremumAcc`'s storage** (§6), the CSV reader's per-value string allocation,
  string sort keys on the comparator fallback, and the six `map[string]int32`
  hash tables are all unchanged from step 18 §6.

---

## 8. Files

| File | Change |
| --- | --- |
| `internal/bitmap/bitmap.go` | `And`, `Or`, `AndNot`, `Not` reimplemented on `Words`/`AppendBits` via one shared `zip`; the four `bitutil.Bitmap*` calls removed |
| `internal/bitmap/bitmap_test.go` | `TestCombinatorsEndingOnAByteBoundary`, and why `TestCombinators` could not see it |
| `groupbatch_test.go` (new) | the gb7 shape end to end |
