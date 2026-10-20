package ursus

import "context"

// The rows of a frame drawn (step 292, R4 of v0.6.7-scope.md): Polars'
// DataFrame.sample, here on both frames.
//
// Every column's values must be of the same rows, so the rows are drawn once, as
// indices: the frame's row index sampled by Expr.SampleN's kernel, each drawn index
// numbered by its place in the sample, joined back to the frame's rows on the index,
// and sorted by that place. The sort keeps a shuffle's order where a join would not
// promise it, under a memory limit.
//
// The join and the sort spill. The draw holds the frame's row indices, one UInt32 a
// row, as one list, as an implode holds a group's values, and refuses past a limit
// smaller than they are: 80 KB for 20,000 rows, where the rows themselves spill.
const (
	sampleRow = "\x00sample_row"
	samplePos = "\x00sample_pos"
)

// SampleN is n of the frame's rows drawn, every column's values of the same rows:
// Polars' DataFrame.sample(n, with_replacement, shuffle, seed). Without replacement
// more rows than the frame has are refused, as Polars refuses them. Unshuffled, the
// rows keep their order, where Polars' unshuffled draw comes in its generator's
// order. The draw is ursus's generator's, seeded by seed and the frame's height, so
// the rows are not Polars' (v0.6.6-scope.md §6.3), and one seed draws the same rows of
// the same frame each time.
func (lf *LazyFrame) SampleN(n int, withReplacement, shuffle bool, seed uint64) *LazyFrame {
	return lf.sampleRows(func(row Expr) Expr { return row.SampleN(n, withReplacement, shuffle, seed) })
}

// SampleFraction is SampleN of a fraction of the rows, truncated, as Polars counts it:
// 0.4 of six rows is two.
func (lf *LazyFrame) SampleFraction(fraction float64, withReplacement, shuffle bool, seed uint64) *LazyFrame {
	return lf.sampleRows(func(row Expr) Expr { return row.SampleFraction(fraction, withReplacement, shuffle, seed) })
}

// Shuffle is every row in an order drawn by seed: Polars' sample(fraction=1,
// shuffle=True).
func (lf *LazyFrame) Shuffle(seed uint64) *LazyFrame {
	return lf.SampleFraction(1, false, true, seed)
}

func (lf *LazyFrame) sampleRows(draw func(row Expr) Expr) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	rows := lf.WithRowIndex(sampleRow, 0)
	picked := rows.Select(draw(Col(sampleRow)).Alias(sampleRow)).WithRowIndex(samplePos, 0)
	return picked.Join(rows, JoinOn(Col(sampleRow)), JoinHow(JoinLeft)).
		Sort(Asc(Col(samplePos))).Drop(sampleRow, samplePos)
}

// SampleN, SampleFraction and Shuffle draw the frame's rows: LazyFrame's.
func (df *DataFrame) SampleN(ctx context.Context, n int, withReplacement, shuffle bool, seed uint64) (*DataFrame, error) {
	return df.Lazy().SampleN(n, withReplacement, shuffle, seed).Collect(ctx)
}

func (df *DataFrame) SampleFraction(ctx context.Context, fraction float64, withReplacement, shuffle bool,
	seed uint64) (*DataFrame, error) {
	return df.Lazy().SampleFraction(fraction, withReplacement, shuffle, seed).Collect(ctx)
}

func (df *DataFrame) Shuffle(ctx context.Context, seed uint64) (*DataFrame, error) {
	return df.Lazy().Shuffle(seed).Collect(ctx)
}
