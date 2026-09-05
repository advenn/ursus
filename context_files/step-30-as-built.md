# Step 30 — as built

**`Explode` — the operation that makes a List column useful rather than merely
readable.** After it, every kernel, aggregate and join that already exists applies
to the elements, with none of the 37-method `.list` namespace built first.

Authoritative where it disagrees with [`step-29-as-built.md`](./step-29-as-built.md),
the vision docs and [`design/`](./design/).

**1437 test cases green** — 1426 before this step — under `GOEXPERIMENT=simd` ×
`GODEBUG=simd={512,256,128,0}`, with the experiment off, and under `-race`.
`make levels` and `go vet` clean in all three modules. PDS-H SF=0.1 still
validates 22/22.

```
  id | tags        ->   id | tags
   1 | [10, 11]          1 | 10
   2 | []                1 | 11
   3 | null              2 | null
   4 | [12]              3 | null
                         4 | 12
```

---

## 1. An empty list produces a row

The two cases that decide the whole design are the middle ones. An empty list and
a null list each produce **one row holding a null**, rather than vanishing.

Vanishing is the other plausible rule and it is wrong twice over. It would drop
rows silently — nothing errors, and the height is only wrong to someone who checks
it — and it would make exploding two columns together incoherent, since the two
would disagree about how many rows a pair of empty lists gives. Polars makes the
same choice.

**The consequence is worth stating because it looks like a bug from either side:
explode ERASES the empty-versus-null distinction.** Step 29 worked to keep those
apart — they differ only in the validity bit, and `data.NewList`'s doc is mostly
about not confusing them — and after this node they are the same row. That is
correct. The distinction is a property of a *list*, and explode is what consumes
the list.

---

## 2. It is a BatchOp, which is most of the design

`BatchOp` is "a stateless, order-independent transform of one batch into one
batch", and a differing row count is already inside that contract because `Filter`
is a BatchOp and drops rows. So explode needed **no new operator shape** — no
`Sink`, no breaker, one dispatch arm — and it rides `parallelOp` unchanged.

The transform is one index build and two gathers:

```
rowSel    one entry per OUTPUT row, holding the input row it came from
childSel  one entry per output row, holding the element it came from
```

Every other column is `Take(col, rowSel)`; the exploded column is
`Take(child, childSel)`. **`NullIndex` does the awkward part**: an empty or null
list appends `NullIndex`, and `Take` already turns that into a null, so the two
special cases need no special path. The only place they are handled at all is one
`s == e` test.

---

## 3. The optimizer needed no changes, and that was checked

Pushing a predicate *below* an explode would be wrong — down there the column is
still a list, and the predicate is written against the element type. All three
pushdown rules already default to leaving an unrecognised node alone:

- predicate pushdown's default is `pushClosed`, whose doc says outright it is "the
  fail-closed treatment ... the default arm for a node type this rule does not
  recognise";
- projection pushdown's is "conservative: require everything this node's children
  can produce";
- limit pushdown's returns the node untouched.

So a new node is safe by construction. `TestExplodeIsBelowAFilter` pins it anyway,
because "it happens to work today" and "it is guaranteed" are different claims.

---

## 4. Several columns explode together

Zipped, not multiplied. Exploding twice in sequence would give the cross product
where the caller means the pairing, which is why it is one operation over N
columns rather than N operations.

That only means anything if the lengths line up, so a row where two named columns
disagree is an **error naming the row**. Truncating loses data and padding invents
it; neither is a guess this can make for the caller.

`Explode()` with no columns is also refused rather than defaulting to "every list
column", because that would zip columns nobody said were parallel.

---

## 5. The expression form is refused, reusing an existing story

`Col("tags").Explode()` fails at build time with a hint pointing at
`lf.Explode("tags")` — the pattern `fill.go` already established for `DropNulls`:
an expression produces one value per input row, so it cannot change the height.

Reusing that refusal rather than inventing a second one matters: a caller who
reaches for the expression form has made the *height* mistake, not a list mistake,
and it is the same mistake `DropNulls` already documents. Refused at build time so
`Explain` and `CollectSchema` fail too, which is the reason `fill.go` gives.

---

## 6. Teeth

**Empty and null lists vanish.** `got 3 rows, want 5` — and the rendered frame
shows exactly the two missing rows. This is the design decision from §1, so it is
the teeth check that matters most.

**Gather the elements with `rowSel` instead of `childSel`.** Panics — the row
indices run past the child's length. Loud rather than silent here only because the
fixture's child is shorter than its row count; with a longer child it would pair
elements with the wrong rows, which is why the test checks values and not just the
height.

**Drop the multi-column length check.** `exploding columns of unequal length must
fail, not truncate`.

---

## 7. Verification

`explode_test.go`: the three row shapes at four batch sizes with values checked
per row; an all-empty column keeping its height rather than going to zero;
explode feeding `GroupBy`/`Count`/`Sort` — the actual point, that the rest of the
engine now applies; the four refusals; the filter-above-explode optimizer pin; and
two-column zip plus the unequal-length error.

Plus the full gate and **PDS-H SF=0.1 revalidated 22/22**, since this adds a plan
node every optimizer rule walks.

`git status bench/results/REPORT.md`: untouched.

---

## 8. What is still open

Lists are now genuinely usable for the common workflow: read, explode, aggregate.
What remains in the arc:

1. **`List(String)`** — `listCol` handles fixed-width elements only, so a column
   of string tags still cannot be read. Independent of explode, and the most
   common real shape after `List(Int64)`.
2. **The `.list` namespace** (37 methods) — `len`, `get`, `contains`, `sum`,
   `unique`, the set operations. Each adds one capability where explode added the
   whole engine, which is why it went second.
3. **Struct**, which needs N children rather than one, then **Array** and **Map**.
4. **Writing** a List column, to Parquet or CSV.

Unchanged: **Pivot/Unpivot**, SQL, cloud stores, plan serialization. And the
performance ledger, untouched by this arc — **join reordering** (no cost model
exists; PDS-H is 10.7x polars at SF=1, worst on the multi-way joins), **join
parallelism** (8 threads slower than 1), **memory** (the most memory-hungry engine
in the field, 4.7x polars at SF=1), and the ~30% measurement noise floor that
makes all three hard to work on.
