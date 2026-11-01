# Step 422 — as built

**A naive Datetime is written without an offset.** H3 of `v0.7.2-scope.md`, a
known open defect since step 296, the plan's 428, decided as D9 recommends: only the
false "Z" goes, the `T` stays.

## 1. What was wrong

`dtype.FormatTemporal` wrote every Datetime with Go's `Z07:00`, and a naive one's
time is held in UTC, so a wall clock in no zone was written "2024-01-02T03:04:05Z":
named an instant in UTC. A print, a cast to String, `json_encode`, a pivot's column
names, `Explain`'s literals and error texts all read it so. The CSV writer was fixed
at step 354; the rest was left, `feature-gaps.md` §1's open row.

## 2. The change

A Datetime of no zone is written with no offset: "2024-01-02T03:04:05.000000". A UTC
one keeps its "Z" and a zoned one its offset. Polars writes a naive one with no
offset too, and a space for the `T`, which ursus keeps, as §9 records.

**A visible change in a patch:** text that read a naive Datetime's "Z" changes. With
`ursustest.IgnoreDTypes()`, which compares cells by their text, a naive Datetime and
a UTC one of the same ticks compared equal, and now differ, as a wall clock and an
instant do.

## 3. Tests

- **`TestANaiveDatetimeHasNoOffset`** (root): naive, UTC and New York, cast to String
  and printed.
- **`TestANaiveDatetimeIsNotUTCUnderIgnoreDTypes`** (`ursustest`): the two frames differ
  under `IgnoreDTypes`; a naive frame equals itself.
- **Seven tests whose expectations held the false "Z"** for a naive column, each
  checked line by line and changed there alone: `TestReadDatabase`'s instant among
  text, whose comment named the bug; `TestFrameAggregates`' mean, median, min, max and
  quantile; `TestInterpolateTemporal`; `TestInt96AndFixedLenByteArrayRead`;
  `TestStrptime`'s three naive cases, its UTC one keeping its "Z";
  `TestTemporalMeanAndMedianAnswerAsPolars`; `TestTemporalQuantile`.
- `feature-gaps.md` §1's row is marked fixed, and §9's two mentions say so.

**Teeth: 2 of 2 bite:** the "Z" back fails the root test and `ursustest`'s.

**Gate:** the whole suite at v3, 28 packages ok; vet. CI's every width, the scalar
build and the race detector on the pull request.
