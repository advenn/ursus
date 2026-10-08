# Step 125 — as built

**`is_in` on a String column compares strings, not encoded keys.** This step saves
CPU and barely moves wall time, and this record says why.

## 1. The profile

PDS-H q12 at SF=1 ran in about 490 ms. `kernel.InSet` was 22.6% of its CPU: `IsIn`
on `l_shipmode`, and twice on `o_orderpriority` inside the aggregates. Of that:

- 53% was `GroupKeyEncoder.Encode`, building every row's grouping key to look it up;
- 25% was the Go map lookup;
- 16% was appending the answer a bit at a time.

q19's `InSet` was 16% of its CPU.

## 2. The change

- **`kernel.StringMembers`** recovers a String or Binary set's members as plain
  strings from its encoded keys, once per query, when the call is compiled. It sits
  beside `keyWriter`, whose format it inverts: 0x01, a four-byte length, the bytes.
  A set with any other key shape is not taken for strings.
- **`kernel.InStrings`** tests each row as it is stored, against the members one by
  one up to eight of them, and through a map of plain strings past that. Membership is
  still grouping equality. For strings that is byte equality, so nothing changes, and
  the floats that need `OrderKey` never reach this path.
- **`memberBits`** packs the answers 64 rows at a time, for `InSet` too.

## 3. Measured

The step 124 runner against this one, alternated, three rounds each:

| query | before | after | |
| --- | --: | --: | --- |
| PDS-H q12, SF=1 | 488–530 ms | 487–521 ms | unchanged |
| PDS-H q19, SF=1 | 466–472 ms | 438–446 ms | −6% |
| PDS-H q16, SF=1 (an integer set) | 96–100 ms | 95–99 ms | unchanged |

**Why q12 did not move:** its profile after the change shows `InStrings` at 0.51 s of
CPU where `InSet` took 1.34 s, and q12's total CPU fell 16%. The work that left was
parallel, though. What bounds q12's wall time is serial: the join's build,
`KeyTable.GetOrInsert`, inserting 1.5 million `orders` keys on one thread, about
250 ms of the 490. That is the next step's subject.

## 4. Where PDS-H SF=1 stands

ursus alone through the driver, under the 8 GB scope, after this step:

- **Geomean:** 3.71× Polars, against the Polars times in step 117's report. Polars
  re-timed tonight is a little faster than that report had it, so the true ratio is
  near 4×.
- **The widest gaps:**
  - q15, 9.1×: it computes one `lineitem` group-by twice;
  - q19, 7.6×;
  - q12 and q4, 6.7×: their join builds;
  - q22, 6.6×.

## 5. Tests and teeth

**`internal/kernel/inset_internal_test.go`:** `TestInStringsAgreesWithInSet` runs 300
random rounds. Each has a String column with nulls and the empty string, sliced, and a
set of one to eleven members, so both the one-by-one path and the map path run. Every
row must answer as `InSet` does. An Int64 key must not be read as a string member.

| tooth | result |
| --- | --- |
| the few-members path misses the empty string | **bites** |
| the map path tests only the first member | **bites** |
| the members keep a byte of the length prefix | **bites** |

**Gate:** with step 126, whose key-table edit began while this step's own gate was running. That gate was stopped and its result discarded; see step 126.
