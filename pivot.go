package ursus

import (
	"context"
	"slices"
	"strconv"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/uerr"
)

// Pivot and Transpose (step 229) are eager only: each makes columns of the frame's
// VALUES, so its schema is known only once the data is, which a lazy plan cannot
// allow. Each reads the frame it is given, and builds its answer with the kernels.

// PivotOptions says how Pivot spreads a frame.
type PivotOptions struct {
	// On is the column whose values name the new columns.
	On string
	// Index is the columns whose values make the rows, and Values those spread into
	// the new columns. Either may be left empty: it is then every column that is
	// neither On nor in the other. Both empty is refused.
	Index, Values []string
	// Agg reduces the values that fall in one cell, e.g. Expr.Sum or Expr.First.
	// Without it, a cell of two values or more is refused.
	Agg func(Expr) Expr
	// Separator joins a value column's name and an On value, when Values is more
	// than one column: "v_x". "_" when empty.
	Separator string
	// SortColumns orders the new columns by their On values, rather than by where
	// each first appears.
	SortColumns bool
}

// Pivot spreads a frame wide, as Polars' DataFrame.pivot does: a row for each
// distinct Index, in the order each first appears, a null among them; and, for each
// Values column, a column for each distinct On value, in the order each first
// appears, holding that value column's value where the row's Index and On meet,
// null where they never do. A column is named after its On value, "null" for a
// null, or the value column's name, Separator and the On value when Values is
// several. A cell holding two values or more needs Agg.
//
//	wide, err := df.Pivot(ctx, ursus.PivotOptions{On: "quarter", Index: []string{"region"},
//		Values: []string{"sales"}, Agg: ursus.Expr.Sum})
func (df *DataFrame) Pivot(ctx context.Context, o PivotOptions) (*DataFrame, error) {
	index, values, err := pivotColumns(df.Columns(), o)
	if err != nil {
		return nil, err
	}
	// One row for each Index and On pair, each value column reduced over it, in the
	// order the pairs first appear, which is the order each On value first appears
	// too: the pair holding a value's first row comes first among the pairs holding
	// it.
	keys := make([]Expr, 0, len(index)+1)
	for _, k := range index {
		keys = append(keys, Col(k))
	}
	keys = append(keys, Col(o.On))
	count := freshName(df.Columns(), "n")
	aggs := make([]Expr, 0, len(values)+1)
	for _, v := range values {
		if o.Agg != nil {
			aggs = append(aggs, o.Agg(Col(v)).Alias(v))
		} else {
			aggs = append(aggs, Col(v).First().Alias(v))
		}
	}
	aggs = append(aggs, Len().Alias(count))
	long, err := df.Lazy().GroupBy(keys...).MaintainOrder().Agg(aggs...).Collect(ctx)
	if err != nil {
		return nil, err
	}
	b := long.batch
	n := b.Rows()
	if o.Agg == nil {
		counts := data.MustValues[uint64](b.Column(b.NumCols() - 1))
		for r, c := range counts {
			if c > 1 {
				return nil, uerr.New(uerr.KindValue, "pivot",
					"%d rows fall where row %d's index and %s meet, and Agg is not set", c, r, o.On).
					Hint("give an Agg to reduce them, e.g. ursus.Expr.Sum or ursus.Expr.First")
			}
		}
	}

	// Each pair row's Index group, numbered in the order the groups first appear.
	group, first, err := groupsOf(b.Columns()[:len(index)], n)
	if err != nil {
		return nil, err
	}
	// Each pair row's On value, numbered likewise.
	onCol := b.Column(len(index))
	onID, onFirst, err := groupsOf([]*data.Column{onCol}, n)
	if err != nil {
		return nil, err
	}
	order := make([]int, len(onFirst))
	for i := range order {
		order[i] = i
	}
	if o.SortColumns {
		distinct, err := kernel.Take(onCol, onFirst)
		if err != nil {
			return nil, err
		}
		sel, err := kernel.ArgSortColumns([]*data.Column{distinct}, []kernel.SortSpec{{}}, len(onFirst))
		if err != nil {
			return nil, err
		}
		for i, s := range sel {
			order[i] = int(s)
		}
	}
	names, err := kernel.Cast(o.On, dtype.String, false, onCol)
	if err != nil {
		return nil, err
	}

	// pick[u][g] is the pair row where Index group g meets On value u.
	pick := make([][]int32, len(onFirst))
	for u := range pick {
		pick[u] = make([]int32, len(first))
		for g := range pick[u] {
			pick[u][g] = kernel.NullIndex
		}
	}
	for r := range n {
		pick[onID[r]][group[r]] = int32(r)
	}

	cols := make([]*data.Column, 0, len(index)+len(values)*len(onFirst))
	seen := make(map[string]bool)
	for i, k := range index {
		c, err := kernel.Take(b.Column(i), first)
		if err != nil {
			return nil, err
		}
		cols, seen[k] = append(cols, c.Rename(k)), true
	}
	sep := o.Separator
	if sep == "" {
		sep = "_"
	}
	for j, v := range values {
		src := b.Column(len(index) + 1 + j)
		for _, u := range order {
			name := "null"
			if row := int(onFirst[u]); names.IsValid(row) {
				name = names.Strings().Get(row)
			}
			if len(values) > 1 {
				name = v + sep + name
			}
			if seen[name] {
				return nil, uerr.New(uerr.KindSchema, "pivot",
					"two columns of the pivot would be named %q", name).
					Hint("rename the index column, or set Separator")
			}
			seen[name] = true
			c, err := kernel.Take(src, pick[u])
			if err != nil {
				return nil, err
			}
			cols = append(cols, c.Rename(name))
		}
	}
	return frameOfColumns(cols, len(first))
}

