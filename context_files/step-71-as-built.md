# Step 71 — as built

**Read every file by name, and prune only what the statistics prove.** This is
`audit.md` §11 item 2: I1–I7, silent data corruption in ordinary file reads, with
I13 and I15 beside them.

Eight commits and this document.

Authoritative where it disagrees with the vision docs and [`design/`](./design/).

`make test-all` exit 0 (**100** package-ok lines = 20 packages × 5 SIMD configurations),
`make race` exit 0 (20), `make levels` and `go vet` clean in all three modules, PDS-H
SF=0.1 exit 0 with **22/22** matching the duckdb reference — each asserted on its own
exit code. No benchmarks. The golden plans did not move. The suite is at **2480**
tests.

---

## 1. Thirty wrong answers, measured first

The evidence commit checked every case against an answer worked out by hand, on one
thread and under a recover. One of the cases panics inside arrow-go, and a panic in
a worker goroutine cannot be recovered. **Thirty answered wrongly; four controls
answered correctly.**

| | today | |
| --- | --- | --- |
| Parquet files in another column order | **values swapped** — `a = 1, 2, 30, 40` | I1 |
| …with other names | read silently as the first file's | I1 |
| …with fewer columns | panic inside arrow-go | I1 |
| …REQUIRED, then OPTIONAL with a null | a null in a non-null column | I1 |
| a struct REQUIRED in one file, OPTIONAL in the next | `{a: null, b: 5}` reads as a **null struct** | I1 |
| a struct before the filtered column | `c == 2` → 0 rows | I2 |
| NaN under `!=` | the NaN row pruned | I3 |
| `""` beside a 5000-byte string | `>`, `>=`, `!=`, `==` lose the group | I4 |
| `IsNotNull` on a struct or list | groups of present rows pruned | I5 |
| a list of REQUIRED elements | `[]` reads as `[null]` | I6 |
| a REQUIRED list; a REQUIRED struct field | a null list; `ErrInternal` | I13 |
| CSV files in another header order | values swapped | I7 |
| a CSV part with no header | its first row lost | I7 |
| a byte-order mark on one CSV part | the column is named `"﻿a"` | I15 |

**Two of these were not in the audit and were found in the design.**

- **The struct's own validity** (the fourth I1 row). Whether a struct can be null was
  decided by the first file's flag, and every later file was read with it.
- **The required struct field.** Its `ErrInternal` was a *type* mismatch, not a level
  bug: the type declared `a: Int64!`, while `data.NewStruct` builds every field
  nullable.

## 2. The rule, which you chose

Several files are read **by column name**. Every file must have the same columns, of
the same types, in any order; anything else is refused with `ErrSchema`, naming both
files and the difference.

- **Parquet reads every footer at plan time**, so a mismatch is refused before any
  row is produced. A column is nullable if it is nullable in any file.
- **CSV headers are checked as each file opens.** CSV columns are always nullable, so
  nothing there depends on seeing every file first.

### Parquet: two layouts, not one

**At plan time, `Schema()` reconciles every footer.** At read time, each file's layout
is derived again from the footer actually being read, by `layoutFor`. That second
derivation is what makes the rewritten-after-planning case correct: a file rewritten
between `CollectSchema` and `Collect` with its columns reordered is read by name. A
file that now allows nulls the plan promised it would not is refused. The cost is a
second footer read per file, at plan time.

A struct's validity is now the file's own answer. The column plans are built in
**output** order. They were built in file order, and matched only because projection
pushdown happens to emit projections in source order; an internal test reaches that
directly, since no public query can.

### CSV: the first file's header is the reference

**Each later header is matched to the first file's header, by the names in the
files.**

- An identical header keeps the first file's mapping.
- The same names in another order get a mapping of their own.
- Anything else is refused: a missing, extra, renamed or repeated column, or no
  header at all.

The names are the ones written in the files, so a later file still matches the first
by what it says even when `WithColumnNames` has renamed the first file's columns.

**The byte-order mark is dropped where the stream is opened.** Without that, the new
header check would have refused a glob in which only some files carried one.

## 3. Pruning: four fixes and one rewritten doc

- **I2.** The pruner found statistics at a column's top-level index, but arrow-go
  counts leaves. It now resolves a column **by name**, through a `leafOf` function the
  reader supplies for the file being read. The same bug also took column `c`'s null
  count for a list's `IsNull`.
- **I3.** A float `!=` is never pruned. Statistics leave NaN out, and NaN != v is true.
  `!=` is the only arm NaN defeats; it makes every other comparison false.
- **I4.** **An empty string max is no bound.** arrow-go reads an absent max and a real
  `""` max identically, and the `min > max` check the header doc called "the defence"
  cannot fire when the min is also `""`. The doc is rewritten to say so. The cost: a
  group holding only `""` is never pruned.
- **I5.** Nested columns are not prunable. No leaf says whether a struct or list is
  itself null.

