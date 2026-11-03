# Step 427 — as built

**A type nobody holds is freed.** The plan's 435, M2 of `v0.7.2-plan.md` (H2.6).

## 1. What was wrong

`dtype` interns the payload of every parameterised or nested type, so that `==` on two
`DataType`s is equality of types. A struct, list, array or enum, or a datetime with a
zone, has exactly one `*typeExt` for each distinct payload. The table
(`dtype/intern.go`) held each payload strongly and never let one go, so every such type
ever built stayed for the life of the process.

Some of them came from the data:
- a struct for each set of NDJSON keys, and an enum for each category set;
- a zone for each name, interned before it is validated (`dtype.go:67`).

A service reading files of varied schemas grew without end. Run at step 427, ten rounds
of 10,000 each of a struct, a list, an enum and a zone, all dropped, left 400,002 entries
and 118.8 MB of heap that nine of the rounds had added.

## 2. The change

- **The table holds `weak.Pointer[typeExt]`.**
  - A lookup whose payload was collected is a miss.
  - `runtime.AddCleanup` on each payload deletes its entry, but only while the entry
    still holds that payload's weak pointer. A key interned again since, as a new
    payload, keeps its entry.
- **Equality still holds.** While a type is held, its entry answers anyone who builds
  it again. A payload nobody holds has no other type to be compared with.
- **A parent's key names its inner types by address,** which is safe for the same
  reason: a held parent holds its inner types, so their addresses are not reused while
  its entry answers.
- **Not changed:** the read lock on a hit, and the second look under the write lock
  when two goroutines race to insert.
- **Cost, measured on the laptop with a micro-benchmark, six runs each:**
  - rebuilding an existing type is unchanged: 2.6 to 3.2 µs, against 2.7 to 5.9 before,
    with the same allocations;
  - a new type costs 2.8 to 4.6 µs, against 1.0 to 1.9, and 14 allocations against 11,
    the weak pointer and its cleanup.
  - Types are built once per query, never per row.
- **End to end:** a read of a two-line NDJSON document with keys of its own, 5,000
  times a run, six runs.
  - It took a median of 471 µs, against 477 before.
  - Before, the runs slowed from 374 to 865 µs as the table grew; after, from 388 to
    676.

## 3. Tests

- **`TestInternedTypesAreCollected`** (`dtype`): ten rounds of 10,000 each of a struct,
  a list, an enum and a zone, dropped.
  - The table goes back to the two entries of the type the test holds, where it kept
    400,002.
  - Nine rounds past the first grow the heap under 2 MB, where they grew 118.8 MB.
  - The held type, rebuilt, is `==` to itself.
- **`TestInternedTypesRebuiltWhileCollected`** (`dtype`): four goroutines build the
  same structs twice over while another runs the collector without pause.
  - Each pair is one payload.
  - Two different structs are never one payload.
  - Also run under the race detector.

**Teeth: 3 of 3 bite.**
- no cleanup: the entries stay, and 51 MB of their keys;
- a cleanup deleting a newer entry: a struct built twice is two payloads;
- no second look under the write lock: the same.

The last two are races, and they bit every time in three runs.

**Gate:**
- `dtype` under the race detector;
- `./internal/...` and the root package in full;
- vet and levels;
- on the pull request, CI's every width, the scalar build and the race detector.

The public API is unchanged; only unexported code in `dtype` changes.
