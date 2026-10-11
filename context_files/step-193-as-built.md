# Step 193 — as built

**P2 of `v0.6.1-scope.md`: a string conjunct on a dictionary column runs once per
dictionary value, in the reader.**

## 1. Why

In v0.6.0's profiles, comparing every row's string, as in
`l_shipmode IN ('AIR', 'AIR REG')`, was 21 to 22% of q19 and q12. That came on top
of copying every row's string so it could be compared. The columns hold a handful
of distinct values, and since step 192 the reader has each row's dictionary index.

## 2. What changed

**What reaches the reader.**

- **`dictable`** accepts a conjunct that reads one String or Binary column and is
  built only of nodes that compute a row from that row alone: a column, literals,
  `Binary`, `Unary`, `Alias`, `Cast`, `Cond` and `Call`. Every `Call` function is
  row-local. An aggregate, a window or a UDF is refused: a UDF might count the
  values it is shown, and would see each distinct value once.
- **`ClassifyPredicates`** reports such a predicate `Inexact`, so the planner hands
  it to the reader and keeps the Filter above.
- **The planner hands over a Filter's predicate whole,** as one `And`. `conjuncts`
  splits its top-level `And`s, and each half reading one column is judged. A row
  that fails a half fails the whole. The first version judged only whole
  predicates, so `kind == "num" & code != "3"`, over two columns, was judged by none.
- **`ScanSpec.Eval`** is new: the engine's evaluator, passed down by the physical
  scan, since the reader's level may not import the physical layer.

**In the reader** (`dictfilter.go`):

- **The table.** Per row group, each judged column's dictionary, plus one null, is
  evaluated once into a table of verdicts. A chunk without a dictionary still has
  the null's verdict, since judging a null needs no dictionary.
- **Two passes per batch** (`readBatch`):
  - the judged columns first `mark` their rows. Each row's index is looked up and a
    rejected row cleared in `keep`, with no string copied. A PLAIN page's values are
    copied aside, since the next page reuses its bytes;
  - then they `emit` only the rows every table kept, copying each value once;
  - the batch's other columns are read whole and taken to the kept rows by
    `kernel.Take` (`compact`).
- **What it can't judge, it keeps:** a present value on a PLAIN page, a column read
  through arrow-go, and a dictionary whose evaluation errs. The last turns that
  table off instead of failing the query. The Filter applies its conjuncts in turn
  (step 164), so a conjunct that errs on some value only ever sees the rows an
  earlier conjunct kept, while a dictionary holds every value.
- **Empty batches are skipped** by both readers.
- **No filtering under `MaxRows`:** the serial reader counts rows read against it,
  and would stop short of rows produced.

**Found on the way: batches too small.** The first version cut q12's CPU by 13% and
raised its wall time by 18%. The reader was sending q19's batches at a fourteenth of
their size, and every operator above paid its per-batch cost fourteen times as
often. `widen` now reads as many more rows as the filter dropped last time, up to
eight batches' worth, so the batches it sends stay near full.

**Found on the way: unpacking indices.** With the strings gone, the hybrid's
`unpack` was 9% of q12. Its dates are dictionary-encoded with about 2,500 values,
which is 12-bit indices, too wide for step 192's one-load-a-group path, so each
index took a load of its own. `unpack` now takes as many whole values as fit in one
64-bit load's 57 usable bits: four 12-bit indices, or 28 2-bit levels.

## 3. Tests and teeth

- **`TestDictionaryConjunctsDropOnlyFailingRows`** (internal, with a stand-in
  evaluator):
  - **the predicates:** `==`, `!=`, `IsNull`, `IsNotNull` with a second column's
    `==`, and one `And` over two columns as the planner hands it;
  - **the fixtures:** step 192's, dictionary V1, dictionary V2 with small pages, and
    PLAIN, each with no nulls, scattered nulls, or every row null;
  - **the reads:** on one thread and on four;
  - **the checks:** under a dictionary, exactly the passing rows are read. Under
    PLAIN, every passing row is read, in order, among a subsequence of the file's.
- **`TestStringConjunctsOverParquetAnswerAsInMemory`** (end to end): eleven filters
  over a file written by ursus in row groups of 3,000, each answering as the same
  frame in memory, on one thread and on four:
  - `==`, `!=`, `IsIn`, `Not(IsIn)`, `StartsWith`, a literal `Contains`, a regexp,
    `IsNull` and `IsNotNull`;
  - two columns' conjuncts;
  - a strict cast that fails on a value an earlier conjunct drops.
- **`TestDictableIsOneStringColumnRowByRow`** and **`TestWidenReadsWhatTheFilterDrops`.**

| tooth | result |
| --- | --- |
| a dictionary row's verdict ignored | **bites** |
| a null on a PLAIN page never judged | **bites** |
| the other columns not taken to the kept rows | **bites:** both tests |
| a kept dictionary value marked null | **bites:** both tests |
| a conjunct over two columns taken | **bites:** the `dictable` test |
| a UDF or an aggregate taken | **bites:** the `dictable` test |
| `And`s not split | **bites:** the one-`And` case |
| reads never widened | **bites:** the `widen` test |
| an evaluation error fails the query | silent |
| filtering under `MaxRows` | silent |

**The two silent teeth are a second line behind the planner:**

- **An evaluation error.** The planner never hands a scan a conjunct that can fail.
  Step 179's rule keeps a fallible one behind the conjuncts before it, and
  `Explain` of the cast case shows nothing pushed to the scan at all.
- **`MaxRows`.** A limit is never pushed into a scan below a Filter.

Both guards stay, as defence should either rule change.

## 4. Measured

Step 192's runner against this step's, alternated three rounds a query:

| query | CPU | wall |
| --- | --: | --: |
| q19 | 1,177 → 1,136 ms, −3% | 286 → 210 ms, **−27%** |
| q12 | 701 → 682 ms, −3% | 189 → 173 ms, −8% |
| q8 | 1,445 → 1,386 ms, −4% | 271 → 254 ms, −6% |
| q10 | 1,298 → 1,233 ms, −5% | 262 → 254 ms, −3% |
| q3 | 1,079 → 1,062 ms, −2% | 181 → 184 ms, +2% |
| q21 | 2,350 → 2,345 ms, ±0% | 545 → 549 ms, +1% |

**Against v0.6.0's profiles,** q12's CPU fell from 1.87 to 1.70 s and q19's from 3.05
to 2.58 s. Their filter's share fell from 21% to 11%, and from 22% to 9%.

**CPU falls less than the filter's share did,** because the other columns are still
decoded whole, then taken to the kept rows. Gathering only the kept rows of every
column is the next step down that road. It belongs with P3's gathering of a join's
output.

**Steps 192 and 193 against v0.6.0,** alternated, toward the scope's claim of 30%:

| query | CPU | wall |
| --- | --: | --: |
| q19 | 967 → 878 ms, −9% | 274 → 158 ms, **−42%** |
| q12 | 624 → 564 ms, −10% | 193 → 141 ms, **−27%** |

q19 is past the claim, and q12 is 3 points short of it.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
