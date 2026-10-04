# Step 82 — as built

**The spill claim, made true of the operators people hit.** This is `v0.3-scope.md`
§2.4 and §3 item 6. The README's first sentence promised "streaming execution that
spills to disk rather than falling over"; the scope document measured that as half
true. Item 6 offered a choice between making `unique` and `over` spill and softening
the sentence. This step did the first, then made the sentence and every hint say
exactly which operators spill and which fail.

It was the ninth step of the road to 0.3.

Eight commits and this document.

Authoritative where it disagrees with the vision docs and [`design/`](./design/).

Each of these was asserted on its own exit code:

- `make test-all` exit 0, with **105** package-ok lines: 21 packages × 5 SIMD
  configurations.
- `make race` exit 0 (21).
- `make levels` and `go vet` clean in all three modules.
- PDS-H SF=0.1 exit 0, with **22/22** matching the duckdb reference.

No benchmarks: every spill test runs a few thousand rows under a limit of a few KiB.

**The suite is 2867 passing tests and subtests** (2835 at step 81).

---

## 1. What was measured

The setting: 4000 rows, a batch size of 128 and a 16 KiB limit.

| query | before |
| --- | --- |
| sort / group_by / join, flat columns | spill, correctly |
| **the same, with a List or Struct column anywhere in the frame** | **fail**: `spill: cannot spill a List(Int64) column`. `putDType` had no nested arm, so the claim was false for every frame carrying one, whatever its keys. |
| `Unique("k")`, `Unique()` | fail: `unique: memory limit … exceeded` |
| `x.Sum().Over(k)`, two partitionings, `Over()` | fail: `over: memory limit … exceeded`, the same sentence for all three |
| `MaintainOrder` group_by over a column named `__ord`, under a limit | fails: `duplicate column "__ord"`. The spill schema's ordinal collided with it. |

The accounting was low too:

- **Window:** the ledger showed 469 KB for 320 KB of input. `Finish` concatenates
  everything it holds into a second copy, uncharged, so the true peak was about
  790 KB.
- **As-of join:** its `Probe` made the same copy and built a bucket map, and charged
  neither.
- **Key tables:** the group-by and window sinks never charged theirs. Only the join
  did.
- **The hint:** the resource error listed five operators that fail. It missed
  `join_asof`, `merge_sorted`, `rolling` and `group_by_dynamic`, which also fail.

## 2. Evidence first

**E1 is `spill_byhand_test.go`: 27 cases, 25 wrong.** Every spilled case runs far
under what it holds, must have reached disk (`Spills > 0`), and must answer as it
does with no limit, order included. The one exception is the join, whose order under
a limit is documented as unspecified. The cases:

- nested columns through a sort, a `MaintainOrder` group_by and a join;
- `unique` by key and by whole row, over NaN and null keys, carrying a List and a
  Struct, stopping early under a `Head`, and over a column named `__ord`;
- `over` as a sum, as a `Rank` and a `CumSum` ordered descending, over two
  partitionings, as `First` and `Last`, over NaN and null partition keys, carrying
  nested columns, and over `__ord`;
- the two walls that stay: no partition keys, and one partition larger than the
  limit;
- the window's and the as-of join's peaks against their copies, and the group-by's
  against its key table;
- the hint's words;
- the spill directory is left empty after a full read and after an early break.

Two more cases came later, from silent teeth (§4).

**E2 is `TestSpillingAnswersAsInMemory`: 150 routes, 81 wrong.**

- **Payloads:** step 81's 23 element types, plus a List and a Struct, as a payload
  column.
- **Shapes:** a sort, a `MaintainOrder` group_by, a join, `Unique("k")`, windows over
  two partitionings, and an ordered window.
- **Each route:** run under 8 KiB, must spill, and must equal the unlimited answer.

## 3. The fixes

1. **The spill format writes List and Struct**, recursively.
   - **List:** validity, then offsets rebased to the elements the column addresses,
     then the element column, sliced to them. A sliced List keeps its parent's whole
     element column, as a sliced String keeps its characters.
   - **Struct:** validity, then each field.
   - **Array and Categorical stay refused**, since neither has a column
     representation.
2. **`unique` spills, in input order** (`extdistinct.go`).
   - **The freeze.** Past the limit it stops admitting keys at a batch boundary. A
     row whose key it holds is dropped; any other row is routed, stamped with its
     input ordinal, to one of sixteen files by the radix of its key. These are
     extagg's radix, writers and depth limit.
   - **The replay.** At the end of the input, each file is deduplicated by a
     `distinctOp` a level deeper, and the survivors are k-way merged on the ordinal.
     This is extsort's `mergeOperator`, now behind `mergeByOrdinal`.
   - **Why the answer is exact, order included:** every row emitted before the freeze
     precedes every row routed after it, and each level's output is in ordinal order.
   - It still streams until the freeze, so `Unique().Head(5)` stops early.
3. **A partitioned `over` spills** (`extwindow.go`).
   - **Routing.** Past the limit, the retained rows and every later one are routed by
     the radix of the partition key, stamped with their ordinal. Each file holds
     whole partitions, in input order.
   - **Sub-sinks.** Each file gets a fresh sink a level deeper, so the group-id remap
     that `windowSink.Merge` refuses is never needed.
   - **Several partitionings** take one pass each: route by the first, compute its
     windows, merge on the ordinal; route that by the next, and so on.
   - **The factory.** `planWindow`'s spec building became `newWindowSink`, so a pass
     can build fresh sinks.
   - **Still refused, now saying which:** a window with no partition keys, and one
     partition larger than the limit.
   - **Coalescing.** Measuring found that a route writes a sixteenth of a batch at a
     time. Held as read, a partition of 24 rows cost 17 buffers' padding per column
     and refused an 8 KiB limit. A file's fragments are now joined back into batches
     before a sub-sink holds them.