// pivotColumns resolves Index and Values against the frame's columns.
func pivotColumns(all []string, o PivotOptions) (index, values []string, err error) {
	has := make(map[string]bool, len(all))
	for _, c := range all {
		has[c] = true
	}
	if !has[o.On] {
		return nil, nil, uerr.New(uerr.KindSchema, "pivot", "no column %q to pivot on", o.On).
			Hint("On names the column whose values become the new columns")
	}
	if len(o.Index) == 0 && len(o.Values) == 0 {
		return nil, nil, uerr.New(uerr.KindSchema, "pivot", "pivot needs Index or Values").
			Hint("name the row keys, the columns to spread, or both")
	}
	used := map[string]bool{o.On: true}
	for _, list := range [][]string{o.Index, o.Values} {
		for _, c := range list {
			if !has[c] {
				return nil, nil, uerr.New(uerr.KindSchema, "pivot", "no column %q", c)
			}
			if used[c] {
				return nil, nil, uerr.New(uerr.KindSchema, "pivot",
					"%q is named twice among On, Index and Values", c)
			}
			used[c] = true
		}
	}
	rest := func() []string {
		var out []string
		for _, c := range all {
			if !used[c] {
				out = append(out, c)
			}
		}
		return out
	}
	index, values = o.Index, o.Values
	if len(index) == 0 {
		index = rest()
	} else if len(values) == 0 {
		values = rest()
	}
	if len(values) == 0 {
		return nil, nil, uerr.New(uerr.KindSchema, "pivot", "pivot has no values to spread").
			Hint("every column is On or Index")
	}
	return index, values, nil
}

