# Step 45 — as built

**`Str().Split()` ships, and with it the first kernel in ursus that BUILDS a list.**
Every List column the project has ever produced came out of a Parquet file. This is
the first way to make one in memory, which makes it as much a nested-types step as a
string one.

Nine `.str` methods in total, a missing arm in `kernel.NullColumn` that this step
made reachable and therefore closed, and a claim from step 6 that had been read as a
standing impossibility for seventeen steps after it stopped being one.

Authoritative where it disagrees with [`step-44-as-built.md`](./step-44-as-built.md),
the vision docs and [`design/`](./design/).

`make test-all` exit 0 (75 package-ok lines across five configurations),
`make race` exit 0 (15), `make levels` and `go vet` clean in all three modules —
each asserted on its own exit code. **PDS-H SF=0.1 validates 22/22** — a formality
here, since nothing on an existing path changed, but the step touched
`kernel.NullColumn`, which every outer join goes through.

---

## 1. What shipped

**List-producing**, the new shape: `Split(by)`, `SplitN(by, n)`, `ExtractAll(pattern)`.

**Scalar-out**, the cheap remainder: `PadStart`, `PadEnd`, `ZFill`,
`StripCharsStart`, `StripCharsEnd`, `EscapeRegex`.

The namespace goes from 20 of ~48 to 29. A scalar-out `.str` method is five edit
sites and the repo had done it eleven times, so those cost about fifteen lines each
once the first was written; `Split` is where the step actually is.

---

## 2. The first kernel that builds a list

`strToList` is `Split`, `SplitN` and `ExtractAll` in one function, for the reason
`listRebuild` is the seven reshaping list functions in one: they differ only in
which elements they produce, and nothing else.

What is new is the direction. Before this, `data.NewList` had four callers —
`takeList`, `concatList`, `listRebuild` and the Parquet reader — and three of them
reshape a list they were handed. Nothing constructed one from something that was not
already a list. `internal/expr/call.go` recorded where that seam was cut:

> A fifth family, keyed to a List receiver. These all reduce a list to a SCALAR:
> **nothing here builds a list**, which is the seam the step was cut on.

The kernel never names `List(String)`. `data.NewList` derives the element type from
the child column it is given, deliberately — *"A List whose declared element type
disagrees with the column actually holding the elements would be a lie no caller
could detect"* — so the type is stated exactly once, in `strCallOut`, and the two
agreeing is what `ResolveCall` means by being the single authority. Tooth 4 is that
sentence made executable.

It also gives the `.list` namespace its first non-Parquet fixture. `list_test.go`
builds lists by hand-writing definition and repetition levels, because *"ursus
cannot write nested Parquet, so the fixture has to come from the layer below"*.
`TestSplitFeedsTheListNamespace` chains `Split` into `.List().Len()`, `.Get()` and
`.Sort()` with no file anywhere.

---

## 3. Three cases that are not the same

```
"a,b"   ->  ["a", "b"]
""      ->  [""]        one EMPTY element, which is what strings.Split returns
NULL    ->  NULL        a null list, not an empty one
```

The last two are the whole difficulty, and `data.NewList`'s doc says why:

> **An empty list and a null list are not the same row.** Both leave the offset
> unmoved: `offs[i] == offs[i+1]` either way. Only the validity bit tells them apart.

Then `Explode` **erases** the distinction — *"A List column keeps them apart
carefully … and after this node they are the same row."* So it is observable only
between `Split` and `Explode`, and a test that checks the exploded output cannot see
it at all. `TestSplitDistinguishesEmptyFromNull` reads the list.

Both teeth bite, and neither would have been caught by a length check: an empty list
and a null list have identical offsets.

---

## 4. A hole opened and closed in the same step

`kernel.NullColumn` had no List arm. It fell through to `nullFixed` and returned
`cannot build a null column of List(String)` — unreachable, because no query could
produce a List column except a Parquet read, and a List operand that had to be null
could not arise.

`Split` makes it reachable. The arm is small and belongs to the step that made it
reachable rather than to whoever would have tripped over it.

**The first version of the test did not reach it**, and the tooth is what said so.
The test used a LEFT join, reasoning that unmatched rows null-pad the right columns
— but a left join gathers its right columns through `takeList` with a `NullIndex`,
which handles a missing row itself. Only a **right or full** join builds a *pad*, and
the pad is what `NullColumn` makes. Removing the arm changed nothing, because the
test was exercising a different mechanism than the one it named.

With a full join it fails with the exact message the arm exists to prevent. Same
shape as step 44's spill test that never spilled: a test that cannot reach the code
it names reads as coverage and is worth less than none.

