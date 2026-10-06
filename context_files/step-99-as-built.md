# Step 99 — as built

**A List column past 2^31-1 elements is refused, not wrapped.** This is
`v0.4-scope.md` item 9: "the same guard as S24's String fix".

## 1. What was wrong

A List's offsets are 32-bit. Step 87 bounded a String column's characters
(`data.MaxStringBytes`, `audit.md` S24). A List's elements had no bound, and the
code that builds List offsets computes them as int32: concatenation, gathers,
implode, the Parquet and Arrow readers. Past 2^31 elements they wrapped silently.

**Measured once, outside the suite** (a probe, since moved out of the tree): two
one-row `List(Bool)` columns of 2^30 elements, built from no-storage bitmaps,
concatenated with no error to:

```
row 0: [0, 1073741824)            child holds 2147483648
row 1: [1073741824, -2147483648)
```

The suite cannot afford that. A List of Null elements looked free, but
`bitmap.Zeros` allocates its bits, and concatenating Null children is refused
outright: "concat is not implemented for Null", which is `audit.md` J12, a tier 2
item. So the suite lowers the limit, as `bigstring_test` lowers the String one.

## 2. The fix

**`data.MaxListElements`** (`math.MaxInt32`; a variable only so a test can lower it),
checked in `NewList` against the child's length.

- **Every List column is built through `NewList`** (ten call sites), and the child's
  length is the one count that has not already wrapped. So one O(1) check covers
  concatenation, gathers, implode, string split, the list kernels, the spill reader,
  the Parquet and Arrow readers, and anything added later.
- **It raises `KindResource`,** since `NewList` has no error to return. The hint
  names `CollectBatches` and `SinkParquet`, which never build one column of all of
  it.
- **A raised error passes through the Parquet reader's recovery as itself** (step
  87), not as a corrupt file. The test checks this.

## 3. Tests and teeth

**`TestListOffsetsDoNotWrap`** (`internal/kernel`, limit 100):

- refused past the limit when concatenated, when gathered, and when built;
- a control at exactly the limit, which reads its offsets back.

**`TestCollectRefusesAListColumnPastItsOffsets`** (public API, limit 100, 200
elements):

- a whole `Collect` is refused, and the error names `CollectBatches`;
- `CollectBatches` works;
- a group-by implode into one list is refused;
- a Parquet list column past the limit is a resource error;
- a control under the limit collects.

| tooth | result |
| --- | --- |
| no guard | **bites:** every refusal case, both tests |
| refused at the limit itself (`>=`) | **bites:** both controls |

**Gate:** test-all 105 ok, race 21 ok, levels, vet ×3 and the bench engine tests clean; PDS-H SF=0.1 22/22 against DuckDB. Run once over steps 98 and 99 together.