Two more traps were **recorded, not fixed**, as I23. arrow-go reports `HasNullCount()`
true for every statistic, and reads a one-sided int or float bound as 0. Only
third-party writers reach either, and the read API cannot detect them.

## 4. Lists and structs

`listCol` read every list as an optional list of optional elements. Now
`newListReader` derives the empty-list and null-element levels from the element's
own repetition, and cross-checks them against the list group's position from the
root. All four shapes read correctly, including across the 4096-level refill — a
5000-element list followed by `[]`.

Struct fields read from Parquet are always nullable. **That is a visible change:** a
struct from Parquet no longer shows a field as non-null.

## 5. Three instruments, and what they ended at

| instrument | at start | now |
| --- | --- | --- |
| `scan_byhand_test.go`, hand answers | 30 wrong | **0** |
| `pruning_differential_test.go`, 843 generated predicates | 106 | **0** |
| `multifile_test.go`, 108 queries across two sets of parts | 90 | **0** |

**The pruning differential derives its predicates from the data.**

- It compares every comparison at every distinct value of every flat column, just
  beyond each end, and at NaN, ±Inf, ±0, `""` and a string too long to keep, plus the
  flipped `lit op col` form and float32 literals against a Float32 column.
- Its unpruned side is **one full read filtered in memory**, which shares no Parquet
  read path with the pruned side. That was also twice as fast: 0.7 s instead of 1.3 s.
- **It must still prune.** Each class of predicate has to be seen skipping a row
  group, counted by the source. The first version had a single "float" class, so a
  pruner that stopped pruning Float64 alone passed it on Float32's skips; that tooth
  was silent. There is a class per width now.

**The multi-file differential has two sets of parts.** "Swapped" exchanges only
columns of the same type, which is the silent form: `a == 6` returned 0 rows, for one
— the part was read by position *and* pruned on the first file's layout. "Permuted"
moves a column of another type, which is the loud form.

**The two old pruning tests compared `String()`**, which renders ten rows of results
up to 500 long. They use `framesDiffer` now.

## 6. Teeth

Every patch was checked to have applied, and **every one ran against a green
baseline**, the lesson from step 70.

| reintroduce | fails |
| --- | --- |
| file 0's layout for every file | I1 ×4, the differential, rewritten-after-planning |
| footers validated lazily | seven plan-time refusals |
| nullability compared, not reconciled | both nullability cases and a control |
| nullability from file 0 only | required then optional |
| struct validity from the unified flag | struct optional then required |
| column plans in file order | `TestOpenHonoursAReorderedProjection` |
| no context check between footers | `TestSchemaStopsWhenTheContextIsCancelled` |
| top-level index for the leaf | I2, the pruning differential |
| nested prunable at plan time | `TestNestedColumnsAreNotPushedToTheScan` — **silent** until it existed |
| nested admitted at plan and read time | I5, the pruning differential |
| no empty-max rule / no float `!=` guard | I4 ×4 / I3, and the differential |
| Float64 or Float32 never pruned | its anti-vacuity floor — **silent** until split by width |
| the list levels reverted, three ways | I6, the long list, I13 |
| struct fields keep the file's nullability | I13, the nested fixture |
| no header check / not remapped / renamed names / skipped under `WithSchema` | the I7 cases, the differential |
| an empty part refused / no BOM strip | the empty-part control / I15 |

**Silent, and recorded rather than tested around:**

- **The `min > max` guard.** Once an empty max is no bound, arrow-go cannot produce
  min > max. It stays for writers that can.
- **The range guards in `statsFor` and `checkEncodings`.** No mapping produces an
  out-of-range leaf any more.
- **The list-shape cross-check.** No writer these tests use produces a list whose
  levels disagree with its shape.
- **Nested admitted at read time only.** The plan-time check stops them first. This is
  defence in depth: each check alone is silent, and removing both bites.

**One first attempt was a compile error, not a bite**, and it was re-aimed rather than
counted.

## 7. Behaviour changes

- A Parquet glob whose files differ in anything but column order is refused; an added
  column in a later file included.
- A missing or unreadable later Parquet file fails at plan time, not partway through.
- A CSV part with no header is refused, rather than losing its first row.
- Struct fields read from Parquet display as nullable.
- Every Parquet file's footer is read twice.

## 8. Still open

- **I24, a class.** `data.CheckNonNullable` is on only in test binaries, so a null in
  a column declared non-nullable is `ErrInternal` in the suite and **silent in
  production**. I1's nullability case and I13 both reached it, and were loud only
  because the tests ran them.
- **I23**, the two third-party statistics traps (§3).
- **The rest of `audit.md` §5**, I8–I22 apart from I13 and I15. That includes the CSV
  writer's UTC offset, Go-only syntax in CSV inference, corrupt-file panics and LZ4.
- **§11's order continues** with literal identity (O3), recovering panics in worker
  goroutines, and the float64 go-between.
- **Step 70's leftovers:** P1, W1, O4, O5, O6, O8b and O9.
- README debt.
