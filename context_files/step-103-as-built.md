# Step 103 — as built

**String concatenation copies offsets and characters, not rows.** This is the third
item of step 101's ranking, and the one step 102's j3 profile named: with the decode
parallel, `Collect`'s final concatenation was 0.37 s of j3's 0.58 s, serial.

## 1. What was wrong

Every `Collect` ends in `kernel.Concat`, once per column, on one goroutine. For a
String or Binary column it:

1. built a `[]string` of every row, taking a string header per row through the
   accessor;
2. handed it to `data.NewString`, which counted the bytes, then copied every one
   of them again into the output buffer, row by row.

## 2. The fix

**`data.ConcatStrings`,** beside the column constructors, since it builds the layout
directly:

- **One pass over the parts** counts rows and live characters, and refuses past
  `MaxStringBytes` before anything is allocated, as `NewString` does.
- **Each part contributes its window:** the characters between its first and last
  offsets, copied once, with its offsets shifted by the characters before it.
  - The parts are derived columns as often as not. `Column.Slice` keeps the whole
    character buffer and windows the offsets, so a part's first offset is rarely
    zero. This is the concatList lesson of step 50, one type over.
  - A part with no character buffer at all (an all-null column from `NewNull`)
    contributes empty ranges.

`kernel.Concat`'s String and Binary arm is one call to it.

## 3. Measured

h2o at 2M rows, Parquet. Step 102's runner and this one were run alternately, three
iterations each:

| query | before | after | |
| --- | --- | --- | --- |
| j3 | 470–503 ms | 372–382 ms | −22% |
| j4 | 425–442 ms | 353–391 ms | −13% |

The "before" here is faster than step 102's own j3 figure (573–578 ms), on the same
code: the machine varies run to run. Hence alternate runs, rather than a comparison
against an old table.

## 4. Tests and teeth

**`TestConcatStringsMatchesRowByRow`:**

- 200 trials each for String and Binary, of one to five parts, mixing:
  - sliced parts whose offsets start past zero;
  - empty parts;
  - all-null parts with no buffer;
  - empty strings and nulls.
- Compared row by row with what each part held.

**`TestConcatStringsRefusesPastItsOffsets`:** 120 characters under a lowered
100-character limit are refused as a resource error.

| tooth | result |
| --- | --- |
| a part's window ignored (characters copied from zero) | **bites** |
| offsets not shifted by the characters before | **bites** |
| no size check | **bites:** both refusal tests, the step 87 one included |
| a payload-free part given no offsets | **bites** |

**Gate:** test-all 105 ok, race 21 ok, levels, vet ×3 and the bench engine tests clean; PDS-H SF=0.1 22/22 against DuckDB.