---

## 5. `ZFill` is not `PadStart` with a zero

`-5` at width 4 is `-005`, not `0-05`: the sign stays in front of the padding. That
is the one thing separating the two functions, and it is why `ZFill` exists as a
function rather than as documentation on `PadStart`.

Padding counts **runes**, not bytes, for the reason `LenChars` exists — `é` padded
to width 4 takes three pad characters, not two.

---

## 6. Teeth

| tooth | result |
| --- | --- |
| a null input becomes an empty list rather than a null one | **bites** |
| an empty input becomes an empty list rather than `[""]` | **bites** |
| `strCallOut` says `String` where the kernel builds a `List` | **bites** — `batch column "parts" is List(String), schema says String` |
| `ExtractAll` is not wired to `CompilePattern` | **bites** — every list comes back empty |
| `NullColumn` has no List arm | **bites**, once the test reached it — §4 |

---

## 7. Verification

- The null/empty/one-element table, read as a list rather than through `Explode`.
- Batch sizes {1, 2, 3, 4, 8192}: the child offsets are relative to a batch's own
  child column, so concatenating two batches has to shift the second's. Correct at
  8192 and wrong at 2 is the signature, which is why `listns_test.go` sweeps.
- `Split` into `.List().Len()`, `.Get()`, `.Sort()` — the consuming half handling a
  list it did not read from a file.
- `Split` then `Filter` — `takeList`'s first non-Parquet list.
- `Split` then a full join — §4.
- `Split` then `GroupBy`, asserting a readable error rather than a panic:
  `GroupKeyEncoder` has no List arm, and that stays a decision about list equality
  rather than a missing arm.
- The scalar additions extend `TestStrMatchesStdlib`'s differential, and three of
  them have exact oracles: `strings.TrimLeft`, `strings.TrimRight`,
  `regexp.QuoteMeta`. That is why those three shipped and `ToTitlecase` did not —
  §8.

---

## 8. What was left out, and why

- **`ToTitlecase`.** Cheap to write and impossible to test honestly. `strings.Title`
  is deprecated, `golang.org/x/text` is not a dependency, so it would be a
  hand-rolled rune walk checked against hand-written expectations — no oracle, in a
  test whose entire value is being a differential. Named rather than shipped weakly.
- **`Join`/`Concat`** (the vertical string aggregation). Not a `.str` kernel: it
  reduces n rows to one, so it needs an `AggOp`, an accumulator and a `Merge`. It
  lands beside `implode`.
- **`Strptime`** and format-carrying `ToDate`/`ToDatetime`. `strCallOut` takes only
  the function; a format-carrying parse needs the output type to depend on an
  argument, which is `structCallOut`'s shape, plus a `dtype.ParseTemporal` that
  accepts a format at all.
- **`ExtractGroups`** — Struct-out, computed from the pattern's named groups.
- **`GroupKeyEncoder` for List** — §7.

---

## 9. Two stale claims retired

- **`step-6-as-built.md`** said List-returning `.str` functions were out because
  `data.Column` has no list payload, and named `Take`, `Concat`, `NullColumn` and
  `GroupKeyEncoder` as needing arms. The payload landed in step 28 and the first two
  arms in steps 29–33; this step did the third. It is struck through rather than
  deleted, because it read as a standing impossibility for seventeen steps after it
  stopped being one, and that is the more useful thing to record.
- **`MapJoin`'s refusal** hinted *"ursus has no List column layout yet"*. The
  refusal stays — `MapJoin` needs an imploding accumulator, not a layout — but it
  now says that instead.

---

## 10. What is still open

- **`implode`** is the highest-leverage thing adjacent to this. One `AggOp` and one
  accumulator would give `head`, `tail`, `slice`, `sort`, `unique`, `reverse` and
  `drop_nulls` **inside a group-by** by desugaring through the `.list` namespace
  that now has a producer — and it is also what `MapJoin` needs.
- **The published numbers are four steps stale.** REPORT.md is from step 40's tree;
  42, 43 and 44 are unpublished, including q21's rewrite. Worth a step of its own on
  a machine that is not carrying 11 GB of swap.
- The standing list: the parallel join's memory trade, CSV scanning, `Expr`-level
  selection in group-by, inline keys for `KeyTable`, the heap sampler being
  unconditional, `quantile`/`median` per-group storage, nested writing and
  `as_struct`, `.list` set operations, Map/Array, Pivot/Unpivot, SQL, cloud stores,
  join reordering.
