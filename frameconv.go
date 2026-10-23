package ursus

import (
	"context"
	"math"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/uerr"
)

// Polars' frame conveniences (step 327): corr, gather, row, item, get_column_index,
// replace_column, drop_in_place, the horizontal reductions, fold, to_series and
// to_struct; and a lazy frame's columns, dtypes and width.

// Corr is the Pearson correlation of every pair of columns, as Polars' DataFrame.corr:
// a Float64 column a column, its row i the correlation with column i. Every column
// must be a number or a Bool. Polars computes it through NumPy's corrcoef, and so
// does this, to within rounding: a column holding a null is NaN throughout, its own
// correlation too, as is one that does not vary, and each value is clipped to
// [-1, 1]. Corr of two expressions differs: it skips the rows where either is null.
func (df *DataFrame) Corr(ctx context.Context) (*DataFrame, error) {
	for _, f := range df.batch.Schema().All() {
		if !f.Type.IsNumeric() && f.Type.ID() != dtype.TypeBool {
			return nil, uerr.New(uerr.KindType, "corr", "corr reads numbers, and %q is %s", f.Name, f.Type).
				Hint("select the numeric columns first: selector.Numeric()")
		}
	}
	k, n := df.Width(), df.Height()
	// The columns without a null, as Float64s: one with a null is NaN throughout, as
	// NumPy's.
	var live []int
	vals := make([][]float64, k)
	for i, c := range df.batch.Columns() {
		if c.NullCount() > 0 {
			continue
		}
		f, err := kernel.Cast(c.Name(), dtype.Float64, true, c)
		if err != nil {
			return nil, err
		}
		if vals[i], err = data.Values[float64](f); err != nil {
			return nil, err
		}
		live = append(live, i)
	}
	cov, err := covariances(ctx, vals, live, n)
	if err != nil {
		return nil, err
	}
	cols := make([]*data.Column, k)
	for j := range k {
		out := make([]float64, k)
		for i := range k {
			out[i] = math.NaN()
		}
		cols[j] = data.NewFixedOwned(df.batch.Column(j).Name(), dtype.Float64, out, bitmap.AllSet(k))
	}
	for a, i := range live {
		out, _ := data.Values[float64](cols[i])
		for b, j := range live {
			// As NumPy divides: by the one deviation, then the other; then clipped,
			// a NaN kept.
			x := cov[a*len(live)+b] / math.Sqrt(cov[b*len(live)+b]) / math.Sqrt(cov[a*len(live)+a])
			out[j] = max(-1, min(1, x))
		}
	}
	return frameOfColumns(cols, k)
}

// The rows covariances sums a block at a time, a block's centred values beside each
// other in the cache, and a span at a time per worker: a fixed number of rows, so
// that the sums, a span's in order, do not depend on how many workers there are.
const (
	covBlock = 1024
	covSpan  = 64 * covBlock
)

