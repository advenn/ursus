package ursus

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/uerr"
)

// Pivot (steps 229 and 303) and Transpose (step 229). Pivot is lazy since step 303,
// its new columns given, as Polars' LazyFrame.pivot takes on_columns, and a
// DataFrame's finds them. Transpose makes columns of the frame's rows, so its schema
// is known only once the data is: it is eager only.

// PivotOptions says how Pivot spreads a frame.
type PivotOptions struct {
	// On is the columns whose values name the new columns: one, or several, whose
	// values together name one, {"x","p"}, as Polars names it.
	On []string
	// OnColumns are the new columns' On values: a column for each of On, named as
	// it, and a new column for each row, in their order. A row the frame never holds
	// is a column of cells over no values; a value the frame holds and OnColumns
	// does not is left out, its index still a row. LazyFrame.Pivot needs them, its
	// columns known before it runs, as Polars' LazyFrame.pivot needs on_columns;
	// DataFrame.Pivot finds them when nil: every combination the frame holds, in the
	// order each first appears, or sorted with SortColumns.
	OnColumns []*Column
	// Index is the columns whose values make the rows, and Values those spread into
	// the new columns. Either may be left empty: it is then every column that is
	// neither On nor in the other. Both empty is refused.
	Index, Values []string
	// Agg reduces the values that fall in one cell, e.g. Expr.Sum or Expr.First, and
	// reads nothing else, as Polars' reads only pl.element(). A cell none fall in is
	// Agg of none: a count's 0, a sum's null, as every empty sum is here, where
	// Polars' is 0. Without Agg, a cell of two values or more is refused, and one of
	// none is null, as Polars' item has it.
	Agg func(Expr) Expr
	// Separator joins a value column's name and the On values' name, when Values is
	// more than one column: "v_x". "_" when empty.
	Separator string
	// SortColumns orders the columns DataFrame.Pivot finds by their On values,
	// rather than by where each first appears. Given OnColumns keep their order.
	SortColumns bool
}

// Pivot spreads a frame wide, as Polars' LazyFrame.pivot does: a row for each
// distinct Index, in the order each first appears, a null among them; and, for each
// Values column, a column for each row of OnColumns, holding Agg of that column's
// values where the row's Index and the On values meet. A new column is named after
// its On value, "null" for a null; after several, {"x","p"}, text quoted; and, when
// Values is several, after the value column, Separator and that.
//
//	wide := lf.Pivot(ursus.PivotOptions{On: []string{"quarter"}, Index: []string{"region"},
//		Values: []string{"sales"}, Agg: ursus.Expr.Sum,
//		OnColumns: []*ursus.Column{ursus.Values("quarter", []string{"Q1", "Q2"})}})
//
// The new columns are given, as Polars' on_columns are: a plan's schema is known
// before it runs. DataFrame.Pivot finds them. A group-by of Index and On reduces the
// rows, streaming the input and spilling past a memory limit; the pivot then holds
// a row for each index and On values that meet.
func (lf *LazyFrame) Pivot(o PivotOptions) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	s, err := lf.schemaNow()
	if err != nil {
		return &LazyFrame{err: err}
	}
	index, values, err := pivotColumns(s.Names(), o)
	if err != nil {
		return &LazyFrame{err: err}
	}
	if len(o.OnColumns) == 0 {
		return &LazyFrame{err: uerr.New(uerr.KindValue, "pivot",
			"a lazy pivot needs OnColumns: its columns are known before it runs").
			Hint("give the On values of the new columns, as Polars' on_columns; DataFrame.Pivot finds them")}
	}
	combos, err := pivotCombos(s, o.On, o.OnColumns)
	if err != nil {
		return &LazyFrame{err: err}
	}
	names, err := pivotNames(combos, index, values, o.Separator)
	if err != nil {
		return &LazyFrame{err: err}
	}

	// A row for each index and On values that meet, in the order each pair first
	// appears, each value's cell reduced over its rows. Then, with Agg, each value's
	// cell over no values, a filter keeping nothing; without, each pair's count.
	keys := make([]Expr, 0, len(index)+len(o.On))
	for _, k := range index {
		keys = append(keys, Col(k))
	}
	for _, k := range o.On {
		keys = append(keys, Col(k))
	}
	all := s.Names()
	aggs := make([]Expr, 0, 2*len(values))
	for _, v := range values {
		if o.Agg == nil {
			aggs = append(aggs, Col(v).First().Alias(v))
			continue
		}
		cell := o.Agg(Col(v))
		if err := pivotReadsOnly(cell, v); err != nil {
			return &LazyFrame{err: err}
		}
		aggs = append(aggs, cell.Alias(v))
	}
	if o.Agg != nil {
		for _, v := range values {
			aggs = append(aggs, o.Agg(Col(v).Filter(Lit(false))).Alias(freshName(all, "none_"+v)))
		}
	} else {
		aggs = append(aggs, Len().Alias(freshName(all, "n")))
	}
	long := lf.GroupBy(keys...).MaintainOrder().Agg(aggs...)
	if long.err != nil {
		return long
	}
	on := Frame(combos...)
	if on.err != nil {
		return on
	}
	return long.derive(&plan.Pivot{
		Input: long.node, OnColumns: on.node,
		Index: index, On: o.On, Values: values, Names: names, Empty: o.Agg != nil,
	})
}

