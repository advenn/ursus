package ursus

import (
	"context"

	"github.com/advenn/ursus/dtype"
)

// A frame's aggregates (step 300, C3 of v0.6.7-scope.md): Polars' DataFrame.sum,
// mean, median, min, max, std, var, quantile, product and null_count, each one row.
//
// Every column is kept, in its place, as Polars keeps it (§7.5): a column the
// aggregate takes is aggregated, and one it does not take is a null of its own type,
// a product's of the Null type, as Polars answers. Which takes which is Polars':
//
//   - sum: numbers, Booleans (their trues), durations;
//   - mean and median: numbers, Booleans (as 0 and 1), instants, durations, times;
//   - min and max: every type with an order, text and Booleans among them, but a
//     List or a Struct;
//   - std and var: numbers and Booleans;
//   - product: numbers and Booleans;
//   - quantile: numbers, instants, durations and times, and not a Boolean;
//   - null_count: every column.
//
// Each column's answer is the expression aggregate's, so its type is ursus's: an
// integer sum an Int128, a product a Float64, a null count a Uint64. feature-gaps.md
// §9 records where those differ from Polars'. GroupBy(k).Sum() and its kin are not
// changed: they keep only the columns they take.
//
// The answer is a Select of aggregates, so it spills and streams as a keyless
// group-by does.

// Sum is each column's sum, one row: Polars' DataFrame.sum. A column that is not a
// number, a Boolean or a duration is null.
func (lf *LazyFrame) Sum() *LazyFrame {
	return lf.frameAgg(func(t DataType) (func(Expr) Expr, bool) {
		return Expr.Sum, t.IsNumeric() || t.IsBool() || t.ID() == dtype.TypeDuration
	})
}

// Mean is each column's mean, one row: Polars' DataFrame.mean. A Boolean's is the
// share of its trues, and a Date's a Datetime(us), as Polars'.
func (lf *LazyFrame) Mean() *LazyFrame {
	return lf.frameAgg(func(t DataType) (func(Expr) Expr, bool) {
		return asFloatFor(t, Expr.Mean), t.IsNumeric() || t.IsBool() || t.IsTemporal()
	})
}

// Median is each column's median, one row: Polars' DataFrame.median.
func (lf *LazyFrame) Median() *LazyFrame {
	return lf.frameAgg(func(t DataType) (func(Expr) Expr, bool) {
		return asFloatFor(t, Expr.Median), t.IsNumeric() || t.IsBool() || t.IsTemporal()
	})
}

// Min is each column's least value, one row: Polars' DataFrame.min. A List or a
// Struct is null.
func (lf *LazyFrame) Min() *LazyFrame {
	return lf.frameAgg(func(t DataType) (func(Expr) Expr, bool) { return Expr.Min, ordered(t) })
}

// Max is each column's greatest value, one row: Polars' DataFrame.max.
func (lf *LazyFrame) Max() *LazyFrame {
	return lf.frameAgg(func(t DataType) (func(Expr) Expr, bool) { return Expr.Max, ordered(t) })
}

// Std is each column's standard deviation with ddof, one row: Polars'
// DataFrame.std(ddof). A column that is not a number or a Boolean is null, an
// instant's too, as Polars'.
func (lf *LazyFrame) Std(ddof int) *LazyFrame {
	return lf.frameAgg(func(t DataType) (func(Expr) Expr, bool) {
		return asFloatFor(t, func(e Expr) Expr { return e.Std(ddof) }), t.IsNumeric() || t.IsBool()
	})
}

// Var is each column's variance with ddof, one row: Polars' DataFrame.var(ddof).
func (lf *LazyFrame) Var(ddof int) *LazyFrame {
	return lf.frameAgg(func(t DataType) (func(Expr) Expr, bool) {
		return asFloatFor(t, func(e Expr) Expr { return e.Var(ddof) }), t.IsNumeric() || t.IsBool()
	})
}

// Quantile is each column's quantile q, one row: Polars' DataFrame.quantile(q,
// interpolation). An instant's, a duration's and a time's is one of them; a
// Boolean's is null, as Polars'.
func (lf *LazyFrame) Quantile(q float64, interp Interpolation) *LazyFrame {
	return lf.frameAgg(func(t DataType) (func(Expr) Expr, bool) {
		return func(e Expr) Expr { return e.Quantile(q, interp) }, t.IsNumeric() || t.IsTemporal()
	})
}

