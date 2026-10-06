# Step 87 — as built

**A String column past 2 GiB is refused, not wrapped, and the key table has no such
limit.** This is `audit.md` S24, the last open row in the release notes' list of
answers that can be wrong, short of the unmeasured ones.

Four commits and this document.

Authoritative where it disagrees with the vision docs and [`design/`](./design/).

Each of these was asserted on its own exit code:

- `make test-all` exit 0, with **105** package-ok lines: 21 packages × 5 SIMD
  configurations.
- `make race` exit 0 (21).
- `make levels` and `go vet` clean in all three modules.
- PDS-H SF=0.1 exit 0, with **22/22** matching the duckdb reference.

No benchmarks.

**The suite is 2936 passing tests and subtests** (2925 at step 86).

---

## 1. What was read, not run

Reproducing either defect takes 2 GiB of characters, which this laptop is not asked
for, so both were read from the code, as S22 was at step 85.

- **String columns.**
  - Every String column is built through `data.NewString` or
    `data.NewStringParts`; no kernel builds string offsets by hand.
  - Both computed 32-bit offsets with no check, so past 2 GiB they wrap: a row reads
    other rows' bytes, or slices at a negative offset.
  - **Why it is reachable:** a batch is bounded by the batch size, but **`Collect`
    concatenates every batch into one column**. Thirty million rows of 100-byte
    strings is 3 GiB, inside the README's "tens of millions of rows".
- **The key table.**
  - `kernel.KeyTable` keeps every distinct group or join key of a query in one arena,
    indexed by `[]int32` offsets.
  - Past 2 GiB of distinct key bytes they wrap, and a key is compared against the
    wrong bytes: groups merged or split, silently.

## 2. The fixes

1. **`data.MaxStringBytes`** is the one limit, `math.MaxInt32`. It is a variable so
   that a test can lower it, and the pad bound from step 85 now reads it too.
2. **`NewString` counts in int64, and `NewStringParts` counts its characters.** Past
   the limit, each raises a KindResource error. The message says what the column
   needs and what it holds; the hint says to read the result with `CollectBatches`,
   or write it with a sink, neither of which builds one column of all of it.
3. **`uerr.Raise`** is how a constructor with no error to return raises one on
   purpose.
   - It panics with a marker that `FromPanic` and `Attributed` unwrap unchanged.
   - An `*Error` panicked any other way is still taken for a bug.
   - Every operator boundary already recovers a panic (step 72), so a raise surfaces
     as itself from `Collect`.
   - The marker is an `error`, so if one ever escapes to a caller's goroutine — a
     public constructor such as `Values`, called directly — it prints its message.
4. **`KeyTable`'s offsets are `[]int64`.** They are internal, not an Arrow layout, so
   the limit is simply gone, at 4 bytes a key, which `NBytes` counts.

## 3. Tests

Each runs under a limit lowered to a few KiB.

- **`internal/data`:** each constructor builds at the limit and is refused past it.
- **`bigstring_test.go`:**
  - a frame built under the real limit is refused when collected whole, and read in
    full through `CollectBatches`;
  - a result under the limit collects;
  - a Parquet column past the limit is `ErrResource`, not "corrupt file". The Parquet
    reader recovers inside its own `Next`, so this is the path a reader's raise
    takes.
- **`internal/uerr`:** a raise is itself through both recoveries, and an `*Error`
  panicked without Raise is still internal.

## 4. Teeth

Every patch was checked to have applied, and every one ran against a green baseline.
**All 5 bite.**

| reintroduce | fails |
| --- | --- |
| `NewString`'s check removed | the data test, and Collect refusing |
| `NewStringParts`' check removed | the data test, and the Parquet case |
| `FromPanic` not passing a raise through | the data test, and Collect refusing |
| `Attributed` not passing a raise through | **silent at first.** The Parquet reader's recovery sends any panic from ursus's own frames to `FromPanic`, so nothing handed a raise to `Attributed`. The contract test now pins it. |
| every `*Error` passing through `FromPanic` (an over-reach) | the contract test's other half |

**No tooth reaches the key table's offsets**, because that takes 2 GiB of distinct
keys. The type change is the fix, and nothing short of that size can tell it from
the old one.

## 5. Behaviour changes

- **A String column past 2 GiB of characters** — in a result, a read, or anything
  else that builds one — is refused with `ErrResource`.
- **A group-by or join over more than 2 GiB of distinct key bytes** groups correctly.

## 6. Still open

- **O13** beyond its plainest shape, and **I23**; both are unmeasured.
- **S26** and the **J8 remainder**, where ursus and Polars wrap or round alike.
- **I24:** the non-nullable check runs only in test builds.
- **List offsets** are 32-bit too. They count elements, not bytes, so they would need
  more than 2^31 elements in one column.
