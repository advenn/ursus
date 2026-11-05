# Step 495 — as built

**`TestARadixSortStopsAtItsNextPass` counts looks, not milliseconds.** PR #43's CI
failed on 2026-11-05 in `internal/kernel`, on its simd=128 leg, in a test the PR did
not touch: "cancelled at its first pass it took 158.410463ms, sorting whole
305.01008ms". The test (step 394) asked a radix sort of four million floats,
cancelled after its first byte pass, to come back in under half the whole sort's
time, the least of three runs each. On a loaded runner it came back at 52%.

## 1. What was wrong

The property is that a cancelled sort does no more passes. The test measured it by
wall clock, where the sort's setup, the keys gathered and the histograms, is a fixed
cost both runs pay, and a runner under load stretches it. Step 486's inventory had
marked the test "may be flaky".

## 2. The change

The test's `countdown` context already counts the sort's looks at it. The test now
reads that count: the whole sort looks eleven times, its entry, its column and once a
byte pass with one more at its end; the sort cancelled at its third look looks five,
the three and the return path's two (`halted()` and the error it returns). It asserts
the whole sort looked at least ten times and the cancelled one at most five. The
input is 65,536 floats where it was four million, and the test takes under 10 ms
where it took a second and a half.

## 3. Teeth

| mutation | |
| --- | --- |
| `halted()` answering false always, a sort that runs every pass and answers no cancellation | **bites:** "cancelled at its third look: <nil>" |

The property a sort that answers the cancellation only at its end would break, a
cancelled run looking once a pass to the end, is the `cut > 5` arm.

## 4. Checks

- `go test ./internal/kernel`: ok, the two cancellation tests three times each.
- PR #43's failed job rerun: see its merge.
