# Step 391 — as built

**A join of several integer keys packs them into words.** The leftovers of
`v0.7-scope.md`: Upsample's join keys, and packed multi-integer join keys, measured.

## 1. What a join of two integer keys spent

Steps 381 and 384 packed a group-by's integer keys into two words or three
(`KeyPacker`, `WordKeyTable`); a join's keys of more than one column were encoded
still, each row's bytes written, hashed and compared, as was one integer key whose
nulls match, which `IntKeyTable` does not hold. Upsample joins its grid to the frame
on the group's keys and the instant with nulls equal: step 383 found the encoded keys
two thirds of what was left of it.

A probe for it: a million rows joined to 500,000 distinct keys of two Int64s, against
the same rows joined on one Int64 of the same values, four threads, at a load of 1.7:
two keys took 95.6–102.5 ms, one 34.0–34.8, and Polars 66.5–67.7 and 27.7–29.4.

## 2. How

- **`wordKeyed`** (`physical/joinbuild.go`): a join whose keys are integers, more
  than one or one whose nulls match, and pack into three words at most, is built
  partitioned into `WordKeyTable`s where it was into `KeyTable`s: each batch's keys
  packed once, routed by `WordHash`, and inserted a chunk at a time; a null key is
  left out unless nulls match, where its null bits are its key. The tables are read
  as one by `kernel.WordKeyParts`, as `IntKeyParts` reads integers.
- **The probe** (`physical/join.go`, `startBatch`): a batch's keys packed by the same
  packer, the build's key types being the probe's, and looked up a chunk at a time. A
  key's bytes are still encoded where a spilled join routes it or a validation needs
  them. The streaming build, on one thread, under a memory limit of the caller's, or
  past half of the default one, encodes as before.

## 3. Measured

The probe, step 390's build and this one alternating, three rounds at a load of about
2.9, minimum and median of seven:

| | step 390 | here |
| --- | --- | --- |
| an inner join on two Int64s | 108.0–125.5 ms, median 114.1–137.7 | 82.1–91.9, median 85.2–98.0 |
| a left join on two, nulls equal | 88.2–124.8, median 109.3–132.8 | 84.1–87.5, median 88.2–91.4 |
| one Int64, a direct index (step 203) | 33.8–39.9 | 36.2–41.1 |
| U1, Upsample by 100 groups (§4) | 230.8–258.8, median 269.7–304.8 | 209.9–217.2, median 217.7–234.7 |

Two keys take about 0.76 of their time and Polars' 1.3 times; Upsample about 0.85, and
still nine times Polars' 21–25 ms: what is left of it is building the frame's million
keys, two fifths, and the probe's lookups, a fifth, where Polars joins a group at a
time on its sorted instants. Its own operator would be the next step, and is left.

PDS-H at SF=1, step 389's runner (step 390 touched the CSV writer alone) and this
step's, two rounds of five at a load of 5.4 to 5.6: q9, whose partsupp joins lineitem
on two keys, 441.7 → 432.2 ms, 0.98; q5 197.2 → 191.0, 0.97; q20 262.4 → 245.8, 0.94.

## 4. Tests

- **`TestAPartitionedBuildOfIntegerKeysPacksThem`** (`physical`): an Int64 and an
  Int32, two words, and two Int64s and an Int32, three, with nulls in each, for every
  kind, matching nulls or not, against the streaming build row for row; four keys past
  three words are encoded still.
- **`TestThePartitionedBuildAnswersAsTheStreamingOne`** (step 151): an Int64 key whose
  nulls match is now packed, where it was encoded.

**Teeth: 5 of 5 bite:** a null key left out though nulls match; a key's id without
its table's base; a key routed by another hash; a key's third word left out; the
probe's third word not looked up. The last's first form, the third word packed into
another slice, was no fault, as `Pack` fills whatever slice it is given.

**Gate:** the light gate, one package at a time on four procs: the tests at v3, 28
packages ok; levels, vet ×3 and the bench engine tests clean; the 22 PDS-H answers
match DuckDB. `./internal/...` and the root package ran whole before it.