// covariances is NumPy's covariance of the columns vals[live], a row-major matrix:
// each pair's centred products summed, times 1/(n-1), or times 1/0 where there are
// not two rows. Each column is read twice, for its mean and for the products.
func covariances(ctx context.Context, vals [][]float64, live []int, n int) ([]float64, error) {
	k := len(live)
	spans := (n + covSpan - 1) / covSpan

	// The means, a span's sums added in order.
	part := make([]float64, spans*k)
	err := inParallel(ctx, spans, func(sp int) error {
		lo, hi := sp*covSpan, min(n, (sp+1)*covSpan)
		for c, col := range live {
			part[sp*k+c] = sum4(vals[col][lo:hi])
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	mean := make([]float64, k)
	for c := range k {
		for sp := range spans {
			mean[c] += part[sp*k+c]
		}
		mean[c] /= float64(n)
	}

	// The centred products, a pair's upper triangle by block.
	pairs := k * (k + 1) / 2
	prod := make([]float64, spans*pairs)
	err = inParallel(ctx, spans, func(sp int) error {
		buf := make([]float64, k*covBlock)
		acc := prod[sp*pairs : (sp+1)*pairs]
		for lo, hi := sp*covSpan, min(n, (sp+1)*covSpan); lo < hi; lo += covBlock {
			m := min(hi-lo, covBlock)
			for c, col := range live {
				dst, src, mu := buf[c*covBlock:c*covBlock+m], vals[col][lo:lo+m], mean[c]
				for r := range dst {
					dst[r] = src[r] - mu
				}
			}
			p := 0
			for i := range k {
				x := buf[i*covBlock : i*covBlock+m]
				for j := i; j < k; j++ {
					acc[p] += dot4(x, buf[j*covBlock:j*covBlock+m])
					p++
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	scale := 1 / float64(max(n-1, 0))
	cov := make([]float64, k*k)
	p := 0
	for i := range k {
		for j := i; j < k; j++ {
			s := 0.0
			for sp := range spans {
				s += prod[sp*pairs+p]
			}
			cov[i*k+j], cov[j*k+i] = s*scale, s*scale
			p++
		}
	}
	return cov, nil
}

// sum4 is the sum of v in four lanes, which a long column sums faster, and nearer the
// exact sum, than one running total does.
func sum4(v []float64) float64 {
	var s0, s1, s2, s3 float64
	r := 0
	for ; r+4 <= len(v); r += 4 {
		s0 += v[r]
		s1 += v[r+1]
		s2 += v[r+2]
		s3 += v[r+3]
	}
	for ; r < len(v); r++ {
		s0 += v[r]
	}
	return (s0 + s1) + (s2 + s3)
}

// dot4 is the sum of x[r]*y[r], in four lanes as sum4 sums.
func dot4(x, y []float64) float64 {
	y = y[:len(x)]
	var s0, s1, s2, s3 float64
	r := 0
	for ; r+4 <= len(x); r += 4 {
		s0 += x[r] * y[r]
		s1 += x[r+1] * y[r+1]
		s2 += x[r+2] * y[r+2]
		s3 += x[r+3] * y[r+3]
	}
	for ; r < len(x); r++ {
		s0 += x[r] * y[r]
	}
	return (s0 + s1) + (s2 + s3)
}

// Gather is the rows at indices, in their order, as Polars' DataFrame.gather: a
// negative index counts from the end, and one past either end is refused, or with
// nullOnOOB is a row of nulls.
func (df *DataFrame) Gather(ctx context.Context, indices []int, nullOnOOB bool) (_ *DataFrame, err error) {
	defer uerr.Catch(&err, "gather")
	n := df.Height()
	sel := make([]int32, len(indices))
	for j, i := range indices {
		at := i
		if at < 0 {
			at += n
		}
		switch {
		case at >= 0 && at < n:
			sel[j] = int32(at)
		case nullOnOOB:
			sel[j] = kernel.NullIndex
		default:
			return nil, uerr.New(uerr.KindValue, "gather", "gather index %d is out of bounds for %d rows", i, n).
				Hint("nullOnOOB makes it a row of nulls")
		}
	}
	cols, err := takeEach(ctx, df.batch.Columns(), sel)
	if err != nil {
		return nil, err
	}
	return frameOfColumns(cols, len(sel))
}

// takeChunk is the rows of a long selection Gather takes at a time.
const takeChunk = 1 << 14

// takeEach is each column's rows at sel: a short selection's columns in parallel,
// and a long one's a column at a time, its chunks in parallel, so that the column the
// random reads land in is the one in the cache.
func takeEach(ctx context.Context, in []*data.Column, sel []int32) ([]*data.Column, error) {
	cols := make([]*data.Column, len(in))
	if len(sel) <= takeChunk {
		err := inParallel(ctx, len(in), func(j int) (err error) {
			cols[j], err = kernel.Take(in[j], sel)
			return err
		})
		return cols, err
	}
	run := func(n int, fn func(k int) error) error { return inParallel(ctx, n, fn) }
	for j, c := range in {
		var err error
		if cols[j], err = kernel.TakeChunked(c, sel, takeChunk, run); err != nil {
			return nil, err
		}
	}
	return cols, nil
}

// inParallel runs fn for 0 to n-1 on up to GOMAXPROCS goroutines, each taking the
// next index; the first error stops it, and is its answer.
func inParallel(ctx context.Context, n int, fn func(i int) error) error {
	workers := min(runtime.GOMAXPROCS(0), n)
	if workers <= 1 {
		for i := range n {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := fn(i); err != nil {
				return err
			}
		}
		return nil
	}
	var (
		wg    sync.WaitGroup
		took  atomic.Int64
		fail  error
		once  sync.Once
		abort atomic.Bool
	)
	for range workers {
		wg.Go(func() {
			for i := int(took.Add(1)) - 1; i < n && !abort.Load(); i = int(took.Add(1)) - 1 {
				err := ctx.Err()
				if err == nil {
					err = fn(i)
				}
				if err != nil {
					once.Do(func() { fail = err })
					abort.Store(true)
					return
				}
			}
		})
	}
	wg.Wait()
	return fail
}

// rowAt is the row index i names, a negative one from the end.
func (df *DataFrame) rowAt(op string, i int) (int, error) {
	at := i
	if at < 0 {
		at += df.Height()
	}
	if at < 0 || at >= df.Height() {
		return 0, uerr.New(uerr.KindValue, op, "index %d is out of bounds for %d rows", i, df.Height())
	}
	return at, nil
}

// Row is row i's values, as Polars' DataFrame.row: each a Go value as ToMaps reads
// it, a null nil; a negative i counts from the end.
func (df *DataFrame) Row(i int) ([]any, error) {
	at, err := df.rowAt("row", i)
	if err != nil {
		return nil, err
	}
	out := make([]any, df.Width())
	for j, c := range df.batch.Columns() {
		read, err := cellReader(c)
		if err != nil {
			return nil, err
		}
		out[j] = read(at)
	}
	return out, nil
}

// RowNamed is Row as a map from each column's name to its value: Polars'
// row(named=True).
func (df *DataFrame) RowNamed(i int) (map[string]any, error) {
	vals, err := df.Row(i)
	if err != nil {
		return nil, err
	}
	out := make(map[string]any, len(vals))
	for j, name := range df.Columns() {
		out[name] = vals[j]
	}
	return out, nil
}

// RowWhere is the one row pred holds of, as Polars' row(by_predicate=): none, or
// several, is refused.
func (df *DataFrame) RowWhere(ctx context.Context, pred Expr) ([]any, error) {
	hit, err := df.Lazy().Filter(pred).Collect(ctx)
	if err != nil {
		return nil, err
	}
	switch hit.Height() {
	case 1:
		return hit.Row(0)
	case 0:
		return nil, uerr.New(uerr.KindValue, "row", "predicate %s returned no rows", pred)
	}
	return nil, uerr.New(uerr.KindValue, "row", "predicate %s returned %d rows", pred, hit.Height()).
		Hint("Filter gives every row it holds of")
}

// Item is the frame's one value, as Polars' DataFrame.item(): a frame of one row and
// one column.
func (df *DataFrame) Item() (any, error) {
	if df.Height() != 1 || df.Width() != 1 {
		return nil, uerr.New(uerr.KindValue, "item",
			"Item needs a frame of a single element, and its shape is (%d, %d)", df.Height(), df.Width()).
			Hint("ItemAt names a row and a column")
	}
	return df.ItemAt(0, df.Columns()[0])
}

// ItemAt is the value at row of column, as Polars' item(row, column): a negative row
// counts from the end.
func (df *DataFrame) ItemAt(row int, column string) (any, error) {
	j, err := df.GetColumnIndex(column)
	if err != nil {
		return nil, err
	}
	at, err := df.rowAt("item", row)
	if err != nil {
		return nil, err
	}
	read, err := cellReader(df.batch.Column(j))
	if err != nil {
		return nil, err
	}
	return read(at), nil
}

// GetColumnIndex is column name's place, as Polars' get_column_index; a name the
// frame lacks is refused.
func (df *DataFrame) GetColumnIndex(name string) (int, error) {
	if j := df.batch.Schema().IndexOf(name); j >= 0 {
		return j, nil
	}
	return 0, uerr.UnknownColumn("get_column_index", name, df.Columns())
}

// columnAt is the column place i names, a negative one from the end.
func (df *DataFrame) columnAt(op string, i int) (int, error) {
	at := i
	if at < 0 {
		at += df.Width()
	}
	if at < 0 || at >= df.Width() {
		return 0, uerr.New(uerr.KindValue, op, "column index %d is out of bounds for %d columns", i, df.Width())
	}
	return at, nil
}

// ReplaceColumn is the frame with c in place of the column at place i, under c's
// name, as Polars' replace_column, which ursus answers with a new frame where Polars
// changes its own. A negative i counts from the end; c must be as long as the frame,
// and its name another column's is refused, where Polars' takes it.
func (df *DataFrame) ReplaceColumn(i int, c *Column) (*DataFrame, error) {
	at, err := df.columnAt("replace_column", i)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, uerr.New(uerr.KindValue, "replace_column", "the column is nil")
	}
	if c.Len() != df.Height() {
		return nil, uerr.New(uerr.KindSchema, "replace_column",
			"unable to replace a column, series length %d doesn't match the frame's height %d", c.Len(), df.Height())
	}
	cols := append([]*data.Column(nil), df.batch.Columns()...)
	cols[at] = c
	return frameOfColumns(cols, df.Height())
}

// DropInPlace is column name, and the frame without it, as Polars' drop_in_place,
// which ursus answers with a new frame where Polars changes its own.
func (df *DataFrame) DropInPlace(name string) (*Column, *DataFrame, error) {
	j, err := df.GetColumnIndex(name)
	if err != nil {
		return nil, nil, err
	}
	cols := make([]*data.Column, 0, df.Width()-1)
	for k, c := range df.batch.Columns() {
		if k != j {
			cols = append(cols, c)
		}
	}
	return df.batch.Column(j), &DataFrame{batch: data.NewBatchRows(df.batch.Schema().Drop(name), cols, df.Height())}, nil
}

// SumHorizontal and MeanHorizontal are each row's sum and mean across the columns, as
// Polars' frame sum_horizontal and mean_horizontal, named "sum" and "mean". They are
// the expressions SumHorizontal and MeanHorizontal over every column, nulls skipped;
// with ignoreNulls false, a row holding a null is null.
func (df *DataFrame) SumHorizontal(ctx context.Context, ignoreNulls bool) (*Column, error) {
	return df.horizontal(ctx, SumHorizontal(All()), "sum", ignoreNulls)
}

func (df *DataFrame) MeanHorizontal(ctx context.Context, ignoreNulls bool) (*Column, error) {
	return df.horizontal(ctx, MeanHorizontal(All()), "mean", ignoreNulls)
}

// MinHorizontal and MaxHorizontal are each row's least and greatest value across the
// columns, as Polars' frame min_horizontal and max_horizontal, named "min" and "max":
// the expressions MinHorizontal and MaxHorizontal over every column.
func (df *DataFrame) MinHorizontal(ctx context.Context) (*Column, error) {
	return df.horizontal(ctx, MinHorizontal(All()), "min", true)
}

func (df *DataFrame) MaxHorizontal(ctx context.Context) (*Column, error) {
	return df.horizontal(ctx, MaxHorizontal(All()), "max", true)
}

// horizontal is e over the frame, named name; with ignoreNulls false, null where any
// column is, its validity ANDed with each column's, which costs nothing for a column
// without a null.
func (df *DataFrame) horizontal(ctx context.Context, e Expr, name string, ignoreNulls bool) (*Column, error) {
	out, err := df.Select(ctx, e.Alias(name))
	if err != nil {
		return nil, err
	}
	c := out.ColumnAt(0)
	if !ignoreNulls {
		valid := c.Validity()
		for _, in := range df.batch.Columns() {
			if in.NullCount() > 0 {
				valid = bitmap.And(valid, in.Validity())
			}
		}
		c = c.WithValidity(valid)
	}
	return c, nil
}

// Fold is fn folded across the columns, row by row, as Polars' frame fold: the first
// column the accumulator, then fn of it and each column after, in their order, named
// after the first; Reduce over every column. fn builds an expression, which runs
// over the frame once.
func (df *DataFrame) Fold(ctx context.Context, fn func(acc, x Expr) Expr) (*Column, error) {
	out, err := df.Select(ctx, Reduce(fn, All()))
	if err != nil {
		return nil, err
	}
	return out.ColumnAt(0), nil
}

// ToSeries is the column at place i, as Polars' to_series: a negative i counts from
// the end.
func (df *DataFrame) ToSeries(i int) (*Column, error) {
	at, err := df.columnAt("to_series", i)
	if err != nil {
		return nil, err
	}
	return df.batch.Column(at), nil
}

// ToStruct is the frame as one Struct column named name, a field a column, as Polars'
// to_struct; no row of it null.
func (df *DataFrame) ToStruct(name string) *Column {
	return data.NewStruct(name, df.batch.Columns(), bitmap.View{})
}

// Columns, DTypes and Width are the lazy frame's column names, their types, and how
// many there are, as Polars' LazyFrame properties: read from its schema, as
// CollectSchema resolves it, with no data read.
func (lf *LazyFrame) Columns(ctx context.Context) ([]string, error) {
	s, err := lf.CollectSchema(ctx)
	if err != nil {
		return nil, err
	}
	return s.Names(), nil
}

func (lf *LazyFrame) DTypes(ctx context.Context) ([]DataType, error) {
	s, err := lf.CollectSchema(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]DataType, 0, s.Len())
	for _, f := range s.All() {
		out = append(out, f.Type)
	}
	return out, nil
}

func (lf *LazyFrame) Width(ctx context.Context) (int, error) {
	s, err := lf.CollectSchema(ctx)
	if err != nil {
		return 0, err
	}
	return s.Len(), nil
}
