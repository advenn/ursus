package ursus

import (
	"context"
	"fmt"
	"math"
	"strconv"

	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/uerr"
)

// Unstacking (step 320, tier 2 of v0.6.7-scope.md): Polars' unstack.

// UnstackHow is the direction Unstack lays a column's values out in.
type UnstackHow uint8

const (
	// UnstackVertical fills each new column with Step values in turn, Polars'
	// default.
	UnstackVertical UnstackHow = iota
	// UnstackHorizontal fills each new row with Step values in turn, across Step new
	// columns.
	UnstackHorizontal
)

// UnstackOptions configures Unstack.
type UnstackOptions struct {
	// Step is how many values a new column holds, vertically, or a new row,
	// horizontally.
	Step int
	How  UnstackHow

	// Columns are the columns to unstack, every column when empty; the others are
	// left out, as in Polars.
	Columns []string

	// Fill fills the slots after the last value: one value for every column, or
	// one for each of Columns, cast to its type as ExtendConstant casts. They are
	// null when it is empty.
	Fill []any
}

// Unstack lays each of Columns out as a grid without keys, Polars' unstack, cheaper
// than a pivot. Vertically, a column x's values go a Step at a time down new columns
// x_0, x_1 and on, Step rows high; horizontally, a Step at a time across Step new
// columns, so x_0 holds the first of each Step. The slots after the last value are
// null, or Fill. Each suffix is zero-padded to the digits of the count of new
// columns, as Polars pads it: x_00 to x_12 for 13.
//
// A vertical column is a slice of the old, shared, but for a last one with slots to
// fill, and a horizontal one a gather.
func (df *DataFrame) Unstack(ctx context.Context, o UnstackOptions) (*DataFrame, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if o.Step <= 0 || o.Step > math.MaxInt32 {
		return nil, uerr.New(uerr.KindValue, "unstack",
			"unstack takes a step of at least 1 and below 2^31, got %d", o.Step)
	}
	names := o.Columns
	if len(names) == 0 {
		names = df.Columns()
	}
	if len(o.Fill) > 1 && len(o.Fill) != len(names) {
		return nil, uerr.New(uerr.KindValue, "unstack",
			"Fill gives %d values for %d columns; give one for all, or one each", len(o.Fill), len(names))
	}
	src := make([]*data.Column, len(names))
	for k, name := range names {
		c, ok := df.batch.ByName(name)
		if !ok {
			return nil, uerr.UnknownColumn("unstack", name, df.Columns())
		}
		src[k] = c
	}
	n := df.Height()
	rows, cols := o.Step, (n+o.Step-1)/o.Step
	if o.How == UnstackHorizontal {
		rows, cols = cols, o.Step
	}
	fill := rows*cols - n
	if need, lim := int64(fill)*int64(len(src))*8, execopt.DefaultLimit(); lim > 0 && need > lim {
		return nil, uerr.New(uerr.KindResource, "unstack",
			"a step of %d pads %d rows with %d slots a column, past the memory limit of %d bytes", o.Step, n, fill, lim)
	}
	if fill > 0 && len(o.Fill) > 0 {
		// The columns extended by their fill, as Polars extends them first.
		ext := make([]Expr, len(names))
		for k, name := range names {
			ext[k] = Col(name).ExtendConstant(o.Fill[min(k, len(o.Fill)-1)], fill)
		}
		filled, err := df.Lazy().Select(ext...).Collect(ctx)
		if err != nil {
			return nil, err
		}
		src = filled.batch.Columns()
	}
	digits := len(strconv.Itoa(cols))
	out := make([]*data.Column, 0, len(src)*cols)
	sel := make([]int32, rows)
	for _, c := range src {
		have := c.Len()
		for j := range cols {
			name := fmt.Sprintf("%s_%0*d", c.Name(), digits, j)
			if o.How == UnstackVertical && (j+1)*rows <= have {
				out = append(out, c.Slice(j*rows, rows).Rename(name))
				continue
			}
			for i := range sel {
				at := j*rows + i // vertical: new column j holds values j*rows on
				if o.How == UnstackHorizontal {
					at = i*cols + j // horizontal: new row i holds values i*cols on
				}
				sel[i] = int32(at)
				if at >= have {
					sel[i] = kernel.NullIndex
				}
			}
			part, err := kernel.Take(c, sel)
			if err != nil {
				return nil, err
			}
			out = append(out, part.Rename(name))
		}
	}
	return framed(out, rows)
}
