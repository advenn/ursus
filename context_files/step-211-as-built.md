# Step 211 — as built

**A short quoted CSV field is scanned once.** Found weighing Tier 2's CSV item.

## 1. The profile, and the item it set aside

Tier 2 of `v0.6.2-scope.md` listed reading a CSV file in parallel by offset. Step 199
had left the read's copy serial. Profiled first: h2o gb1 over CSV at 10 million
rows, step 210's runner, three iterations at eight threads.

- **Wall time** is about 0.60 s a run, at an average of 6.9 of 8 cores.
- **The serial read**, `splitter.read`, is 0.47 s across four runs: about 120 ms of
  a 600 ms run, overlapped with the parsing. The read syscalls are 3% of the CPU.
- **The parse** is half the CPU: `scanner.parse` 50%, and `quotedField` 31%, since
  h2o quotes every string field.

The parse alone needs about 375 ms a run across eight cores, so reading by offset
would remove little from the path. **That item is not built.**

## 2. What changed

`quotedField` scanned each quoted field three times: once for its closing quote,
then a count of its line endings, then a search for a CRLF. Each scan is a call. For
`"id001"` the two after the first cost more than the field: `indexbody`,
`countbody` and `indexbytebody` were 13% of the CPU.

- **A field of up to 32 bytes** (`shortField`) is now looked at once, a byte at a
  time, counting line endings and noting a carriage return.
- **A longer field** is still counted and searched by the calls, which pay over
  many bytes.
- **The CRLF search** runs only when a carriage return was seen.

## 3. Tests and teeth

- **`TestALongQuotedFieldIsNormalisedAndCounted`:** fields of 28, 40 and 96 bytes,
  either side of the threshold, each holding a CRLF and a LF. The CRLF reads as LF,
  as `encoding/csv` reads it, and the record after starts on its physical line.
- **The existing differential tests** against `encoding/csv`, the line number tests,
  and the parallel-against-serial error tests cover short fields.

| tooth | result |
| --- | --- |
| a short field's carriage return missed | **bites:** the `encoding/csv` differential |
| a short field's line endings not counted | **bites:** three tests |
| a long field's carriage return missed | **bites:** the long-field test, added for it |
| a long field's line endings not counted | **bites:** the long-field test |
| every field the long way | silent, and equivalent: it is the speed |

## 4. Measured

Step 210's runner against this step's, h2o at 10 million rows over CSV, alternated
three rounds, two iterations:

| query | CPU | wall |
| --- | --: | --: |
| gb1 | 4,179 → 3,787 ms, **−9%** | 611 → 572 ms, −6% |
| gb3 | 10,743 → 9,957 ms, **−7%** | 1,494 → 1,398 ms, −6% |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
