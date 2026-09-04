# Step 3 — as built

IO: CSV and Parquet, readers and writers. Authoritative where it disagrees with
[`step-2-as-built.md`](./step-2-as-built.md) and the vision docs.

**325 tests green** under `GOEXPERIMENT=simd` × `GODEBUG=simd={512,256,128,0}`,
with the experiment off, and under `-race`.

```go
err := ursus.ScanCSV("sales.csv").
    Filter(ursus.Col("qty").Gt(1)).
    Select(ursus.Col("region"), ursus.Col("qty")).
    SinkParquet(ctx, "sales.parquet")

df, err := ursus.ScanParquet("sales.parquet").
    GroupBy(ursus.Col("region")).
    Agg(ursus.Len().Alias("n")).
    Collect(ctx)
```

ursus can now read a file. Before this step the only source was `memsrc`, which
made it a demo.

---

## 1. The scan contract was unsound, and that had to be fixed first

`pushIntoScan` was wrong the moment any source set `Caps().Predicate`. Three
compounding faults, zero test coverage — because `memsrc` set the cap false, so
the branch had never executed.

| # | Fault |
| --- | --- |
| 1 | An inexact source got its `Filter` **deleted**. Parquet row-group pruning proves a group *cannot* match, never that a row *does*, so every surviving non-match reached the user. |
| 2 | `Scan.Predicate` was **never read**. Written by the rule, rendered by Explain, and dropped by `planScan` — `ScanSpec` had no predicate field. |
| 3 | Projection pushdown ran second and never consulted `s.Predicate`, so the scan was asked for column `a` while being told to filter on `c`. |

### `Caps.Predicate bool` → per-conjunct `Pushdown`

A bool is wrong by construction, in two independent ways:

- **Capability is per-conjunct.** `x > 5 AND lower(y) = 'z'` has one prunable
  conjunct and one that is not. `Filter.Preds` is already a conjunct slice
  precisely so they can move independently.
- **Honouring ≠ satisfying.** This is the one that returns wrong rows.

```go
const (
    Unsupported Pushdown = iota  // the ZERO VALUE
    Inexact                      // into the Scan AND kept in a Filter above
    Exact                        // into the Scan, Filter removed
)
```

`Unsupported` is the zero value deliberately: a short slice, a missing switch arm,
a source that forgets to answer — every accident costs a slower plan rather than a
wrong one. `plan.ClassifyPredicates` is the only thing the optimizer calls, and it
fails closed on a wrong-length slice or an out-of-enum value. Both guards were
verified to have teeth (removing the length check turns a malformed source into a
**panic**).

### Read set ≠ emit set

`Filter(c > 5).Select(a)` with an Exact conjunct leaves nothing in the plan
mentioning `c`, so projection pushdown narrows the scan to `[a]` — yet the source
cannot evaluate its predicate without reading `c`.

The resolution is **not** to widen `Projection`, which would change the scan's
output schema. `Projection` is the emit set; the source reads
`Projection ∪ RootNames(Predicate)` and emits only `Projection`.
`ScanSpec.ReadSet(full)` derives it once so no reader re-derives it wrong, and
Explain prints a `reads:` line whenever the two differ — a plan that says
`projection: [a] (1/3 cols)` while decoding two columns lies to whoever is
debugging it.

### `testsrc`: a source that lies on purpose

The dangerous path was unreachable from the test suite, so `internal/source/testsrc`
supplies the missing half of the contract: a decorator that claims Exact, Inexact,
or something invalid, and behaves accordingly — including badly.

`NewInexact` filters **nothing**, which looks lazy and is the point: the full set
is the largest legal superset and therefore the most adversarial conforming answer.
Reintroducing the original bug makes it return 7 rows against 4.

It is a decorator rather than a bespoke table so the same wrapper can go around a
real CSV or Parquet reader.

### Limit pushdown, added so `MaxRows` is not born dead

`Scan.MaxRows` + a `limitPushdown` rule. The lesson from `Scan.Predicate` was
precisely: do not add a field nothing reads.

The rule fires **only on a Limit directly above a Scan**, which rules out the
unsound case structurally rather than by a check someone has to remember:

```
Limit(2, Filter(qty > 3, Scan{INEXACT}))
```

If the limit reached the scan, the source would return 2 rows of its *superset*,
the Filter would reject both, and `Head(2)` would return nothing. The residual
Filter of an inexact pushdown is exactly what sits between them.

## 2. Decimal — one line, three guards

`dtype.Decimal(p,s)` was declarable and had nowhere to be stored: `Physical()` had
no case for it. **The entire gap was `case TypeDecimal: return Int128`** — after
which sort comparators, group keys, Min/Max, Take, Concat and `data.Values` all
work, because they already handled Int128.

It also closed a live inconsistency: `IsOrdered()` already returned true, so
`Sort(Col("price"))` passed plan-time validation and failed in the comparator.

