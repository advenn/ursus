package ast

import (
	"fmt"

	"github.com/advenn/ursus/qcheck/model"
)

// Bound names the columns that bound a noisy column's rounding: the sum of its
// values' magnitudes, and how many there were.
type Bound struct {
	Op  AggOp  // Sum or Mean
	Abs string // Σ|x| of the group, a Float64
	N   string // the count of x, a Uint64
}

// Companion suffixes. No generated name holds "__".
const (
	AbsSuffix = "__abs"
	NSuffix   = "__n"
)

// Bounded is q with a noisy column's bounds beside it: every float Sum or Mean of
// a group-by that runs on every thread gains x.Abs().Sum() and x.Count() in its
// Agg, and every Select that passes it through passes them too. Its result is
// compared under the error model of the critic's correction to 437, |Δ| ≤
// 2·n·eps·Σ|x| for a Sum, for each noisy column, and exactly for every other.
// Step 513 compares a float aggregate against DuckDB by the same bounds.
func Bounded(q *Query) (*Query, map[string]Bound) {
	bounds := map[string]Bound{}
	root := bound(q.Root, bounds)
	return &Query{Root: root, Count: q.Count}, bounds
}

func bound(f Frame, bounds map[string]Bound) Frame {
	switch f := f.(type) {
	case *Scan:
		return f
	case *Filter:
		return &Filter{In: bound(f.In, bounds), Pred: f.Pred}
	case *WithColumns:
		return &WithColumns{In: bound(f.In, bounds), Cols: f.Cols}
	case *Sort:
		return &Sort{In: bound(f.In, bounds), Keys: f.Keys}
	case *Head:
		return &Head{In: bound(f.In, bounds), N: f.N}
	case *Slice:
		return &Slice{In: bound(f.In, bounds), Offset: f.Offset, Len: f.Len}
	case *Join:
		return &Join{L: bound(f.L, bounds), R: bound(f.R, bounds), How: f.How, LeftOn: f.LeftOn,
			RightOn: f.RightOn, MaintainOrder: f.MaintainOrder}
	case *Select:
		in := bound(f.In, bounds) // first: it names the bounds below it
		cols := append([]Named(nil), f.Cols...)
		for _, n := range f.Cols {
			c, ok := n.E.(*Col)
			if !ok || c.Name != n.Name {
				continue
			}
			if b, ok := bounds[c.Name]; ok {
				cols = append(cols,
					Named{Name: b.Abs, E: &Col{Name: b.Abs, T: model.Float64}},
					Named{Name: b.N, E: &Col{Name: b.N, T: model.Uint64}})
			}
		}
		return &Select{In: in, Cols: cols}
	case *GroupBy:
		out := &GroupBy{In: bound(f.In, bounds), Keys: f.Keys, MaintainOrder: f.MaintainOrder}
		out.Aggs = append([]Named(nil), f.Aggs...)
		for _, n := range f.Aggs {
			a := n.E.(*Agg)
			if f.MaintainOrder || (a.Op != Sum && a.Op != Mean) || a.X == nil || a.X.Type() != model.Float64 {
				continue
			}
			abs, err := NewUn(Abs, a.X)
			must(err)
			sumAbs, err := NewAgg(Sum, abs)
			must(err)
			count, err := NewAgg(Count, a.X)
			must(err)
			b := Bound{Op: a.Op, Abs: n.Name + AbsSuffix, N: n.Name + NSuffix}
			bounds[n.Name] = b
			out.Aggs = append(out.Aggs, Named{Name: b.Abs, E: sumAbs}, Named{Name: b.N, E: count})
		}
		return out
	}
	panic(fmt.Sprintf("ast: Bounded: unknown frame %T", f))
}

func must(err error) {
	if err != nil {
		panic("ast: " + err.Error())
	}
}
