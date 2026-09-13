# Step 53 — as built

**The substrate gets its first tests.** `internal/uerr` — 355 lines, imported by 129
files, the most-depended-on package in the repository — had **no test file at all**.
`internal/spill`'s five tests were every one a happy-path round trip; nothing
truncated or corrupted a file, so every recovery path was unexercised.

This is step 51's measurement acted on: tests were 48× denser at the public API than
in the engine, and these two packages sat at the bottom of it.

Authoritative where it disagrees with [`step-52-as-built.md`](./step-52-as-built.md),
the vision docs and [`design/`](./design/).

`make test-all` exit 0 (**85** package-ok lines = 17 packages × 5 SIMD
configurations), `make race` exit 0 (17), `make levels` and `go vet` clean in all
three modules — each asserted on its own exit code. **PDS-H SF=0.1 validates 22/22.**

---

## 1. A silent wrong answer, reproduced by execution

`physical.callCache` memoises a compiled `Call` so a regex is not recompiled once per
8192 rows. It was keyed on the `*expr.Call` pointer **alone** — but `is_in`'s probe
set is encoded against the **receiver's type** through `GroupKeyEncoder`, and
`list.contains`' needle against the element type.

An `ursus.Expr` is a plain value, so hoisting a predicate is ordinary use:

```go
probe := ursus.Col("id").IsIn(int64(2), int64(3))
frameA.Filter(probe)   // id is Int64: encodes, caches
frameB.Filter(probe)   // id is Int32: probed against the Int64 encoding
```

**The second filter returned zero rows instead of two. No error** — nothing is wrong
with the encoding; it is simply an encoding of a different type. Measured, not
inferred.

The cache's own comment asserted *"that type is fixed for the plan's lifetime, so the
cached entry stays valid"*, which is true of one plan and false of a reused
expression. The key is now `{node, recvType}`.

This is the second silent wrong answer in fourteen steps, and the first that a user
could hit without writing anything unusual.

---

## 2. The predicted headline was refuted, and the refutation is the finding

`uerr.Annotate` mutates: `errors.As(err, &e)` then `e.WithOp(op).WithNode(node)`,
writing through the pointer and returning the original. 45 call sites. `expr.FirstErr`
hands out the pointer of an error parked in an expression tree, and `plan.Resolve`
annotates what it gets. So a construction-time error looked like it would be branded
by the first query to touch it — permanently, since `WithNode` is
innermost-wins-by-emptiness — and two goroutines sharing a hoisted erroring `Expr`
looked like an unsynchronised write.

**It is not, and the test that was written to prove it passed instead.** The reason
is a guard nobody had written down: `lazy.go`'s `nodes()` calls `e.Err()` and returns
**before building any plan node**, so a parked error never reaches `Resolve`. Every
expression-taking builder goes through it — `Select`, `Filter`, `Agg`, `GroupBy`,
`Join`, `JoinWhere`.

The tests stay, because the property is now pinned from the user's side: removing
that guard makes the contamination a failing test rather than a mysterious node
attribution in someone's error message. A negative result checked is worth more than
one assumed.

---

## 3. `wrap`'s two branches were swapped relative to their messages

`Reader.wrap` branched on `err == io.EOF` and produced *"ends mid-record"*, with a
comment saying that *"beats an opaque unexpected-EOF"*. Both halves were wrong:

- `io.EOF` reaches it when a read begins with **nothing left** — a field or record
  boundary. The file stopped cleanly, usually without its terminator. That is the one
  thing it is *not* mid-record.
- `io.ReadFull` and `binary.ReadUvarint` return **`io.ErrUnexpectedEOF`** when a read
  runs out partway — mid-varint, mid-name, mid-bitmap, mid-payload. That *is*
  mid-record, and it fell to the generic arm, producing verbatim the opaque error the
  comment claimed to have replaced.

Measured on a two-column fixture: **61 offsets produced the mid-record case and 33 the
clean stop.** Both now have their own message; neither is opaque.

---

## 4. Read-after-close: a panic was predicted, and something quieter was found

`Close` sets `r.f = nil` while both arms of `wrap` called `r.f.Name()`, so a read
after close was predicted to nil-deref.

What actually happens on a small file is worse. The 64 KiB `bufio` buffer still holds
the data, so **`Next` returned a full batch — four rows, two columns — from a closed
file, with no error.** The nil-deref is the *large*-file face of the same defect:
once the buffer is drained the refill returns `os.ErrClosed`, which is not EOF, so
`wrap` takes its second arm and panics.

Silent stale data or a panic, depending on buffer state. `Next` now refuses on a
closed reader, and the path is remembered separately from the file handle so every
message survives `Close`.

