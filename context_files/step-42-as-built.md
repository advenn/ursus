# Step 42 — as built

**The CSV reader uses more than one core now.** Conversion goes wide; scanning
still does not. It is worth about 1.15x end to end, which is a small number with a
clear ceiling — and the step's real result is the defect the teeth found on the way
there: the same malformed file reported a **different error depending on how many
threads the query was running on**.

Authoritative where it disagrees with [`step-41-as-built.md`](./step-41-as-built.md),
the vision docs and [`design/`](./design/).

`make test-all` exit 0 (75 package-ok lines across five configurations),
`make race` exit 0 (15), `make levels` and `go vet` clean in all three modules —
each asserted on its own exit code, not on a pipeline's.

---

## 1. The profile chose the design, which was the point of measuring first

Step 40 was designed for the wrong half of its problem and one baseline run would
have said so. So this step profiled before writing anything:

```
inside the reader
  appendRecord  (conversion)   0.82 s cum
  readRecord    (scanning)     0.30 s cum
```

**Conversion is about 2.7x scanning**, so the plan's §1 — convert columns in
parallel — and not §2, which splits the file by byte range.

That choice also disposes of the plan's first tooth. *"Feed a worker a block that
starts inside a quoted field"* is the failure mode of a byte-range split, and this
design never splits by byte range, so the tooth **cannot be applied**. That is not
a gap in the testing; it is the reason §1 was preferred.

---

## 2. What changed

`colStage` holds one column's raw field bytes for one batch — `chars`, `offs`,
`null`, the same shape as a String column. The record loop copies into it and
parses nothing:

```
scan record  ->  stageRecord   (copy the wanted fields, serial)
   ... once the batch is full ...
             ->  convertAll    (one goroutine per column, bounded by threads)
```

The axis is **columns, not row ranges**, and that is what makes it cheap: two
columns never touch the same builder, so there is nothing to synchronise during
the conversion and nothing to merge after it.

`source.ScanSpec` gained a `Threads` field. The thread count had never reached a
source before — `WithThreads` shaped the operator tree above the scan and stopped
there.

**The ceiling is the column count.** Seven columns on the file this was measured
on, sixteen on a wide one, two on a narrow one. That limit is real and was named
in the plan before any of this was written.

---

## 3. Two record paths, because staging is not free

The first version staged unconditionally, and the serial path got **slower** —
3.977 s against 4.596 s on one matched pair, about 15%. The copy into the stage is
pure cost when there is nobody to hand the work to.

So `threads <= 1` keeps the original inline `appendRecord`, converting each field
as it is scanned, and only `threads > 1` stages. `WithThreads(1)` is documented as
reproducing the serial operator tree exactly, and now it reproduces the serial
*reader* exactly too.

Two paths is real duplication and it is the thing most likely to rot. `raggedError`
was factored out so both raise the identical record-shape error, and §4 is what
happened to the error they did not share.

---

## 4. The defect: the same file, two answers

The two paths chose **different errors** for the same malformed file.

The serial path converts row by row and left to right, so it stops at the earliest
bad **row**. The first parallel version converted whole columns and reported the
first failing **column** in file order. Those are the same answer only when the bad
values share a row.

A file where they do not — column `b` bad on row 11, column `a` bad on row 251:

```
threads=1:  row 11  (line 12),  column "b": cannot read "badB" as Int64
threads=8:  row 251 (line 252), column "a": cannot read "badA" as Int64
```

**The reproducible error message is most of what this reader is for.** A file that
reports a different first error depending on an unrelated performance knob is worse
than a slow reader.

The fix: `convert` returns the batch-relative row it failed on, and `convertAll`
picks the smallest row, leftmost column on ties — the serial path's answer by
construction rather than by coincidence. `TestScanCSVErrorsAreIdenticalAcrossThreads`
now pins it, including this cross-row case.

This was found by writing a tooth, not by the suite. The suite passed.

---

## 5. Measurement

Matched pairs, alternating before and after, on the 10M-row seven-aggregate CSV
query — the one slow enough to read directly, which is the luxury this target has
and the join no longer does:

| | before | after | |
| --- | --: | --: | --: |
| **8 threads** | 4.180 s / 3.993 | **3.737 / 3.385** | **~1.15x** |
| 1 thread | 4.498 / 3.956 | 4.174 / 3.938 | unchanged |

The serial row is the point of §3: it takes byte-for-byte the same path it did
before, and it measures that way.

`bench/micro`, 1M rows × 7 columns, medians of three, two matched pairs (ms/op):

