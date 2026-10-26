# Step 384 — as built

**Integer keys of three words.** G1 of `v0.7-scope.md`, extended to the keys step
381 left encoded.

## 1. How

Step 381 packed a group-by's key of integer columns into two words where its fields
and a null bit a column fit 128 bits. PDS-H q20's key, `l_partkey` and `l_suppkey`,
two Int64s, is 130 bits, and q3's, an Int64, a Date and an Int32, 131, so both stayed
encoded: q20's table and its encoding a quarter of its CPU.

- **`WordKeyTable`** (`kernel/wordkeys.go`, `PairKeyTable` and `pairkeys.go`
  renamed): a key of two words or three, the count fixed when the table is made. A
  third word is stored, compared and hashed only in a table of three; `WordHash` of
  a key whose third word is zero is step 381's hash of two, so a table of two hashes
  as it did.
- **`KeyPacker`** places fields in up to three words, each in the first with room,
  and says how many it used: two Int64s and their nulls take three, two Int32s still
  two. Three Int64s and their nulls, 195 bits, stay encoded.
- **The group-by** makes its table of the packer's words; n_unique's (value, group)
  stays two.

## 2. Measured

**The table alone,** 900,000 keys of q20's shape, an Int64 of 200,000 values beside
one of 10,000, inserted into an empty table on one proc, medians of six: two words
77–79 ms, three words 84–88, the encoded keys' table 104–119, without counting the
encoding it no longer pays.

**PDS-H at SF=1,** step 382's runner and this step's alternating, three rounds of
five at a load of 4.2 to 6.8 from other work: q20 0.96 (298.7 ms to 285.8), q3 0.99,
q16 1.01; h2o gb6, whose key stays two words, 1.01. q20's group-by holds about half
a million keys, a table past the cache, where the misses cost both forms alike.

Again, four rounds at a load of 4.0 to 5.8: q20 0.96 (298.0 to 286.7), q3 1.01.

## 3. Tests

- **`TestWordKeyTableAgainstAMap`:** step 381's test for tables of two words and of
  three, keys that differ in one word alone, the third among them, a table of two
  given no third word half the time, lookups that miss in the first word or the
  last.
- **`TestKeyPackerKeepsKeysApart`:** four more shapes of three words, q20's, q3's,
  Uint64 Int64 Int16 Uint8 with a column of no payload, four Int32s, each its words
  counted; three Int64s, two Int64s and two Int32s, and a Uint64 and four Int32s
  refused.
- **`TestPackedGroupKeysAnswerAsEncodedOnes`:** q20's and q3's shapes, and three
  Int64s, encoded both ways, against the keys cast to String.

**Teeth: 4 of 4 bite:** a key's third word read back as zero; a key of three words
taken for two; a chunk's third words kept from the chunk before; a new key's third
word not stored.

**Gate:** the light gate, one package at a time on four procs: levels, vet ×3 and
the bench engine tests clean; the 22 PDS-H answers match DuckDB. Its tests at v3
found `TestAnIntegerGroupKeyIsNotEncoded` holding two Int64 keys encoded: it now names
each form, two words for two Int32s and a Date beside a Uint16, three for two Int64s
and for q3's key, encoded for three Int64s and a float beside an Int64; with it,
`internal/physical` passes whole, and the other 27 packages passed in the gate.