No consumer reads after close today — but `mergeOperator.Close` closes every run
including live ones, so a cancellation path is one refactor away.

---

## 5. The sweep, and what it did not find

A truncation sweep over **every byte offset** of a complete spill file, asserting per
offset: no panic, a non-nil error, and **`!errors.Is(err, io.EOF)`**.

The third is the load-bearing one. `extsort`, `extjoin` and `extagg` all end their
read loops on `errors.Is(err, io.EOF)`, so if `wrap` ever wrapped `io.EOF` a truncated
spill would stop being an error and become a **silent short read across the whole
engine**. Nothing else in the repository asserts it.

**It found no panics.** A prediction that unvalidated header lengths — `ncols`,
`rows`, buffer and string sizes — would panic on a corrupt file is *not* reachable by
truncation: truncation produces short reads, not wrong values. Reaching those needs
byte corruption, which is a different instrument. Recorded rather than claimed.

---

## 6. `uerr`, pinned at last

Its first tests cover what 129 files depend on:

- **The `Is`/`Unwrap` interaction that caused step 48's false negatives.** A
  `KindInternal` wrapper around a `KindSchema` cause matches **both** sentinels,
  which is what `errors.Is` means and is *not* what four tests assumed. The
  distinguishing assertion — `errors.As` for the top-level kind — is written down
  beside it.
- **`Annotate` mutates its argument**, asserted directly, so the day it stops the
  change is visible here rather than downstream.
- **`Kind.String()` had `default: return "internal"`.** The ninth instance of the
  enum hazard `ConcatMode.String()` documents for eight others, and subtler than the
  `JoinKind` one fixed in step 51: `KindInternal` is the **zero value**, so the
  default arm was legitimately reached and the omission read as deliberate. An eighth
  kind added without an entry would have told a user their resource limit was *"a bug
  in ursus"*. Now a table sized by a `kindCount` sentinel.
- **`NearMatches` panicked on a negative `n`** (`out[:n]`). One caller, a constant —
  but it is exported.
- **A false doc claim**: the package doc said the second signal was *"prefix
  containment"*. It is separator squashing, and always was.
- The budget tiers (the flips are at 6 and 9 runes), the deterministic stable-sort
  tie-break its own doc says golden files depend on, and the **OSA transposition**
  that makes `"pirce"` suggest `"price"` — which a simplification to plain
  Levenshtein would silently break while leaving its ten-line justification reading
  correctly.

---

## 7. Teeth

| tooth | result |
| --- | --- |
| drop `recvType` from the cache key | **bites** — 0 rows where 2 belong, on two of three frames |
| send mid-record truncation back to the generic arm | **bites** — exactly 61 offsets go opaque again |
| remove the closed-reader guard | **bites** |
| revert `Kind.String()` to the plausible default | **bites** — *"claims it is a bug in ursus"* |

And, as in step 52, an instrument caught **my own** mistake: `TestNearMatchesDoesNotFloodShortNames`
used `"cd"` as a far candidate, which shares the trailing `d` and is therefore one
edit from `"id"`. The engine was right and the fixture was wrong — the same mistake a
fixed budget would have hidden.

---

## 8. What is still open

- **Byte-level corruption of a spill file** is unexercised. Truncation cannot reach
  the unvalidated header lengths (`ncols`, `rows`, buffer and string sizes, the
  validity flag, payload size against row count); a corrupting instrument would, and
  `Schema.Field` is a bare slice index, so those are panics rather than errors.
  `ncols == 0` is the quiet one: it yields a *valid-looking* batch with N schema
  fields and zero columns.
- **`callCache` has no eviction.** Every `Call` node ever evaluated is retained for
  the process's life with its regex and its full probe set, and the key pins the node
  so the subtree cannot be collected. A lifetime question, not a correctness one.
- **`Writer.Write` returns a raw bufio error unwrapped** while the identical flush in
  `Close` wraps it — a disk-full during a spill escapes with no `KindIO` and no path.
  `Open`'s magic check likewise discards the read error, so an `EIO` reports as
  *"is not a spill file … please report it"*.
- **The step-51 goldens for `AsOfJoin`, `MergeSorted` and `HStack` are vacuous** for
  detecting the three missing projection-pushdown arms: every fixture is `(N/N cols)`,
  so landing all three would change zero bytes. My own instrument, blind in the way
  step 51 was written to attack.
- The standing list: 32 weak `errors.Is` accept-assertions, `Str().Join()`, the UDF
  follow-ons and a serial opt-out, `.list` set operations, `Time + Duration`
  overflowing 24h, `Expr`-level selection, `Pivot`, and a benchmark suite twenty
  commits stale on a machine that cannot run it.
