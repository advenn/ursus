# Step 321 — as built

**Tier 2, its second half:** a group-by's groups as frames, and five frame odds, as
Polars 1.44.1 has them (`v0.6.7-scope.md` §6). The scope's tiers are built with it.

## 1. The API

```go
func (g *GroupBy) Groups(ctx context.Context, opts ...CollectOption) iter.Seq2[Partition, error]
func (g *GroupBy) MapGroups(ctx context.Context, fn func(*DataFrame) (*DataFrame, error),
	opts ...CollectOption) (*DataFrame, error)
func (df *DataFrame) Extend(ctx context.Context, other *DataFrame) (*DataFrame, error)
func (df *DataFrame) Glimpse() string
func (df *DataFrame) EstimatedSize() int64
func (lf *LazyFrame) WithColumnsSeq(exprs ...Expr) *LazyFrame
func (lf *LazyFrame) SelectSeq(exprs ...Expr) *LazyFrame
```

**`Groups`,** as Polars' `for key, df in df.group_by(...)`:

- each group a `Partition`, its `Key` a one-row frame and its `Frame` the rows;
- the groups in the order each first appears, Polars' `maintain_order=True`, and
  each group's rows in the frame's order;
- **a group's frame is the rows as the frame holds them,** its own columns only, as
  Polars hands out `df[indices, :]`. A key computed from a column, `v % 2` named `v`,
  is the key's and never the rows': the keys are computed into hidden columns, which
  `PartitionBy` splits by and drops, and the `Key` frame takes the key expressions'
  names;
- a break ends the walk;
- a dynamic or rolling group is refused: its windows overlap, so its rows are not
  split into frames.

**`MapGroups`,** as Polars' `map_groups`:

- `fn` of each group's frame, the answers stacked in the groups' order, which must
  agree in their columns and types, as Polars' vstack requires;
- a frame of no rows is refused, as Polars' "cannot group_by + apply on empty
  DataFrame";
- Polars refuses a key computed from a column; ursus takes one, its frames the rows
  as they are.

**The odds:**

- `Extend` is `ConcatFrames` of the two, strict, a new frame where Polars changes its
  own;
- `Glimpse` prints as Polars' does: a line a column, its name, its type and its
  first ten values, no more and no mark for more, a name past 50 characters cut to 49
  and "…". The values and types print as ursus's frame prints them;
- `EstimatedSize` is the frame's bytes, shared buffers once;
- `WithColumnsSeq` and `SelectSeq` are `WithColumns` and `Select`.

## 2. Found on the way

**The scratchpad draft computed the keys into the frames.** It partitioned
`WithColumns(keys...)`, so a key `v % 2`, named `v`, replaced the column `v` in each
group's frame with the remainders. Polars' probe showed the frames hold the rows as
they are. The keys go to hidden columns now, and a test holds a group's `v` to its
own values.

## 3. Speed, and `PartitionBy` made 3.5 times as fast

A million rows of an Int64 key of 1,000 values, a Float64 and a String, medians of
seven, two rounds interleaved on a quiet machine:

| | first version | final | Polars 1.44.1 |
| --- | --- | --- | --- |
| iterate the 1,000 groups | 116, 136 ms | 35, 37 ms | 72, 51 ms |
| `MapGroups` of each group's `Head(1)` | 126, 128 ms | 48, 56 ms | 53, 49 ms |
| `PartitionBy` alone | 103 ms | 30 ms | |
| `Glimpse` | 0.03 ms | 0.03 ms | 0.05 ms |

**The first version was 2 to 3 times Polars'.** It is `PartitionBy` (step 305), and
the profile found three costs in it:

- **Each part gathered alone,** 3,000 gathers of three columns on one thread, 55% of
  the time;
- **its parts numbered by a Go map** of encoded keys, 20%;
- **a bit read a row,** in each gather's validity: see below, 15%.

**Now:**

1. the parts are numbered by `kernel.FirstSeenKeys`, step 320's kernel taking several
   columns, encoded together;
2. the rows are bucketed by part, a counting sort, each part's a slice of one array;
3. the parts are gathered in parallel, each part's frame its own, so that one kept
   does not keep the others.

What is left is the gathers themselves, which Polars makes as well, and in
`MapGroups` the caller's function: `Head` on each group plans and runs a query,
about 20 µs a call. `feature-gaps.md` §8 records that.

**Found on the way: a frame collected from several batches lost its "no nulls".**
`concatColumn` built each column's validity with a `bitmap.Builder`, which gives a
bitmap with every bit set and does not say so. So every kernel after a `Collect` of
several batches read a bit a row where it could have read none; step 320 met it in
its own kernel. Parts that are all valid now concatenate to a column that says so,
`bitmap.AllSet`, and `TestConcatSaysAllValid` holds it.

## 4. Tests

**`TestGroupsAsFrames`:**

- the groups of a key, their order and heights;
- a break after the first;
- `MapGroups` adding a group's sum to its rows;
- a key computed from a column: the frames' columns `k,v` and `v`'s values their own,
  the keys 1 and 0;
- refused: a dynamic group, answers of two shapes, no groups;
- `Extend`, `WithColumnsSeq` and `SelectSeq`, `EstimatedSize`;
- `Glimpse` of two columns, and of a long name and twelve values;
- `PartitionBy`'s own tests, unchanged, over its new numbering and gathers, and
  under the race detector;
- **`kernel.TestConcatSaysAllValid`:** all-valid parts concatenate to a column that
  says so, and one null among them keeps its bit in its place.

## 5. Docs

- **Aggregation:** "A group's rows as a frame".
- **Coming from Polars:** two rows.
- **The CHANGELOG, `feature-gaps.md`, `polars-parity.md`, the scope:** the features;
  the scope's tiers built.

## 6. Teeth, all 11 biting

- the hidden keys left in the frames;
- the keys under their hidden names;
- a break ignored;
- only the first group's answer;
- no groups not refused;
- names cut past 51 characters;
- eleven values;
- no size;
- `WithColumnsSeq` doing nothing;
- `PartitionBy`'s parts in last-seen order;
- `concatColumn` saying a column with nulls is all valid, which 19 tests caught.

One first failed the build, "the keys under their hidden names", which left a
variable unused, and was rewritten.

**Gate:** test-all 125 ok, race 25 ok (the root package 589 s here), levels, vet ×3
and the bench engine tests clean; the 22 PDS-H answers match DuckDB.
