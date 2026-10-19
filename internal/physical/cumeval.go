package physical

import (
	"context"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
)

// cumulative_eval (step 286): Polars' cumulative_eval, each row's answer its body
// over the values of its partition up to and including it, in the window's order. The
// body is lowered as List().Eval lowers an aggregate (expr.LowerCumulativeEval), one
// value a list, so it is evaluated over a list column of the prefixes. A row whose
// prefix holds fewer than MinSamples values is null.
//
// A kernel cannot evaluate an expression, so the window evaluates it here. Prefixes
// overlap and a list column's offsets cannot share them, so they are built a run of
// rows at a time, about cumulativeChunk values at once: the time is a prefix a row,
// as Polars', and the memory a run's.

const cumulativeChunk = 1 << 20

func cumulativeEval(ctx context.Context, p expr.WinParams, name string, out dtype.DataType,
	child *data.Column, seg kernel.Segments) (*data.Column, error) {

	schema, err := dtype.NewSchema(dtype.Field{Name: expr.CumulativeName, Type: dtype.List(child.DType()), Nullable: true})
	if err != nil {
		return nil, err
	}
	valid := child.Validity()
	var pieces []*data.Column
	sel := make([]int32, child.Len())
	at := int32(0)
	for q := 0; q+1 < len(seg.Bounds); q++ {
		rows := seg.Perm[seg.Bounds[q]:seg.Bounds[q+1]]
		seen := make([]int, len(rows)) // values in each row's prefix
		count := 0
		for i, r := range rows {
			if valid.Get(int(r)) {
				count++
			}
			seen[i] = count
		}
		for a := 0; a < len(rows); {
			// The run [a, b): its prefixes' values within the chunk, one row at least.
			b, total := a, 0
			for b < len(rows) && (b == a || total+b+1 <= cumulativeChunk) {
				total += b + 1
				b++
			}
			idx := make([]int32, 0, total)
			offs := make([]int32, 1, b-a+1)
			for i := a; i < b; i++ {
				idx = append(idx, rows[:i+1]...)
				offs = append(offs, int32(len(idx)))
			}
			elems, err := kernel.Take(child, idx)
			if err != nil {
				return nil, err
			}
			lists := data.NewList(expr.CumulativeName, offs, elems, bitmap.AllSet(b-a))
			batch, err := data.NewBatch(schema, []*data.Column{lists})
			if err != nil {
				return nil, err
			}
			res, err := Eval(ctx, p.Body, batch)
			if err != nil {
				return nil, err
			}
			if res.DType() != out {
				if res, err = kernel.Cast(name, out, true, res); err != nil {
					return nil, err
				}
			}
			pieces = append(pieces, res)
			for i := a; i < b; i++ {
				sel[rows[i]] = at + int32(i-a)
				if seen[i] < int(p.MinSamples) {
					sel[rows[i]] = kernel.NullIndex
				}
			}
			at += int32(b - a)
			a = b
		}
	}
	if len(pieces) == 0 {
		return kernel.NullColumn(name, out, child.Len())
	}
	all, err := kernel.ConcatColumns(name, pieces)
	if err != nil {
		return nil, err
	}
	res, err := kernel.Take(all, sel)
	if err != nil {
		return nil, err
	}
	return res.Rename(name), nil
}
