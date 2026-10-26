# Step 379 — as built

**A join's text gathered by runs.** J1 of `v0.7-scope.md`.

## 1. How

A join's output takes each side's columns at its matched rows (`gatherOut`), and a
text column's rows are gathered by `data.TakeStrings`, whose `takeInto` copied each
row's bytes with a copy call of its own. In h2o `j1` over Parquet at ten million
rows, on four threads, `takeInto` and its copies were a tenth of the CPU: the probe
side's text gathered at its matches, nine rows in ten, in order, and the build side's
from its ten rows, one at a time.

- **A run of at least four consecutive rows** is copied with one call, and its
  offsets are the source's, moved to where the run lands. A run is taken where the
  next row and the one three on are the row's own plus one and plus three, so a
  gather in no order, where two rows in sequence happen by chance, does not branch
  into it and out again.
- **A lone row of at most sixteen bytes** is moved as two eight-byte words where both
  buffers have sixteen bytes from it, which takes no branch on its length; a longer
  row, or one at either buffer's end, is copied as before. The bytes past the row are
  overwritten by the rows after it.
- A column of no payload is its offsets zeroed, as before.

## 2. Measured

**The gather alone,** 65,536 rows on one thread, medians of six, against the copy a
row it replaces:

| | before | here | |
| --- | --- | --- | --- |
| the probe side: in order, one row in ten dropped | 370 µs | 195 | 0.53 |
| the build side: ten rows, of 3 to 9 bytes | 814 | 488 | 0.60 |
| in no order, from 100,000 rows of mostly 9 bytes | 631 | 564 | 0.89 |
| in no order, from 1,000,000 rows of 3 to 9 bytes | 2,640 | 2,549 | 0.97 |
| in no order, from 1,000,000 rows of mostly 9 bytes | 2,831 | 3,234 | 1.14 |

The last is the one shape it loses: rows of one length, whose copy calls predict
their branches, from a source larger than the cache, where sixteen bytes read from
each row likely touch the next cache line more often than its nine do. No suite query
showed it.

**h2o's joins** over Parquet at ten million rows, four threads, step 378's runner
and this step's alternating, three rounds of five, twice, at a load of 5 to 6 from
other work: j1 1.00 and 0.96, j2 0.96 and 0.96, j3 0.99 and 0.96, j4 0.99 and 0.95;
in all 0.99 and 0.96.

**PDS-H,** the same alternation: at SF=0.1, all 22, **0.99 in all** (627.9 ms to
624.0), q4 0.89, q13 0.92, q18 1.06 and q5 1.05 at a load of 5; at SF=1, the joins
that gather text, q18 0.99, q5 1.00, q21 0.97, q17 1.01, q10 0.98, q3 0.98, q13 0.98,
**0.98 in all**.

## 3. What was tried and left

- **A run of two rows:** a gather in no order from a small build side reads two rows
  in sequence one time in ten, and branched into the run and out: the lone row was
  1.10 to 1.15 of the copy's time.
- **A lone row copied as before,** the run check beside it: 1.10 to 1.20 of the old
  gather's time on a build side's, the check added and nothing saved.
- **A lone row moved exactly,** two overlapping words of four or eight bytes chosen by
  its length: no faster than the copy call where lengths vary, which branches on the
  length as the copy does inside.
- **The scope's "written without zeroing":** Go zeroes every buffer it allocates;
  the take's zeroing is about 1% of j1's CPU, and is left.

## 4. Tests

**`TestTakeStringsByRuns`** (`internal/data`): 400 gathers of runs of one row to
twenty and a pair whose fourth row looks like a run's; from the source's first rows
and to its last, between negative indices; of null rows, empty rows and rows of up
to forty bytes; from a column and from a slice of it whose offsets do not start at
zero; of 32-bit offsets and 64-bit, into either, each of the four seen; and a column
of no payload. Each row is the source's, and the characters are exactly the rows'.

**Teeth: 5 of 5 bite:** a run's offsets not moved to where it lands; a run's last
row's bytes left out; a run taken on its fourth row alone; a lone row of seventeen
bytes moved as two words; a lone row's offset left unwritten.

**Gate:** the light gate, one package at a time on four procs: the tests at v3, 28
packages ok; levels, vet ×3 and the bench engine tests clean; the 22 PDS-H answers
match DuckDB.
