# Step 307 — as built

**P5: `Concat`'s modes** (`v0.6.7-scope.md` §3), Polars' `concat(how=...)`, which
finishes the scope's reshaping.

## 1. What Polars 1.44.1 has, probed

Eleven modes: `vertical`, `vertical_relaxed`, `diagonal`, `diagonal_relaxed`,
`horizontal`, `horizontal_extend`, `align`, `align_full`, `align_inner`,
`align_left`, `align_right`.

- **`vertical`** refuses an Int32 beside an Int64. ursus's strict mode meets them
  exactly at Int64, as before.
- **The relaxed modes** meet types at Polars' supertype:
  - Int64 and Float64 at Float64;
  - a number, a Boolean or a date beside text, at text;
  - a Boolean beside a number, at the number;
  - a Date beside a Datetime, at the Datetime;
  - a List beside a number, refused.

  The columns' names and order must still agree.
- **`horizontal` pads** a shorter frame with nulls, deprecated since 1.42.1: it will
  require equal heights, and `horizontal_extend` keeps the padding.
- **`align`**, read in Polars' source:
  1. keys, the columns every frame has, in first appearance;
  2. a chain of joins, `coalesce=True`, `maintain_order="right_left"`;
  3. a stable sort by the keys, nulls first, and a select of every column in first
     appearance.

  Null keys match nothing. Its comment says a column collision raises. In fact a
  column in some frames and not every one is suffixed, then selected away: the
  first frame's values are kept and the others' silently lost.

## 2. The API

```go
type ConcatMode uint8 // ursus's own type now, not the plan's
const (
	ConcatStrict ConcatMode = iota // "vertical"
	ConcatDiagonal
	ConcatRelaxed           // "vertical_relaxed"
	ConcatDiagonalRelaxed
	ConcatHorizontal        // HStack's, equal heights
	ConcatHorizontalExtend  // a shorter frame padded
	ConcatAlign             // "align" and "align_full"
	ConcatAlignInner
	ConcatAlignLeft
	ConcatAlignRight
)
func ConcatFrames(ctx context.Context, frames []*DataFrame, opts ...ConcatOption) (*DataFrame, error)
```

**`ConcatMode` became ursus's own type,** from an alias of `plan.ConcatMode`, since
the horizontal and align modes never reach a `Union`. Code outside ursus cannot name
the internal type, so only `String` changed, to Polars' names: `ConcatStrict` prints
"vertical". The CHANGELOG lists it under breaks. `ConcatStrict` and
`ConcatDiagonal` keep their values, 0 and 1.

**`ConcatFrames`** is the eager form in any mode. `DataFrame.Concat(ctx, others...)`
takes its frames variadically, so it cannot take options too; it stays strict.

## 3. How each is built

**Relaxed:**

- `plan.ConcatRelaxed` and `ConcatDiagonalRelaxed` meet types by `dtype.Supertype`:
  `Promote`'s, which may round, and past it text, a Boolean beside a number, a Date
  beside a Datetime.
- `Promote` already met an Int64 and a UInt64 at Int128, where Polars' supertype is a
  Float64, and a Decimal and an integer at the Decimal wide enough, where Polars' is a
  Decimal(38). Both are exact and narrower; both are in §9.
- **The casts stay strict.** A strict cast rounds 2^53+1 into a Float64 without
  failing, as Polars rounds it. Nothing the supertype makes can then fail but a value
  past its type's range, which is better refused than nulled. The tooth that made
  them strict was silent, which is how this was found.

**Horizontal:**

- `ConcatHorizontal` is `HStack`.
- `ConcatHorizontalExtend` is `plan.HStack{Extend: true}`. The operator, which reads
  its frames in step, gives a frame that has ended null columns beside the others'
  rows, and the schema makes every column nullable.
- `WithChildren` carries the flag, or a rewrite would drop it, which a tooth checks.
- Projection pushdown keeps a column of every frame for its height, so a padded frame
  still counts.

**Align** (`alignOf`):

1. the keys are the columns every frame has, in first appearance;
2. a column in some frames but not every one is refused, as Polars' docs have it;
3. each frame's rows are numbered, by names no frame has;
4. the frames are chained in joins, `JoinCoalesce(true)`;
5. a sort by the keys and then the row numbers, frame by frame, and a select in first
   appearance.

The numbers make the order the same under a memory limit, where a join's is not
promised. Ties come in each frame's order, a row a frame has none for first. Polars'
are its joins' "right_left", which agrees on the cases probed: two null keys, and a
key twice in the first frame.

## 4. Against Polars

**Every mode answers as Polars on its probes,** each held in `TestConcatModes`:

- relaxed Int64 beside Float64, beside text, beside a Boolean; a Date beside a
  Datetime; an Int32 beside an Int64;
- diagonal relaxed; horizontal extend, a frame of no rows too;
- align full, inner, left and right over three frames;
- align on two keys in two orders, null keys, a key twice, keys of two widths, a
  frame alone.

The guide's two examples are Polars' answers exactly.

**§9:**

- strict meets Int32 and Int64;
- relaxed meets Int64 and UInt64 at Int128, and a Decimal narrower;
- `ConcatHorizontal` refuses two heights, Polars' padding being deprecated;
- align refuses a collision Polars silently resolves, and orders its ties by frame.

## 5. Found on the way

**A join under a limit too small for the query blames one key.** Three frames aligned
under 64 KiB, nearly every key held once, were refused: "a single join key has more
build rows than the limit". `oneKeyOver` reads the query's total through the
account's `Check`, and the memory was the other join's. In §1, open. At 256 KiB the
same query spills and answers, which `TestConcatAlignUnderAMemoryLimit` holds.

## 6. The site

`docs/guide/reshaping.md` on `dev`, each new in 0.6.7:

- the relaxed modes, with `Example_reshapeConcatRelaxed`;
- "Aligned by key", with `Example_reshapeConcatAlign`, a price and a volume by day;
- `ConcatHorizontalExtend`, beside `HStack`.

The Polars page maps the modes.

## 7. Tests and teeth

**The tests:**

- `TestConcatModes`: eighteen answers, six refusals, the eager form, the names;
- `TestConcatAlignUnderAMemoryLimit`: 20,000 keys of three frames, a key twice in
  one, under 256 KiB, spilling, equal to the run without a limit.

**Teeth, all biting:**

- relaxed meeting exactly;
- no text supertype;
- a Date unmet with a Datetime;
- extend refused;
- extend lost in a rewrite;
- align unsorted;
- align's ties by key only;
- a collision let through.

**Gate:** test-all 125 ok, race 25 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
