package ursus

import (
	"context"
	"slices"
	"strconv"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// ValueCounts counts the rows of each distinct value of by, most frequent first,
// in a column named "count": Polars' value_counts, sorted.
//
//	lf.ValueCounts(Col("city"))                  // city, count
//	lf.ValueCounts(Col("city"), Col("year"))     // each combination
//
// Ties keep the order in which their values first appear, and a null is a value
// like any other, as a group-by's key. It is GroupBy(by...).MaintainOrder(), Len, and
// a stable Sort by count, descending. A key named "count" is refused, as Polars
// refuses it; alias it first.
func (lf *LazyFrame) ValueCounts(by ...Expr) *LazyFrame {
	if len(by) == 0 {
		return &LazyFrame{err: uerr.New(uerr.KindSchema, "value_counts",
			"ValueCounts requires at least one expression")}
	}
	return lf.GroupBy(by...).MaintainOrder().Agg(Len().Alias("count")).Sort(Desc(Col("count")))
}

// ValueCounts is LazyFrame.ValueCounts, collected.
func (df *DataFrame) ValueCounts(ctx context.Context, by ...Expr) (*DataFrame, error) {
	return df.Lazy().ValueCounts(by...).Collect(ctx)
}

// Describe summarises each column, one statistic a row: Polars' describe.
//
//	statistic   count, null_count, mean, std, min, 25%, 50%, 75%, max
//
// A number's column is a Float64: its mean, its standard deviation (ddof 1), and its
// percentiles, each the nearest value, as Polars takes them. A Bool's is a Float64
// too, with true as 1: its mean is the share of trues, and its min and max say
// whether there is a false and a true. Any other column is a String, holding its
// counts, and for a String or a temporal column its min and max, formatted as a Cast
// to String formats them. A statistic a column has no answer for is null.
//
// percentiles replace 0.25, 0.5 and 0.75, each between 0 and 1. It runs the query
// once, through one aggregate per statistic and column, and is therefore eager.
func (lf *LazyFrame) Describe(ctx context.Context, percentiles ...float64) (*DataFrame, error) {
	if len(percentiles) == 0 {
		percentiles = []float64{0.25, 0.5, 0.75}
	}
	for _, p := range percentiles {
		if !(p >= 0 && p <= 1) {
			return nil, uerr.New(uerr.KindValue, "describe",
				"percentile %v is not between 0 and 1", p)
		}
	}
	percentiles = slices.Clone(percentiles)
	slices.Sort(percentiles)
	percentiles = slices.Compact(percentiles)

	schema, err := lf.CollectSchema(ctx)
	if err != nil {
		return nil, err
	}
	if schema.Has("statistic") {
		return nil, uerr.New(uerr.KindSchema, "describe",
			"the frame has a column named \"statistic\", which names Describe's own").
			Hint("rename it first")
	}
	stats := []string{"count", "null_count", "mean", "std", "min"}
	for _, p := range percentiles {
		stats = append(stats, strconv.FormatFloat(p*100, 'g', -1, 64)+"%")
	}
	stats = append(stats, "max")

	// One aggregate per column and statistic it has, named by both.
	var aggs []Expr
	var outs []describedColumn
	for i, f := range schema.All() {
		d := describedColumn{name: f.Name, number: f.Type.IsNumeric() || f.Type.IsBool()}
		key := func(stat string) string { return strconv.Itoa(i) + "\x00" + stat }
		c := Col(f.Name)
		add := func(stat string, e Expr) {
			d.stats = append(d.stats, stat)
			aggs = append(aggs, e.Alias(key(stat)))
		}
		to := Float64
		if !d.number {
			to = String
		}
		add("count", c.Count().Cast(to))
		add("null_count", c.NullCount().Cast(to))
		switch {
		case d.number:
			x := c.Cast(Float64)
			add("mean", x.Mean())
			add("min", x.Min())
			add("max", x.Max())
			if !f.Type.IsBool() {
				add("std", x.Std(1))
				for j, p := range percentiles {
					add(stats[5+j], x.Quantile(p, InterpNearest))
				}
			}
		case f.Type.ID() == dtype.TypeString || f.Type.IsTemporal():
			add("min", c.Min().Cast(String))
			add("max", c.Max().Cast(String))
		}
		d.key = key
		outs = append(outs, d)
	}

	cols := []*data.Column{data.NewString("statistic", stats, bitmap.AllSet(len(stats)))}
	if len(aggs) > 0 {
		one, err := lf.GroupBy().Agg(aggs...).Collect(ctx)
		if err != nil {
			return nil, err
		}
		for _, d := range outs {
			c, err := d.column(one, stats)
			if err != nil {
				return nil, err
			}
			cols = append(cols, c)
		}
	}
	fields := make([]dtype.Field, len(cols))
	for i, c := range cols {
		fields[i] = dtype.Field{Name: c.Name(), Type: c.DType(), Nullable: true}
	}
	out, err := dtype.NewSchema(fields...)
	if err != nil {
		return nil, err
	}
	b, err := data.NewBatch(out, cols)
	if err != nil {
		return nil, err
	}
	return &DataFrame{batch: b}, nil
}

// Describe is LazyFrame.Describe over the frame.
func (df *DataFrame) Describe(ctx context.Context, percentiles ...float64) (*DataFrame, error) {
	return df.Lazy().Describe(ctx, percentiles...)
}

// describedColumn is one input column of Describe: the statistics it has, and where
// the one-row aggregate holds each.
type describedColumn struct {
	name   string
	number bool
	stats  []string
	key    func(stat string) string
}

// column reads the column's statistics out of the one-row aggregate, in the order of
// stats, null where it has none.
func (d describedColumn) column(one *DataFrame, stats []string) (*data.Column, error) {
	valid := make([]bool, len(stats))
	if d.number {
		vals := make([]float64, len(stats))
		for i, s := range stats {
			if !slices.Contains(d.stats, s) {
				continue
			}
			v, ok, err := one.At[float64](0, d.key(s))
			if err != nil {
				return nil, err
			}
			vals[i], valid[i] = v, ok
		}
		return data.NewFixed(d.name, dtype.Float64, vals, validityOf(valid)), nil
	}
	vals := make([]string, len(stats))
	for i, s := range stats {
		if !slices.Contains(d.stats, s) {
			continue
		}
		v, ok, err := one.At[string](0, d.key(s))
		if err != nil {
			return nil, err
		}
		vals[i], valid[i] = v, ok
	}
	return data.NewString(d.name, vals, validityOf(valid)), nil
}

func validityOf(valid []bool) bitmap.View {
	b := bitmap.NewBuilder(len(valid))
	for _, v := range valid {
		b.Append(v)
	}
	return b.Finish()
}
