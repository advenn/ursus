# Step 136 — as built

**The CSV reader's fixed-width builders keep their values slice from batch to
batch.** Step 132's profile put them at 10% of what h2o `gb10` allocates over CSV.
The step was asked for as doubling. What the measurements found was not growth but a
slice thrown away every batch, and keeping it is the whole change. No benchmark was
run, by request.

## 1. Evidence first

**`TestReadingAllocatesAboutWhatItProduces`** (`internal/source/csv`) reads 200,000
rows serially and on four threads:

- two Int64 columns, a Float64 and a String;
- the bytes allocated, from `/gc/heap/allocs:bytes`, against the bytes of the
  columns produced.

**On the code before this step,** counting every batch: **4.08×** serially and
**4.30×** on four threads.

## 2. What it found

**`fixedBuilder.finish` dropped its values slice every batch.** Its comment said
`data.NewFixed` wraps the slice without copying, so reusing it would rewrite a batch
the consumer still held.

**`NewFixed` copies,** into a fresh Arrow buffer, and has since the first commit.
`git log -S` finds the copy in `673696a`.

So each batch's values were allocated twice:

- once growing an empty slice by `append`, past 256 values a quarter at a time;
- once in `NewFixed`'s copy.

## 3. What changed, and what was tried and dropped

**`finish` keeps the slice,** `b.vals = b.vals[:0]`, as `stringBuilder` always kept
its buffers. Its comment now says why that is safe.

**Tried and dropped:**

- **A `reserve(n)` on every builder,** called by the reader at each batch's start.
  It took the ratio from 4.08 to 1.89 while the slice was still dropped. Once the
  slice was kept, it saved 1 to 2% per batch, 1.13 to 1.11, mostly the validity
  bitmaps. The rest of what it saved was the first batch, once per reader. Not
  worth an interface method that no test could tell from its absence.
- **Doubling the String builder's character buffer.** That buffer is kept between
  batches already, so doubling changes only its first growth. The profile never
  showed the String builder, and no test could see it.

## 4. Measured

The test counts from the second batch on. The first grows what every reader grows
once, its staging and its builders, which 200,000 rows do not amortize and ten
million do.

| reading | before | after |
| --- | --: | --: |
| every batch, serial | 4.08× | 1.29× |
| every batch, four threads | 4.30× | 1.54× |
| from the second batch, serial | not taken | 1.13× |
| from the second batch, four threads | not taken | 1.13× |

The "after" readings counting every batch were taken before the test began from the
second batch. The test's own figure, from the second batch on, has a bound of 1.25,
which the gate checks in every build and under `-race`.

The 0.13 left over was not broken down.

**`gb10` over CSV is not measured.** The profile's 2.4 GB for these builders should
mostly go: of each batch's two allocations, the one that grew by quarter steps
happens once per reader now.

## 5. Teeth

| tooth | result |
| --- | --- |
| the builder drops its values slice every batch | **bites:** the allocation bound |
| `NewFixed` wraps its slice instead of copying | **bites:** the first batch's row 0 read 196,608, not 0, after the rest was read |

The second is why the test keeps the first batch and reads it after every other: it
checks the reuse is safe, and would fail if `NewFixed` ever stopped copying.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. No benchmark, by request.

## 6. Measured afterwards, at the maintainer's request

`gb10` and `j5` at ten million rows, ursus alone, one timed run each, under a 3 GB
scope, at `55fb091`. Beside each, step 134's run, at `c83a400`. CPU is the scope's,
from the journal.

| query | time | CPU | VmHWM | heap in use | allocated | collections |
| --- | --: | --: | --: | --: | --: | --: |
| `gb10`, CSV | 6.6 s (6.8) | 20.1 s (21.7) | 2.75 GB (2.77) | 2.71 GB (2.83) | 14.9 GB (17.5) | 31 (39) |
| `j5`, CSV | 8.8 s (10.0) | 46.9 s (53.9) | 2.89 GB (2.88) | 2.89 GB (2.83) | 12.0 GB (17.1) | 27 (40) |
| `gb10`, Parquet | 3.7 s (4.0) | 14.8 s (16.1) | 2.85 GB (2.67) | 2.78 GB (2.82) | 16.4 GB (16.4) | 32 (30) |
| `j5`, Parquet | 4.4 s (4.4) | 41.1 s (41.6) | 2.89 GB (2.88) | 2.79 GB (2.78) | 17.4 GB (17.4) | 33 (32) |

**Over CSV, both allocate less:**

- `gb10` 2.6 GB less, 15%;
- `j5` 5.1 GB less, 30%. It reads two ten-million-row files, so twice the columns.

**`j5` over CSV used 13% less CPU,** with a third fewer collections. That is past the
9% single runs on this machine have wandered by (step 134), though it is still one
run.

**Over Parquet nothing moved, as it should not:** that reader does not use these
builders. Its allocations are the same to a tenth of a gigabyte.

**Since step 131,** before steps 133 to 136:

| query | allocated then | now |
| --- | --: | --: |
| `gb10`, CSV | 28.0 GB | 14.9 GB |
| `gb10`, Parquet | 25.6 GB | 16.4 GB |
| `j5`, CSV | 18.7 GB | 12.0 GB |
| `j5`, Parquet | 18.5 GB | 17.4 GB |