The honest cost is three **narrowing** guards, for paths that became reachable and
would have been wrong by a factor of 10^scale — not imprecise, wrong:

| Guard | Without it |
| --- | --- |
| `Mul`/`Div`/`Mod` on Decimal | `12.34 * 12.34` = 152.2756 at scale 4, labelled scale 2, printed as `15227.56` |
| `Cast` to or from Decimal | `12.34` casts to Int64 as `1234`; Int64 `5` casts to Decimal(10,2) as `0.05` |
| `Sum`/`Mean` over Decimal | binds `Acc: Float64`, then asks an Int128 column for `[]float64` and dies as an internal error |

`Add` and `Sub` are allowed: equal scales mean the unscaled integers add directly.
`dtype.FormatDecimal` places the point, so a Decimal never renders as its storage.

## 3. A crash found by accident

Writing `GroupBy(Lit(1))` in a Decimal test panicked with
`index out of range [1] with length 1`.

A literal expression evaluates to a **one-row** column. Six operators evaluate
expressions against a batch; **three broadcast the result and three did not**, and
each of the three failed differently:

```
GroupBy(Lit(1))    panic
Sort(Lit(1))       "a bug in ursus" — a batch row-count mismatch
Filter(Lit(true))  a mask shorter than the batch it filters
```

