# Step 389 — as built

**A CSV file's floats read in one pass however they are written, and Collect's copy
on the query's threads.** C2 of `v0.7-scope.md`.

## 1. What C2's probe spends

§4's C2 probe (step 382), a million rows of an integer, a float, text and an instant
read from a 54 MB file on four threads, profiled on step 388's build:

- **the float's parse, 28%.** Step 373 parsed a float in one pass where it was
  Clinger's exact case, a whole number below 2^53 over a power of ten of at most 22.
  A float written in full, its 17 digits, as Go and Polars write `i/3`, is not; 46%
  of the probe's floats were read three times, by that pass, the grammar's
  (`floatSyntax`) and strconv's. Go 1.27's own `strconv.ParseFloat` was faster than
  ursus's on them.
- **the scanner, a fifth**, and the builders' calls and `appendTo`'s loop, a tenth;
- **`Collect`'s copy, 14%,** on one thread after the read on four: Polars hands
  back the file's 129 chunks as they are, where a `DataFrame` here is one.

## 2. How

- **A float in one pass, whatever its digits** (`dtype/numtext.go`, `exactFloat`):
  a plain decimal of at most 19 significant digits is read into a whole number and
  its float found by `dtype.NearestFloat`, Clinger's case inline and else the
  Eisel-Lemire algorithm, the NDJSON reader's since step 334, moved to `dtype`
  (`dtype/nearest.go`) for both. Both round correctly, so the float is strconv's;
  what neither settles, a halfway case, a subnormal, past the range, more digits, is
  strconv's as before. A fraction's digits are read eight at a time while the whole
  number has room for them (`le64`, `eightDigits`, `addEight`, Lemire's fast_float);
  a whole part's eight at a time cost a short float more than they saved.
- **`Collect`'s copy on the query's threads** (`kernel.ConcatOwnedN`,
  `data.ConcatStringsWith`): each column's rows are split in pieces, a goroutine
  each, every part's place in the answer known before any is copied; the columns
  still go one at a time, so the batches are let go of a column at a time as step
  128 made them. A piece's panic is raised again in the caller. A fixed-width column
  and text are split; a Bool, a list and a struct are copied as before.
- **Not built:** a block's records parsed a column at a time. The builders' calls
  made direct by a type switch, what it would save first, measured nothing: the
  builders' methods are too large to inline either way.

## 3. Measured

The float alone, `ParseFloat` over 143,000 values each, ns a value, step 388's and
this step's in one run, four runs each:

| | step 388 | here | strconv |
| --- | --- | --- | --- |
| `i/3` as Go writes it, 7 to 17 digits | 48.3–48.6 | 15.9–16.7 | 40.5–53.0 |
| h2o's, six places | 13.3–15.2 | 13.1–14.0 | 33.7–37.0 |
| random float64s, shortest, with exponents | 134.2–169.3 | 39.8–41.6 | 78.7–86.7 |

C2's probe, four threads, five rounds alternating, minimum and median of seven:

| | minimum | median |
| --- | --- | --- |
| step 388 | 58.8–76.3 ms | 76.2–92.4 |
| the float's parse alone | 56.6–68.3 | — |
| here | 56.2–65.3 | 59.7–73.0 |
| Polars 1.44.1 | 25.5–42.9 | 28.2–45.4 |

Four rounds at a load of 4.6 to 5.0; the float's parse alone in five earlier rounds
at about 2, beside step 388's 62.4–77.1. About 0.78 of the time by the medians, and
twice Polars' where it was 2.6 times. Five rounds of step 388's build and this one's
at a load of about 2 read 74–84 ms and 54–65 at their minimums.

**What is left,** the profile after: the scanner a quarter, the float a sixth, the
text columns' copies and `appendTo`'s loop a tenth each, the copy into one frame a
tenth now spread on four threads. Polars' 129 chunks are never copied into one.

h2o `gb1` over its 10M-row CSV (`-io csv`), four threads, step 388's runner and this
step's alternating, three rounds of three at a load of 3 to 3.9: 529–1,007 ms, median
785, before; 521–876, median 791, after; the answers byte for byte the same. h2o's
floats have six places, Clinger's case before and after, and `gb1`'s answer is a
hundred rows.

## 4. Tests

- **`TestParseFloatIsStrconvsToTheBit`** (`dtype`), extended: the range's edges and
  past them, subnormals, the halfway cases between two floats, more digits than a
  word holds, 300,000 random float64s written shortest, in `e` to 24 places and in
  full, 300,000 fractions of runs of zeros and digits across the eight-byte words,
  and a byte that is no digit at each place in a word.
- **`TestConcatOwnedNIsConcat`** (`kernel`): `ConcatOwnedN` against `Concat`, row
  for row: Int64, Float64, Int128 and text with nulls and a Bool; parts empty, of one
  row, sliced out of larger columns, and text parts with no payload; at 32-bit offsets
  and past `MaxStringBytes` at 64; on 2 to 7 workers with every size split; a null
  row's bytes too; and a piece's panic reaching the caller.
- **`TestACSVsFloatsReadAsStrconvReadsThem`** (root): 120,000 rows, more than a
  piece of the copy, of floats as Go writes `i/3`, random float64s shortest, h2o's
  six places, `e` to 19 places and nulls, beside text with nulls and empty strings
  and an integer; every float strconv's to the bit, on one thread and four.

**Teeth: 11 of 11 bite:** a word's leading zeros not counted out; ':' to '?' taken
for digits; a word's digits misread; a word's place misread; a halfway case settled
by Eisel-Lemire; a piece of text from its part's first row; a piece begun a part
late; a piece of values from its part's first row; a piece's last row left out; a
piece's panic dropped; text with no payload given its part's place. The first's
first form did not build; the last bit once the test compared a null row's bytes too.

**Gate:** the light gate, one package at a time on four procs: the tests at v3, 28
packages ok; levels, vet ×3 and the bench engine tests clean; the 22 PDS-H answers
match DuckDB. `./internal/...` and the root package ran whole before it.