// Product is each column's product, one row: Polars' DataFrame.product. A Boolean's
// is of its 0s and 1s. A column that is not a number or a Boolean is a null of the
// Null type, as Polars answers it.
func (lf *LazyFrame) Product() *LazyFrame {
	return lf.frameAggAs(func(t DataType) (func(Expr) Expr, bool) {
		if t.IsBool() {
			return func(e Expr) Expr { return e.Cast(Int64).Product() }, true
		}
		return Expr.Product, t.IsNumeric()
	}, func(DataType) DataType { return NullT })
}

// NullCount is each column's count of nulls, one row: Polars' DataFrame.null_count.
func (lf *LazyFrame) NullCount() *LazyFrame {
	return lf.frameAgg(func(DataType) (func(Expr) Expr, bool) { return Expr.NullCount, true })
}

// asFloatFor gives a Boolean column to agg as 0 and 1, which Polars' mean, median,
// std and var take; ursus's refuse a Boolean.
func asFloatFor(t DataType, agg func(Expr) Expr) func(Expr) Expr {
	if !t.IsBool() {
		return agg
	}
	return func(e Expr) Expr { return agg(e.Cast(Float64)) }
}

// ordered is the types Polars' min and max take: every type with an order but a
// List and a Struct.
func ordered(t DataType) bool {
	return t.IsOrdered() && t.ID() != dtype.TypeList && t.ID() != dtype.TypeStruct
}

func (lf *LazyFrame) frameAgg(take func(DataType) (func(Expr) Expr, bool)) *LazyFrame {
	return lf.frameAggAs(take, func(t DataType) DataType { return t })
}

// frameAggAs is a Select of each column's aggregate, or of a null of the type
// nullAs gives where take does not take the column, in the frame's order.
func (lf *LazyFrame) frameAggAs(take func(DataType) (func(Expr) Expr, bool),
	nullAs func(DataType) DataType) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	s, err := lf.schemaNow()
	if err != nil {
		return &LazyFrame{err: err}
	}
	if s.Len() == 0 {
		return lf
	}
	exprs := make([]Expr, 0, s.Len())
	for _, f := range s.All() {
		if agg, ok := take(f.Type); ok {
			exprs = append(exprs, agg(Col(f.Name)).Alias(f.Name))
			continue
		}
		exprs = append(exprs, Null(nullAs(f.Type)).Alias(f.Name))
	}
	return lf.Select(exprs...)
}

// Sum, Mean, Median, Min, Max, Std, Var, Quantile, Product and NullCount are a
// DataFrame's aggregates: LazyFrame's.
func (df *DataFrame) Sum(ctx context.Context) (*DataFrame, error) {
	return df.Lazy().Sum().Collect(ctx)
}

func (df *DataFrame) Mean(ctx context.Context) (*DataFrame, error) {
	return df.Lazy().Mean().Collect(ctx)
}

func (df *DataFrame) Median(ctx context.Context) (*DataFrame, error) {
	return df.Lazy().Median().Collect(ctx)
}

func (df *DataFrame) Min(ctx context.Context) (*DataFrame, error) {
	return df.Lazy().Min().Collect(ctx)
}

func (df *DataFrame) Max(ctx context.Context) (*DataFrame, error) {
	return df.Lazy().Max().Collect(ctx)
}

func (df *DataFrame) Std(ctx context.Context, ddof int) (*DataFrame, error) {
	return df.Lazy().Std(ddof).Collect(ctx)
}

func (df *DataFrame) Var(ctx context.Context, ddof int) (*DataFrame, error) {
	return df.Lazy().Var(ddof).Collect(ctx)
}

func (df *DataFrame) Quantile(ctx context.Context, q float64, interp Interpolation) (*DataFrame, error) {
	return df.Lazy().Quantile(q, interp).Collect(ctx)
}

func (df *DataFrame) Product(ctx context.Context) (*DataFrame, error) {
	return df.Lazy().Product().Collect(ctx)
}

func (df *DataFrame) NullCount(ctx context.Context) (*DataFrame, error) {
	return df.Lazy().NullCount().Collect(ctx)
}
