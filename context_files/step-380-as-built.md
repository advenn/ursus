# Step 380 — as built

**zstd's floor recorded.** R4 of `v0.7-scope.md`. No code changed.

[`feature-gaps.md`](./feature-gaps.md) §8 holds a row for it, so a later reader does
not chase it: Parquet's Zstandard is decoded by klauspost's pure-Go decoder, the one
arrow-go uses, at 508–571 MB/s on one thread where libzstd decodes 849 (step 375),
and ursus, being pure Go, keeps it.

## Measured

**The bare scan,** lineitem at SF=1, a program of ursus at step 379's code and
Polars 1.44.1's script, each seven runs, min and median, two rounds, at a load of
3.3 to 3.5 from other work:

| | ursus | Polars |
| --- | --- | --- |
| the four columns q14 reads, summed, one thread | 248–277 ms | 141–156 |
| `l_extendedprice` summed, one thread | 100–104 | 56–58 |
| the four, four threads | 118–127 | 55–70 |
| the one, four threads | 48–54 | 22–29 |

The first round read 342–374 against 167–175 for the four on one thread; the table
is the second's, and the profiled run's 248–258.

**Where ursus's goes,** the four on one thread, profiled: zstd 62%, about 170 ms a
run, more than Polars' whole read; the sums 15%, about 40 ms, a float's summed in one
chain to keep its bits; the read's own decode 8%, the runtime's collections 10% and
its copies and zeroing 6%, about 65 ms together. At step 375 the same scan was
306–326 ms, its zstd 63% and its runtime 14%: steps 376 to 378 took about 10 ms of
the rest, and the floor stands.