Three call sites with the guard and three without is the running-schema walk again
(step 2's audit found four copies, two already diverged). All six now go through
one `physical.evalColumn`. Broadcasting stays at the operator boundary rather than
inside `Eval`, because the kernels already accept a length-1 operand and
broadcasting per node would materialise n copies of a literal at every level.

## 4. CSV

**Hand-rolled scanner, with a differential test against `encoding/csv`.** The
argument is structural, not a benchmark: `csv.Reader.Read` materialises every field
of every row before the caller gets a say, which makes projection pushdown
*unimplementable* — the reader would cost O(columns in file) instead of O(columns
in query). It also forces a row-major intermediate, and `csv.ParseError` cannot
carry the column name and expected type the error contract promises.

The risk is paid down the way the SIMD kernels pay theirs: `scanner_diff_test.go`
requires field-for-field agreement over a generated corpus, **including at buffer
sizes of 1–31 bytes**, which forces a refill inside every construct that has state.
The deviations (multi-byte comment prefix, record-size bound, buffer-aliased
fields) are listed in the test file rather than hidden.

The differential test immediately found one: `encoding/csv` normalises `\r\n` to
`\n` *inside quoted fields*. ursus now matches it — a reader that disagrees with
`encoding/csv` about the contents of a field is a worse trap than the rare field
that wants a literal CRLF.

**Projection is honoured by not parsing.** Splitting is unavoidable; materialising
is not. `wanted[i] < 0` means the field is walked past and never converted.

**No predicate pushdown, stated plainly rather than as a TODO.** A CSV file has no
statistics; every fact requires parsing, and once parsed the engine's own
`filterOp` applies the predicate correctly and faster. The one real optimization —
parse the filter columns, then the rest for survivors only — needs `physical.Eval`,
which a level-45 source cannot import. A second evaluator would be step 2's
four-copies defect with higher stakes. `Source` simply does not implement
`plan.PredicatePushdown`, so `ClassifyPredicates` answers Unsupported without this
type saying anything.

`ScanSpec.MaxRows` **is** honoured: `Head(10)` on a large file stops.

Two decisions worth knowing:

- **An empty field is null for every type except String**, where `""` is a value
  and conflating the two would be unrecoverable.
- **Multiple files are separate STREAMS**, not concatenated bytes. Each has its own
  header and its own skipped rows; gluing the bytes would turn every header after
  the first into a data row.

Errors name the **data row** and the **physical line** (they differ under a header,
embedded newlines, comments and skipped rows), the column by name, the text found
and the type expected — and are `KindValue`, not `KindInternal`. A stray comma in
someone's data must not send them to the issue tracker.

## 5. Parquet

**`parquet/file`, never `pqarrow`.** Measured on this tree: `go.mod` went from **7
modules to 14** — thrift, the four compression codecs, uuid, xxhash, x/sync. What
it did *not* gain is the point: pqarrow imports `arrow/flight` for one metadata
helper, which drags in grpc, protobuf, genproto, x/net and x/text — a gRPC stack in
a library whose job is to read a file.

Three arrow-go behaviours shaped the code. All three are reachable with files
arrow-go itself writes.

**An unsupported encoding PANICS.** `typed_encoder.go` ends in
`panic("unimplemented encoding …")` at eleven sites, reachable through `HasNext()`
inside `ReadBatch`. `BYTE_ARRAY` + `BYTE_STREAM_SPLIT` is legal Parquet 2.11 with
no arrow-go decoder, so a file someone else wrote can take down the process. The
encoding allowlist is checked at open time and is a **correctness** requirement,
not politeness.

**`Statistics().NumValues()` is the NON-NULL count.** So the obvious
"`NullCount == NumValues` means the group is all nulls" is *always false* and would
silently never prune. The row count lives on the column chunk.

**`HasMinMax()` is an OR over two independently-droppable fields.**
`ApplyStatSizeLimits` clears `Max` alone past 4096 bytes, and the read path sets
`hasMinMax = IsSetMax() || IsSetMin()`. A column with one 5 KB string reads back as
`HasMinMax() == true, Max == ""`. Verified live: **removing the `min > max` sanity
check makes the pruner skip all 200 matching rows.**

### The packed-values trap

`ReadBatch` returns two counts — `total` rows and `valuesRead` values — and the
values come back with **no gaps where the nulls are**:

```
rows      0     1     2     3
defLvls   1     0     1     1
values    a     b     c          <- b belongs at row 2, not row 1
```

Writing `values[i]` to row `i` shifts everything after the first null by an amount
that depends on the data, and is invisible on any fixture without nulls. The
round-trip test gives every column nulls in **different** positions, so a
consistent shift cannot look correct.

### Pruning

Classified **Inexact**, always. Conservative abstract interpretation over the
comparisons, `IsNull`/`IsNotNull`, `And`, `Or`: every function answers "could this
group contain a match?" and may answer *yes* when the truth is no, never *no* when
the truth is yes. A wrong yes costs a read; a wrong no deletes data.

Unsigned columns are **refused rather than reinterpreted**: Parquet stores a
UINT_64 in an INT64, and guessing the sort order wrong makes `[2^63, 2^63+10]` look
like `[-2^63, -2^63+10]` and prunes groups that match.

`Source.RowGroupStats()` exists because otherwise "the pruner works" is untestable:
an inexact pushdown leaves the Filter in place, so a pruner that skips nothing
returns exactly the same rows. The counter is the only thing that can see the
difference — 1 group read and 9 skipped, for a predicate matching the last one.

### Writing

Batches are **split at row-group boundaries**, not appended whole. Appending whole
meant `RowGroupRows` was only honoured where a batch happened to end: one 500-row
batch with a 50-row target produced a single 500-row group and the option silently
did nothing. Row-group size is the granularity of pruning, so a writer that ignores
it hands the reader a file it cannot skip anything in.

Every column is written **OPTIONAL** regardless of the field's `Nullable` flag. A
REQUIRED column can never hold a null again, and ursus's nullability is a property
of the batch in hand — a filter that happens to remove every null would otherwise
write a file that rejects the same data tomorrow.

`file.Writer.Close()` type-asserts its destination to `io.Closer` and closes it,
which would close a caller's `os.Stdout`. A `writeOnly` wrapper hides every method
but `Write`.

## 6. Honest gaps

- **Temporal types round-trip through neither format.** Date writes and reads;
  Time, Datetime and Duration are refused by both, rather than written as their
  storage integers into a file this package cannot read back.
- **No CSV predicate pushdown.** The seam is named; it needs a compiled predicate
  closure through `ScanSpec`.
- **Parquet pruning is row-group only.** No page index, no bloom filters.
- **No encryption**, no nested or repeated columns, no hive partitioning, no cloud
  object stores.
- **`Sum`/`Mean` over Decimal are refused**, pending a precision rule.
- **DECIMAL in a variable-length `BYTE_ARRAY` is refused**; INT32, INT64 and
  FIXED_LEN_BYTE_ARRAY are read.
- **A null String does not survive a CSV round trip by default.** CSV cannot
  distinguish `""` from absent. `WithNullValue`/`WithNullValues` with a sentinel on
  both sides makes it exact; the default losing it is a property of the format and
  is tested as such.
- Still deferred from step 2: `exec.Collect` peaks at 2× the result, `sortSink` at
  roughly 3×.

## 7. Verification

```bash
make test-all   # 4 SIMD widths + experiment-off
make race
make levels
```

The tests worth knowing about, because each pins something otherwise invisible:

| Test | What it would otherwise miss |
| --- | --- |
| `TestInexactScanKeepsTheFilter` | the step-2 soundness bug — 7 rows against 4 |
| `TestClassifyPredicatesFailsClosed` | a malformed source; without the guards, a **panic** |
| `TestLimitIsNotPushedThroughAnInexactFilter` | `Head(2)` returning nothing |
| `TestScannerMatchesEncodingCSV` | quote handling, at buffer sizes 1–31 |
| `TestParquetRoundTrip` | the packed-values shift, via per-column null positions |
| `TestParquetPruningIsSound` | pruning that changes the answer |
| `TestParquetTruncatedStatsAreNotTrusted` | 200 matching rows silently skipped |
| `TestParquetPruningActuallySkips` | a pruner that is sound because it prunes nothing |
| `TestLiteralExpressionsInEveryOperator` | `GroupBy(Lit(1))` panicking |
| `TestDecimalGuards` | numbers wrong by 10^scale |
| `TestSinkCSVIsAtomic`, `TestSinkParquetIsAtomic` | a half-written file where a complete one is expected |