// Pivot spreads a frame wide, as Polars' DataFrame.pivot does: LazyFrame's, with
// OnColumns, when nil, every combination of On's values the frame holds, in the
// order each first appears, or sorted with SortColumns, a null first.
//
//	wide, err := df.Pivot(ctx, ursus.PivotOptions{On: []string{"quarter"}, Index: []string{"region"},
//		Values: []string{"sales"}, Agg: ursus.Expr.Sum})
func (df *DataFrame) Pivot(ctx context.Context, o PivotOptions) (*DataFrame, error) {
	if len(o.OnColumns) == 0 {
		if _, _, err := pivotColumns(df.Columns(), o); err != nil {
			return nil, err
		}
		keys := make([]Expr, len(o.On))
		sorted := make([]SortKey, len(o.On))
		for i, name := range o.On {
			keys[i], sorted[i] = Col(name), Asc(Col(name))
		}
		q := df.Lazy().GroupBy(keys...).MaintainOrder().Agg()
		if o.SortColumns {
			q = q.Sort(sorted...)
		}
		found, err := q.Collect(ctx)
		if err != nil {
			return nil, err
		}
		o.OnColumns = found.batch.Columns()
	}
	return df.Lazy().Pivot(o).Collect(ctx)
}

// pivotColumns resolves On, Index and Values against the frame's columns.
func pivotColumns(all []string, o PivotOptions) (index, values []string, err error) {
	has := make(map[string]bool, len(all))
	for _, c := range all {
		has[c] = true
	}
	if len(o.On) == 0 {
		return nil, nil, uerr.New(uerr.KindSchema, "pivot", "pivot needs On").
			Hint("On names the columns whose values become the new columns")
	}
	if len(o.Index) == 0 && len(o.Values) == 0 {
		return nil, nil, uerr.New(uerr.KindSchema, "pivot", "pivot needs Index or Values").
			Hint("name the row keys, the columns to spread, or both")
	}
	used := make(map[string]bool)
	for i, list := range [][]string{o.On, o.Index, o.Values} {
		for _, c := range list {
			if !has[c] {
				if i == 0 {
					return nil, nil, uerr.New(uerr.KindSchema, "pivot", "no column %q to pivot on", c).
						Hint("On names the columns whose values become the new columns")
				}
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

// pivotCombos are OnColumns in On's order, each cast to its On column's type. A
// text column beside one of another kind is refused, as Polars refuses comparing
// them, rather than parsed.
func pivotCombos(s *dtype.Schema, on []string, given []*Column) ([]*Column, error) {
	byName := make(map[string]*Column, len(given))
	for _, c := range given {
		if c == nil {
			return nil, uerr.New(uerr.KindValue, "pivot", "OnColumns holds a nil column")
		}
		if !slices.Contains(on, c.Name()) {
			return nil, uerr.New(uerr.KindSchema, "pivot",
				"OnColumns has a column %q, which is not among On", c.Name()).
				Hint("name each of OnColumns after the On column whose values it holds")
		}
		if byName[c.Name()] != nil {
			return nil, uerr.New(uerr.KindSchema, "pivot", "OnColumns has %q twice", c.Name())
		}
		byName[c.Name()] = c
	}
	out := make([]*Column, len(on))
	for i, name := range on {
		c := byName[name]
		if c == nil {
			return nil, uerr.New(uerr.KindSchema, "pivot", "OnColumns has no column %q", name).
				Hint("give a column of values for each of On, named after it")
		}
		if i > 0 && c.Len() != out[0].Len() {
			return nil, uerr.New(uerr.KindValue, "pivot",
				"OnColumns' %q is %d long and %q %d: a row for each new column", out[0].Name(), out[0].Len(), name, c.Len())
		}
		t := s.Field(s.IndexOf(name)).Type
		if c.DType() != t {
			if !c.DType().IsNull() && c.DType().IsString() != t.IsString() {
				return nil, uerr.New(uerr.KindType, "pivot",
					"OnColumns' %q is %s, and the frame's is %s, which cannot be compared", name, c.DType(), t).
					Hint("give the values as the On column holds them, or cast it")
			}
			cast, err := kernel.Cast(name, t, true, c)
			if err != nil {
				return nil, uerr.Wrap(err, uerr.KindType, "pivot", "OnColumns' %q as the frame's %s", name, t)
			}
			c = cast
		}
		out[i] = c
	}
	return out, nil
}

// pivotNames are the new columns' names, value by value, a row of combos at a time.
// One On column names a column by its value, "null" for a null; several by all of
// them as Polars writes them, {"x",1}, text quoted. Two names alike are refused.
func pivotNames(combos []*Column, index, values []string, sep string) ([]string, error) {
	k := combos[0].Len()
	texts := make([]*data.Column, len(combos))
	for i, c := range combos {
		t, err := kernel.Cast(c.Name(), dtype.String, false, c)
		if err != nil {
			return nil, uerr.Wrap(err, uerr.KindType, "pivot", "naming the columns by %q, %s", c.Name(), c.DType())
		}
		texts[i] = t
	}
	text := func(i, u int) string {
		if !texts[i].IsValid(u) {
			return "null"
		}
		s := texts[i].Strings().Get(u)
		if len(combos) > 1 && combos[i].DType().IsString() {
			return `"` + s + `"`
		}
		return s
	}
	one := make([]string, k)
	for u := range k {
		if len(combos) == 1 {
			one[u] = text(0, u)
			continue
		}
		parts := make([]string, len(combos))
		for i := range combos {
			parts[i] = text(i, u)
		}
		one[u] = "{" + strings.Join(parts, ",") + "}"
	}
	if sep == "" {
		sep = "_"
	}
	seen := make(map[string]bool, len(index)+len(values)*k)
	for _, name := range index {
		seen[name] = true
	}
	names := make([]string, 0, len(values)*k)
	for _, v := range values {
		for u := range k {
			name := one[u]
			if len(values) > 1 {
				name = v + sep + name
			}
			if seen[name] {
				return nil, uerr.New(uerr.KindSchema, "pivot",
					"two columns of the pivot would be named %q", name).
					Hint("rename the index column, set Separator, or give each On value once")
			}
			seen[name] = true
			names = append(names, name)
		}
	}
	return names, nil
}

// pivotReadsOnly refuses an Agg that reads a column other than the value v it is
// given: a cell is the values that fall in it, as Polars' aggregate reads only
// pl.element(), and a cell over none could not be one value for every row.
func pivotReadsOnly(cell Expr, v string) error {
	for _, name := range expr.RootNames(cell.node()) {
		if name != v {
			return uerr.New(uerr.KindValue, "pivot",
				"Agg reads %q, where it reads only the values it is given, here %q", name, v).
				Hint("reduce e alone, as func(e ursus.Expr) ursus.Expr { return e.Sum() }; " +
					"a cell is the values of one column that fall in it")
		}
	}
	return nil
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
