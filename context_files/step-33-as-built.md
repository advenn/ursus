# Step 33 — as built

**The `.list` namespace, list → list.** The seven functions that reshape a list and
hand back a list: `Reverse`, `Head`, `Tail`, `Slice`, `Sort`/`SortDesc`, `Unique`,
`DropNulls`. With step 32's reducing half, `.list` is now complete apart from the
operations that take a second list.

Authoritative where it disagrees with [`step-32-as-built.md`](./step-32-as-built.md),
the vision docs and [`design/`](./design/).

**1453 test cases green** — 1448 before this step — under `GOEXPERIMENT=simd` ×
`GODEBUG=simd={512,256,128,0}`, with the experiment off, and under `-race`.
`make levels` and `go vet` clean in all three modules. PDS-H SF=0.1 validates
22/22 for ursus (155/155 across all engines).

```go
Col("tags").List().Sort().List().Head(3)   // the three smallest tags, still a list
```

---

## 1. Seven functions, one of them

Every one of these picks which of a row's element indices survive and in what
order; nothing else differs. So there is one `listRebuild` and seven closures, and
the closures are three to eight lines each:

```go
offs := make([]int32, 1, c.Len()+1)
var sel []int32
for i := range c.Len() {
    if start, end, ok := acc.Get(i); ok {
        sel = pick(start, end, sel)
    }
    offs = append(offs, int32(len(sel)))
}
child, err := Take(acc.Child(), sel)
return data.NewList(name, offs, child, c.Validity()), nil
```

**The element type never appears.** `Take` does the gather, so `List(String)`
works for exactly the reason `List(Int64)` does — `TestListSortOnStrings` is three
assertions and no new code path. `Sort` reuses `NewComparator`, the same one a
column-wide `Sort` uses, which is what makes a list of values and a column of the
same values order identically; `Unique` reuses `NewGroupKeyEncoder`, the same
equality `is_in` and `list.contains` use.

`Sort` sorts in place inside the selection being built (`out[base:]`) rather than
into a scratch slice, so there is nothing to allocate per row.

---

## 2. Two of the three planned teeth do not bite

The tests passed on the first run, which is the point at which the teeth are the
only thing separating "correct" from "untested". Two of the three the plan named
turned out to be no-ops, and both for reasons worth keeping.

### "Call `pick` for a null list too"

The plan's headline tooth, on the reasoning that a null list treated as an empty
one is the conflation this whole arc keeps defending. **Removing the `ok` guard
changes nothing** — every test still passes. A null row's element range is
genuinely empty (`ListAccessor.Get` returns the real range, which step 29's doc
insists on), so every `pick` here produces nothing from it anyway.

The line that actually defends the null list is `c.Validity()` being carried into
`NewList`. Replacing it with an all-set bitmap breaks **all seven functions at
once**, which is the tooth that does bite:

```
rev row 4:   valid = true, want false — a null list and an empty one are NOT the same
head row 4:  valid = true, want false
tail row 4:  ...
```

The guard stays, and its doc now says what it is for — a precondition for whatever
`pick` is written next ("you are only asked about lists that exist"), not the
defence. That is a judgment call against step 32's precedent of deleting provably
dead code; the difference is that step 32's post-mask did work at runtime whose
output was provably identical, where this skips work that would have no effect and
states a contract for the next closure.

### "Drop `Sort`'s stability"

Also a no-op, and this one is structural: **two elements of one column that compare
equal are the same value**, so on `List(Int64)` or `List(String)` no test can see
the difference between a stable and an unstable sort. Swapping `SortStableFunc` for
`SortFunc` passes everything.

It stops being unobservable the moment two *distinguishable* values compare equal,
and `-0.0` against `+0.0` already does — checked directly rather than assumed:

```
cmp(-0.0, +0.0) = 0
```

So the stability guarantee is real and will be observable on `List(Float64)`; it is
simply not observable on what can be read today. `SortStableFunc` stays, and the
doc says which case it is for instead of claiming the fixture's ties prove it.

### The five that do bite

| tooth | what fails |
| --- | --- |
| validity dropped in `NewList` | all seven, row 4 |
| `Unique` keeps the LAST occurrence | `uniq` row 1: `[1, 3, 2]`, want `[2, 1, 3]` |
| nulls placed last | `sort`/`desc` rows 2 and 5, **both directions** |
| the descending flag ignored | `desc` rows 0 and 1 |
| `Slice`'s negative offset read as 0 | `back`: `[10]`, want `[20]` |
| `Tail` measured from the front | `tail` rows 0 and 1 |

---

## 3. The fixture's shape is what makes the teeth possible

`Unique`'s tooth only bites because row 1 is `[2, 1, 3, 2]`. With the obvious
fixture — `[2, 1, 1]` — keeping the **first** occurrence and keeping the **last**
give the same answer, because the duplicate is adjacent. The duplicate has to be
non-adjacent, and the row has to have four elements, because any three-element row
with a non-adjacent duplicate is a palindrome and would hide `Reverse` instead.