| | before | after |
| --- | --: | --: |
| `BenchmarkScanCSV` | 400 / 445 | **354 / 415** |
| `BenchmarkScanCSVTypedSchema` | 396 / 416 | 444 / 404 |

The first says 1.07–1.13x. **The second says nothing** — it flips sign between
adjacent pairs, and the same binary spans 351 to 451 ms across these runs. That is
the noise floor that has shaped every performance step since 23, and it is why the
10M-row query is the number quoted above it.

Allocation is the honest cost: **25,307 → 27,381 allocations (+8%)** and 241 → 246
MB (+2%), the stage buffers. Small, permanent, and only paid on the parallel path.

**Why 1.15x and not more.** Three reasons, in order of size: the ceiling is the
column count; scanning stays serial and is now the larger half of the reader; and
the reader is only part of the query. This step moved the smaller half of one
stage, and 1.15x is what that looks like.

---

## 6. Teeth

| tooth | result |
| --- | --- |
| a block starting inside a quoted field | **not applicable** — §1 has no byte-range split, which is why it was chosen |
| the stage keeps the scanner's slice instead of copying it | **bites** — the batch holds another record's bytes; `TestStagedFieldsAreCopiedNotAliased` |
| report the first failing **column** in file order | **bites** — §4, and this is the version that shipped first |
| report whichever goroutine failed first | **bites** — same test |
| index the stage by file position instead of output position | **bites** — a projection puts one column's bytes in another's builder |
| `ScanSpec.Threads` ignored, reader always serial | **bites** in 3 s — `TestThreadsReachTheConverter` |

The last one is the tooth step 36 needed and did not have: it shipped a fleet of
join workers where only one ever ran, and every output test passed because a serial
run produces exactly the right answer. So the distribution is **observed** here
rather than inferred — four columns block on a barrier and the test can only
collect all four if all four are in flight.

**The tooth's first version hung the package for ten minutes** instead of failing
at its three-second deadline. The barrier builder blocked sending into a channel
that a failed test had stopped draining, so the reader deadlocked inside `Next`
while holding its mutex, and the deferred `Close` waited on that mutex forever. The
send is non-blocking now. A tooth that hangs is nearly as useless as one that does
not bite.

---

## 7. Verification

- The differential tests are the shape this step needed: the same file read at
  1, 2, 3, 8 and 16 threads, compared frame to frame with `AssertFrameEqual`,
  which checks row order — a CSV's row order is its data.
- The fixture is built to be hostile: quoted fields holding the delimiter, an
  embedded newline and an escaped quote, so the physical line and the logical
  record disagree; nulls inside a numeric column; and an `ord` column carrying the
  input order so an interleaved frame is visible rather than merely suspected.
- Degenerate shapes on both paths — no trailing newline, header only, header with
  no newline, a trailing blank line, an empty file.
- Errors on both paths — ragged with and without `TruncateRaggedLines`, two bad
  values in one row, two bad values in different rows.
- `scanner_diff_test.go`'s differential against `encoding/csv` is untouched and
  still green: the scanner did not change.
- **PDS-H SF=0.1, ursus: 22/22 answers match the duckdb reference.**
- **h2o in CSV mode: 5/5 match** — gb1, gb3, gb7, j1, j4. Five of fifteen, not all
  of them, and the reason belongs here rather than in a footnote: the machine had
  4 GB free with 11 GB already in swap, and step 41 measured gb5 and gb10 at up to
  8.75 GB. The five chosen read **every CSV file the suite has** — the 543 MB
  group-by file and both ~500 MB join files — so the reader is covered end to end
  against a real file; the ten skipped differ in what they aggregate, not in what
  they read. A subset reported as a subset.
- `timings.csv` is gitignored and **`REPORT.md` was not regenerated**: 1.15x on one
  path does not justify republishing a suite measured on a machine in this state.

---

## 8. What is still open

- **Scanning is now the larger half of the reader and is still serial.** The
  remaining designs are the plan's §2 (byte-range blocks, with the quoted-field
  boundary problem it brings) and SIMD delimiter search. Neither is small.
- **Type inference** is a separate serial pass and was out of scope; nothing here
  measured what it costs on a wide file.
- **The CSV writer**, still unmeasured by anyone.
- The standing list: `JoinWhere`, the parallel join's memory trade (28–47% faster,
  30–45% heavier, still not a decision), inline keys for `KeyTable`, the heap
  sampler being unconditional, `quantile`/`median` per-group storage, nested
  writing and `as_struct`, `.list` set operations, Map/Array, Pivot/Unpivot, SQL,
  cloud stores, join reordering.
