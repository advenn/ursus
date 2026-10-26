# Step 378 — as built

**Text marked and emitted by the rows that matter.** R3 of `v0.7-scope.md`, its text
half.

## 1. How

After step 377 the reader's own work on PDS-H's text-filtered queries was mostly
marking and emitting text: `pageStrings.mark` 7 to 9% of q12's, q19's and q10's CPU
at SF=1, `emit` 5 to 9%, the dictionary indices' unpacking 3 to 9%.

- **`mark`'s common page** (`dictfilter.go`): a dictionary page whose every row is
  present, as every page of PDS-H's is, has each row's pending entry its index,
  copied, and each verdict its index's, looked up, in two plain loops, where each row
  went through a branch for its level, an append and a lookup that allowed a null.
  A page with nulls, and a PLAIN page, are marked as before.
- **`emit` by the kept rows' positions:** `readBatch` has them since step 377, so
  `emit` sizes and copies the kept rows alone, where it walked every row marked,
  twice, testing each against `keep`. A text column read beside a predicate's is
  emitted so too.
- The indices' unpacking is left as it was: it takes as many values as a load of
  eight bytes holds (step 192), and the rest of R3, a wider unpack, would be a
  few percent of these queries.

## 2. Measured

**PDS-H at SF=1,** on four threads, step 377's runner and this step's alternating,
three rounds of five, at a load of 5.8 to 8.3 from other work:

| | step 377 | here | |
| --- | --- | --- | --- |
| q12 | 170.3 ms | 156.2 | 0.92 |
| q10 | 284.4 | 267.9 | 0.94 |
| q9 | 528.6 | 503.1 | 0.95 |
| q19 | 216.1 | 212.3 | 0.98 |
| q16, q3 | 64.2, 221.9 | 64.1, 221.7 | 1.00, 1.00 |
| in all | 1,485 | 1,425 | 0.96 |

**PDS-H at SF=0.1,** all 22, the same alternation, three rounds of five at a load of
4.7 to 5.0: **0.98 in all** (618.2 ms to 604.3), q19 0.90, q12 and q13 0.91; q1
1.07 and q17 1.05, whose path this step does not touch, the noise at that load.

## 3. Tests

No new test: the paths are step 193's and step 377's, and their tests cover them,
`TestDictionaryConjunctsDropOnlyFailingRows` with dictionary pages of no null, of
scattered nulls and of all null, and `TestAParquetFilterReadsTheRestAtItsKeptRows`
with text read beside a predicate's, from dictionary pages and PLAIN.

**Teeth: 4 of 4 bite:** a present row pending another dictionary index; a present
row's verdict ignored; a kept dictionary string emitted as a null; a kept PLAIN
string emitted as another.

**Gate:** the light gate, one package at a time on four procs: the tests at v3, 28
packages ok; levels, vet ×3 and the bench engine tests clean; the 22 PDS-H answers
match DuckDB.
