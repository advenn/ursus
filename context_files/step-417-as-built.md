# Step 417 — as built

**A Parquet dictionary page's count is checked against its bytes before it sizes
anything.** H2 of `v0.7.2-scope.md`, confirmed by its prober, the plan's 422.

## 1. What was wrong

`readDictionary` read a BYTE_ARRAY dictionary page by making its offsets for the
header's count, `make([]int32, 1, n+1)`, and only then found the values were not
there. H2's prober drew 76 MB from a 2.4 KB file; a count can ask for 8 GiB, which on
a service's small container ends the process before any error is returned. The
fixed-width branch already checked `n*size` against the bytes.

## 2. The change

Each BYTE_ARRAY value holds a 4-byte length, so a page of `len(data)` bytes holds at
most `len(data)/4` values: a count past that is refused as corrupt, "a dictionary page
of B bytes says N values", before anything is allocated.

**Left:** arrow-go sizes a page's buffer by its header's `uncompressed_page_size`
before decompressing, 64 MB from a 924-byte file, capped at 256 MB a page; that is
arrow-go's, decided apart (the plan's D14).

## 3. Tests

- **`TestADictionaryPagesCountIsCheckedAgainstItsBytes`** (in the package): a page of
  12 bytes, two values, saying ten million is refused as corrupt, having allocated
  under 1 MB, where it allocated 40 MB first; saying two, it reads "ab" and "cd".

**Teeth: 1 of 1 bites:** the check gone allocates the 40 MB again.

**Gate:** the Parquet package's tests; the root package's Parquet and dictionary
tests; vet. CI's every width, the scalar build and the race detector on the pull
request.