4. **What an operator holds is charged.**
   - **The window and the as-of join** reserve, as they retain, the copy they will
     make: every distinct allocation once, through a `BufferSet`, so a buffer that
     many slices share is not reserved many times.
   - **The as-of join** also charges its bucket map, and checks the limit after
     building it.
   - **The group-by and window sinks** charge their key tables, at the group-by's
     Consume and Merge sites both.
5. **The ordinal column's name** is chosen not to collide with the input or a
   window's output (`ordinalName`). The group-by, unique and the window share it.
6. **The words.**
   - **The hint** now says that sort, group_by, join, unique and a partitioned over
     spill. Its list of operators that fail is reverse, hstack, tail, join_asof,
     merge_sorted, rolling, group_by_dynamic and an unpartitioned over.
   - **`holdsHint`** gains `over`, `join_asof` and `merge_sorted`.
   - **`WithMemoryLimit`'s doc** says unique and windows keep their order when they
     spill.
   - **The README's first sentence and its Execution row** say the same.

**Three test corrections rode with the fixes:**

- **E2's ordered shape** took `First` over an ordered window, which ursus refuses in
  memory too. The spilled run's error hid that, because the route collected the
  spilled answer first. Routes now compute the in-memory answer first and stop on
  its error, and the shape takes `Shift`.
- **`k % 7` partitions** are genuinely larger than 8 KiB, so the sweep partitions by
  `k % 50` instead. The two by-hand cases over `k % 7` and over `f` run at a limit
  that holds one partition but not the input.
- **`TestMemoryLimitDisablesParallelAggregation`** compared an unordered group-by
  under a limit with the serial answer row for row. That order matched only while
  the uncharged key table let the freeze land after the last new key. It now
  compares content, as `WithMemoryLimit` documents.

## 4. Teeth

Every patch was checked to have applied, and every one ran against a green baseline.
**All 19 bite.** One was re-aimed, two silent ones gained cases, and one needed both
of its sites patched:

| reintroduce | fails |
| --- | --- |
| a Struct's validity not written | the spill round trip, E2, five nested E1 cases |
| a List's element column sliced from 0 | the spill package's nested and sliced-list round trips — re-aimed after a first patch left a variable unused. The E1 and E2 lists never reach a spill sliced: routing gathers rows first, which starts every element column at 0. |
| unique: routed rows not checked against the held keys / merged on the key, not the ordinal / a sub-level keeping duplicates | E2 and four to six unique cases, each |
| unique / over: the spill directory not removed | the cleanup case, each |
| over: pass 2 routed by the first partitioning / a pass computing every window | E2 and the two-partitionings case |
| over: the unpartitioned refusal dropped | the no-keys case |
| over: ordinals restarting each batch | E2 and eight over cases |
| over: fragments not coalesced | E2 and `TestBudgetErrorNamesTheOperator/over_spills` |
| the window's / the as-of join's reservation not charged | its peak case; for as-of, its refusal case too |
| the window's key table not charged | **silent at first**, because no case held enough partition keys to see it; now the 20000-key case |
| no Check after the as-of buckets | **silent at first**, because the reserved copy refused every as-of case earlier; now the case run at its own peak less a byte |
| the group-by's key table not charged | **silent at first**: the patch hit Consume only, and an unlimited group-by runs parallel and charges the table again at Merge. Both sites together fail the key-table case. |
| the ordinal name fixed at `__ord` | the three `__ord` cases |
| the old hint | the words case |

## 5. Behaviour changes

- **`Unique` and a window with partition keys spill under `WithMemoryLimit`** rather
  than failing. Their answers are the in-memory ones, order included.
- **Sort, group-by and join spill frames that carry List and Struct columns.**
- **Windows and as-of joins are charged their second copy.**
  - A window now spills at about half the input it used to.
  - An as-of join, or a window with no partition keys, now refuses at about half its
    old size. That was its true peak all along.
- **Group-bys are charged their key table**, so one under a limit may freeze sooner.
  The row order of an unordered group-by under a limit, which is documented as
  depending on the limit, moves with it.
- **A column named `__ord`** no longer breaks a spilling `MaintainOrder` group-by.
- **The resource error's hints, `WithMemoryLimit`'s doc and the README** name what
  spills and what fails.

## 6. Still open

- **Operators that still fail past the limit:** reverse, hstack, tail (by its N),
  join_asof, merge_sorted, rolling, group_by_dynamic, an unpartitioned window, a
  cross join, and quantile-like aggregates over a few huge groups. Each names itself.
- **Spill files written in fragments:** the group-by and the join read theirs back
  as written. Only the window coalesces, because only it retains what it reads.
- **`MaintainOrder` group-by** materialises its spilled result to sort it on the
  ordinal, under `Retain` rather than `Check`. `mergeByOrdinal` is the tool that
  would stream it.
- **The k-way merge's fan-in** is unbounded (`mergeOperator`'s documented
  runs × batch term). Unique and over merge sixteen files per level, so this stays
  small for them.
- **The rest of the road to 0.3:** object stores, or their deferral, then release
  readiness.
