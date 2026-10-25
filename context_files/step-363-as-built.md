# Step 363 — as built

**A sort of text by its bytes, seven at a time:** a String, Binary or Categorical
key is radix-sorted, alone or beside other keys, where it went to the comparator.
Step 361 found a million distinct strings of eight bytes took 963 to 1,430 ms on one
thread where Polars takes 42 to 64: Go's stable sort, whose in-place merge rotates,
and two closures and two offsets read a comparison.

## 1. How

**The key** (`textKey`). A string's key from an offset is seven of its bytes there,
big-endian and padded with zeros, over how many bytes it has there, at most eight,
in the low byte. Keys order as their strings do:

- two strings of equal bytes there differ by how many they have, and the shorter, a
  prefix of the longer, comes first: `a` before `a\x00`;
- two equal keys whose low byte is below eight are equal strings;
- only strings of equal keys whose low byte is eight have more to compare.

A string of fewer than eight bytes there is read a byte at a time: a copy of so few
bytes called memmove, a fifth of a sort of eight-byte strings. A descending sort
inverts each key, which reverses the order and leaves equal keys equal.

**The sort** (`textPass`, `sortText`). It is the radix sorter's pass for a column of
text, as `sortPass` is for a number's: nulls partitioned out first and put first or
last, then the rest sorted, stably, in place.

- **A round** gives each row its key at the offset, sorts the keys by the radix
  passes, steps 361's `radixSortedN` past 65,536 rows, and moves the rows into the
  buffer the sort did not end in.
- **The runs of one key with more to compare** are sorted again from seven bytes
  on. Of many rows, runs of 65,536 or more are split across the workers themselves,
  and the rest are shared out, a piece of them a worker.
- **A round of one key,** as a prefix every string shares gives, moves nothing.
- **At most 32 rows** (`sortFewTexts`) sort their keys by insertion, which is
  stable, and their ties again from the next seven bytes.

No two strings are ever compared. Each round is a stable sort, and a run of one key
keeps the order it came in, so the sort is the comparator's stable sort exactly, ties
in their order. `radixable` takes text, so a text key beside a number's is one LSD
pass among the others.

## 2. Tests

- **`TestTextSortMatchesTheComparator`,** against `ArgSort` of `NewComparator`, on one
  worker and on four, `parallelMin` lowered to 64, at 5, 33, 200, 5,000 and 70,000
  rows:
  - words that are prefixes of one another, an empty one, zero bytes, bytes past
    0x7f, lengths about every multiple of seven;
  - random strings of up to 22 bytes of four values, 0, 1, `a` and 0xff;
  - a 24-byte prefix every string shares;
  - distinct strings;
  - nulls first and last, ascending and descending;
  - text alone, beside an integer key before and after it, and two keys of text.
- **`TestTextKeysOrderAsTheirStrings`:** 20,000 pairs of strings that share their
  bytes before an offset: their keys from it order as `strings.Compare` orders them,
  and tie with a low byte below eight only when the strings are equal.
- `TestRadixFallsBackForUnorderableKeys` now checks the text pass, alone and beside a
  numeric key, against the comparator, its comment updated.

**Teeth: 12 of 14 bite.**

- **The key:** a long string's low byte seven; a short string's length not kept; its
  bytes a byte low; a descending key not inverted, or its low byte read so.
- **The sort:** a few keys' insertion unstable; a few strings' ties, a run's ties or a
  shared prefix's next bytes not sorted on; a worker's last run skipped.
- **The nulls:** first for last; sorted as their bytes.

The two silent ones: the rows moved into the buffer the sort did not end in, which
the code did then and which is equivalent, since each place of the order is read
before it is written, so the rows now take their order in its own buffer; and text
sent to the comparator again, which changes no answer and is the timings' to show.

## 3. Performance

A frame of a million rows in memory, median of seven, at a load of 2.5, Polars 1.44.1
measured alongside:

| | before, 1 thread | 4 | 8 | after, 1 thread | 4 | 8 | Polars |
| --- | --- | --- | --- | --- | --- | --- | --- |
| distinct strings of 8 bytes | 853 ms | 295 | 274 | 150 | 86 | 75 | 52 |
| URLs of a 24-byte prefix | 1,128 | 495 | 370 | 240 | 150 | 126 | 104 |

The "before" is step 362's, whose merge sort split the comparator's across the
threads. The URLs are `https://example.com/item/N?ref=M`.

- **The strings:** what was the comparator's is now a fifth of the time: the key's
  reads, the radix rounds, and the insertion sorts of the hundred thousand runs of
  ten strings that share seven bytes. The rest is the output's gather of the strings,
  `TakeStrings`, and the collector, which scans the benchmark's own million Go
  strings at every cycle.
- **The URLs:** three rounds of one key, their shared prefix, move nothing, and a
  fourth sorts them. The key's read of a row's bytes, in an order no longer the
  rows', is the sort's largest part.

**Gate:** the light gate, one package at a time on four procs: the tests at v3, 28
packages ok; levels, vet ×3 and the bench engine tests clean; the 22 PDS-H answers
match DuckDB. The other widths, the scalar build and the race detector are CI's.