The six rows and what each is for:

```
row 0  [3, 1, 2]         ordering
row 1  [2, 1, 3, 2]      a NON-adjacent duplicate, and a tie to sort
row 2  [1, null]         a null ELEMENT
row 3  []                empty
row 4  null              a null LIST
row 5  [null, null, 4]   a REPEATED null — one survives Unique, like any value
```

Reading it back goes through the column rather than the rendered string: a null
list and an empty list render the same and differ only in a validity bit, so
`strings.Contains(df.String(), ...)` — what step 29's test used — cannot tell them
apart at all.

**The fixture writer was generalised rather than copied.** `writeListFile` now
converts its plain slices and delegates to `writeElemLists`, which spells out the
def/rep levels. Its doc comment has listed `def 2 — null element` since step 29 and
the body had never emitted one; now it does, and both front doors share the one
level encoder.

---

## 4. The semantics, and which of them were decisions

| input | `Reverse` | `Head(2)` | `Sort` | `Unique` | `DropNulls` |
| --- | --- | --- | --- | --- | --- |
| `[3, 1, 2]` | `[2, 1, 3]` | `[3, 1]` | `[1, 2, 3]` | `[3, 1, 2]` | `[3, 1, 2]` |
| `[2, 1, 3, 2]` | `[2, 3, 1, 2]` | `[2, 1]` | `[1, 2, 2, 3]` | **`[2, 1, 3]`** | `[2, 1, 3, 2]` |
| `[1, null]` | `[null, 1]` | `[1, null]` | **`[null, 1]`** | `[1, null]` | **`[1]`** |
| `[]` | `[]` | `[]` | `[]` | `[]` | `[]` |
| `null` | **null** | **null** | **null** | **null** | **null** |

- **`Head(99)` returns the whole list**, and `Head(0)` an empty one. Lists are
  ragged; asking for three tags from rows that mostly have two is an ordinary
  question, the same reasoning `Get` uses for an index past the end. A **negative**
  count is refused (`KindValue`) because there is no reading to guess at.
- **`Slice`'s offset may be negative** and counts from the end; the clamping is
  `.str.slice`'s, deliberately, so the two slice operations in the library agree
  about their edges. A negative length is refused.
- **Nulls sort FIRST, in both directions.** That is `SortSpec`'s zero value and so
  the public `Asc()`'s default, and placement is applied before direction — which
  is why `SortDesc` does not silently move them. Both placements are defensible;
  this is the one ursus picked, and rows 2 and 5 pin it.
- **`Unique` keeps the first occurrence and leaves the order alone**, matching
  `distinctOp`'s rule for whole rows. Sorting as a side effect would make
  `Unique().Head(2)` mean something different from `Head(2)`.

Chaining reopens the namespace at each step —
`.List().Sort().List().Head(2).List().Len()` — because a method has to return
`Expr` for the rest of the language to reach it. polars reads the same way for the
same reason.

---

## 5. Verification

`listns_test.go` gained five tests: the full table at four batch sizes (a list
that spans batches is concatenated, and concatenation shifts the child offsets);
the clamping edges including `Head(0)`, `Head(99)`, `Slice(-2, 1)`, `Slice(9, 2)`
and `Slice(-99, 2)`; `Sort`/`SortDesc`/`Reverse` on `List(String)`; the chains,
which prove the output is a real List the reducing half can consume; and the three
refusals plus the non-List receiver.

`internal/expr/call.go`'s `listCallOut` is now the place a reader can see which
half a function belongs to — a fixed or element type for the reducing half, the
receiver's type unchanged for this one.

`git status bench/results/REPORT.md`: untouched.

---

## 6. What is still open

- **The set operations** — `set_union`, `set_intersection`, `set_difference`,
  `set_symmetric_difference` — and **`gather`**. All take a SECOND list column, so
  `listRebuild`'s one-child shape does not fit them.
- **`join(sep)`**, `to_struct`, and `list.eval` with `element()`, which the
  catalogue singles out as "powerful and non-obvious" and which needs a
  sub-expression evaluated per row.
- **`List(Bool)`, `List(Decimal)`, nested lists** — one reader accumulator each.
  Note that `Sort` and `Unique` would work on all three the day they can be read.
- **Struct**, **Array**, **Map**; **writing** a List column.

Unchanged: **Pivot/Unpivot**, SQL, cloud stores, plan serialization. And the
performance ledger, which this arc has not touched — **join reordering** (no cost
model; PDS-H is 10.7x polars at SF=1), **join parallelism** (8 threads slower than
1), **memory** (4.7x polars at SF=1), and the ~30% measurement noise floor that
makes all three hard to work on.