// groupsOf numbers each of n rows by its values in cols, as group_by groups them, a
// null among them, in the order each group first appears; and gives each group's
// first row. No columns is one group.
func groupsOf(cols []*data.Column, n int) ([]int32, []int32, error) {
	ids := make([]int32, n)
	if len(cols) == 0 {
		if n == 0 {
			return ids, nil, nil
		}
		return ids, []int32{0}, nil
	}
	enc, err := kernel.NewGroupKeyEncoder("pivot", cols)
	if err != nil {
		return nil, nil, err
	}
	seen := make(map[string]int32)
	var first []int32
	for r := range n {
		k := string(enc.Encode(r))
		id, ok := seen[k]
		if !ok {
			id = int32(len(first))
			seen[k] = id
			first = append(first, int32(r))
		}
		ids[r] = id
	}
	return ids, first, nil
}

// freshName is a column name not among names, from base.
func freshName(names []string, base string) string {
	name := "__" + base
	for slices.Contains(names, name) {
		name += "_"
	}
	return name
}

// frameOfColumns makes a frame of cols, rows long, every column nullable.
func frameOfColumns(cols []*data.Column, rows int) (*DataFrame, error) {
	fields := make([]dtype.Field, len(cols))
	for i, c := range cols {
		fields[i] = dtype.Of(c.Name(), c.DType())
	}
	sch, err := dtype.NewSchema(fields...)
	if err != nil {
		return nil, err
	}
	return &DataFrame{batch: data.NewBatchRows(sch, cols, rows)}, nil
}

// TransposeOptions says how Transpose names what it makes.
type TransposeOptions struct {
	// IncludeHeader adds a first column of the frame's column names, named
	// HeaderName, "column" when empty.
	IncludeHeader bool
	HeaderName    string
	// ColumnNames names the new columns, one for each row; "column_0" and on when
	// empty.
	ColumnNames []string
}

// Transpose turns rows into columns, as Polars' DataFrame.transpose does: column j
// of the answer holds row j of the frame, a value for each of its columns, in their
// order. Every column is cast to the type all of them meet at, as a conditional's
// branches meet, so an Int64 and a Float64 make Float64 columns; columns that meet
// at none, a String and a number, are refused, where Polars casts them all to
// String.
func (df *DataFrame) Transpose(ctx context.Context, o TransposeOptions) (*DataFrame, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b := df.batch
	rows, width := b.Rows(), b.NumCols()
	if len(o.ColumnNames) > 0 && len(o.ColumnNames) != rows {
		return nil, uerr.New(uerr.KindSchema, "transpose",
			"transpose was given %d column names for %d rows", len(o.ColumnNames), rows).
			Hint("name one column for each row, or none")
	}
	common := dtype.Null
	for _, f := range b.Schema().All() {
		t, ok := dtype.Promote(common, f.Type)
		if !ok {
			return nil, uerr.New(uerr.KindType, "transpose",
				"transpose puts every column in one, and %s and %s (%s) have no common type",
				common, f.Type, f.Name).
				Hint("cast the columns so they agree, e.g. .Cast(ursus.String)")
		}
		common = t
	}
	parts := make([]*data.Column, 0, width)
	for _, c := range b.Columns() {
		if c.DType() != common {
			var err error
			if c, err = kernel.Cast(c.Name(), common, true, c); err != nil {
				return nil, err
			}
		}
		parts = append(parts, c)
	}
	cols := make([]*data.Column, 0, rows+1)
	if o.IncludeHeader {
		header := o.HeaderName
		if header == "" {
			header = "column"
		}
		cols = append(cols, data.NewString(header, df.Columns(), bitmap.AllSet(width)))
	}
	if width > 0 && rows > 0 {
		all, err := kernel.ConcatColumns("t", parts)
		if err != nil {
			return nil, err
		}
		sel := make([]int32, width)
		for j := range rows {
			if j%1024 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			for i := range sel {
				sel[i] = int32(i*rows + j)
			}
			c, err := kernel.Take(all, sel)
			if err != nil {
				return nil, err
			}
			name := "column_" + strconv.Itoa(j)
			if len(o.ColumnNames) > 0 {
				name = o.ColumnNames[j]
			}
			cols = append(cols, c.Rename(name))
		}
	}
	return frameOfColumns(cols, width)
}
