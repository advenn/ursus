package ursus

import (
	"strconv"

	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/uerr"
)

// Length-changing expressions in Select (step 258, E4 of v0.6.5-scope.md).
//
// Polars' Select(Col("a").Sort(), Col("b").Head(3)) evaluates each expression over
// the whole column, so a column may be of another length than the frame's: the
// columns of one length stand side by side, one value is broadcast to their length,
// and columns of different lengths are refused.
//
// Each expression that changes a length is computed by a plan of its own over the
// input: its values projected, then the frame-level operation that does to rows what
// it does to the column, Sort, Head, Tail, Slice, Reverse, Unique, a Filter, so each
// spills or streams as that operation does. The plans are zipped by an HStack, which
// refuses columns of different lengths, and one value, an aggregate, is broadcast
// beside them by a cross join with its one row. The expression around them is then a
// projection of the zipped columns.
//
// The input is read once a plan. A shared input that joins, aggregates or sorts is
// computed once and replayed (MarkShared); a scan is read again.

// changesLength reports whether n has a reshape or a filter outside an aggregate: a
// column of another length than its input's.
func changesLength(n expr.Node) bool {
	return expr.LooseReshape(n) != nil || expr.LooseFilter(n) != nil
}

// hasMatch reports whether n selects columns by a pattern or a list, which only the
// input's schema expands.
func hasMatch(n expr.Node) bool {
	if _, ok := n.(*expr.Match); ok {
		return true
	}
	for _, c := range n.Children() {
		if hasMatch(c) {
			return true
		}
	}
	return false
}

// zipPart is one expression's value: a column of a plan, or a constant's own node.
type zipPart struct {
	lf   *LazyFrame // nil for a constant
	col  string     // the column of lf that holds the value
	one  bool       // lf is one row, broadcast against the others
	node expr.Node  // a constant
	text string     // the expression, for an error
}

// zipper lifts a Select's expressions into plans over in.
type zipper struct {
	in    *LazyFrame
	count int
}

func (z *zipper) tmp() string {
	z.count++
	return "__z" + strconv.Itoa(z.count)
}

// selectReshaped is Select of ns, at least one of which changes its length.
func (lf *LazyFrame) selectReshaped(ns []expr.Node) *LazyFrame {
	z := &zipper{in: lf}
	parts := make([]zipPart, len(ns))
	for i, n := range ns {
		if changesLength(n) && hasMatch(n) {
			return &LazyFrame{err: uerr.New(uerr.KindUnsupported, "select",
				"%s reshapes a selection of several columns, which is not there yet", n).
				Hint("name each column: Col(\"a\").Sort(), Col(\"b\").Sort()")}
		}
		core := n
		for {
			a, ok := core.(*expr.Alias)
			if !ok {
				break
			}
			core = a.Child
		}
		p, err := z.lift(core)
		if err != nil {
			return &LazyFrame{err: err}
		}
		p.text = n.String()
		parts[i] = p
	}
	frame, refs := z.zip(parts)
	if frame.err != nil {
		return frame
	}
	out := make([]Expr, len(ns))
	for i, n := range ns {
		out[i] = wrap(refs[i]).Alias(expr.OutputName(n))
	}
	return frame.Select(out...)
}

// lift is n's value as a column of a plan over the input, or n itself if it is a
// constant.
func (z *zipper) lift(n expr.Node) (zipPart, error) {
	switch {
	case expr.IsConstant(n):
		return zipPart{node: n}, nil
	case !changesLength(n):
		// A value a row, or one value: the input's own projection, or its keyless
		// aggregate.
		col := z.tmp()
		return zipPart{lf: z.in.Select(wrap(n).Alias(col)), col: col, one: expr.IsScalar(n)}, nil
	}
	switch t := n.(type) {
	case *expr.Reshape:
		return z.reshape(t)
	case *expr.Filter:
		return z.filter(t)
	}
	// Anything else over columns of other lengths: each operand lifted, the operands
	// zipped, and n a projection of them.
	kids := n.Children()
	parts := make([]zipPart, len(kids))
	for i, c := range kids {
		p, err := z.lift(c)
		if err != nil {
			return zipPart{}, err
		}
		p.text = c.String()
		parts[i] = p
	}
	frame, refs := z.zip(parts)
	col := z.tmp()
	one := true
	for _, p := range parts {
		one = one && (p.lf == nil || p.one)
	}
	return zipPart{lf: frame.Select(wrap(expr.Rebuild(n, refs)).Alias(col)), col: col, one: one}, nil
}

// zip places the parts' plans side by side: those of a column zipped by an HStack,
// which refuses two lengths, and those of one value beside them by a cross join with
// their one row. refs is each part's value in the zipped frame.
func (z *zipper) zip(parts []zipPart) (*LazyFrame, []expr.Node) {
	refs := make([]expr.Node, len(parts))
	var cols, ones []*LazyFrame
	var labels []string
	for i, p := range parts {
		if p.lf == nil {
			refs[i] = p.node
			continue
		}
		refs[i] = &expr.Col{Name: p.col}
		if p.one {
			ones = append(ones, p.lf)
		} else {
			cols = append(cols, p.lf)
			labels = append(labels, p.text)
		}
	}
	if len(cols) == 0 {
		// Every part is one value: one row, side by side.
		cols, ones = ones, nil
		labels = nil
	}
	frame := cols[0]
	if len(cols) > 1 {
		nodes := make([]plan.Node, len(cols))
		for i, f := range cols {
			if f.err != nil {
				return f, nil
			}
			nodes[i] = f.node
		}
		frame = frame.derive(&plan.HStack{Inputs: nodes, Labels: labels})
	}
	for _, o := range ones {
		frame = frame.Join(o, JoinHow(JoinCross))
	}
	return frame, refs
}

// reshape lifts a length-changing operation: its values' plan, then the frame-level
// operation of the same effect.
func (z *zipper) reshape(r *expr.Reshape) (zipPart, error) {
	switch r.Op {
	case expr.ReshapeGet:
		// One value: the list form an aggregation takes, Get past the end null.
		return z.lift(expr.LowerReshapes(r, true))
	case expr.ReshapeSortBy:
		return z.sortBy(r)
	case expr.ReshapeGather:
		return zipPart{}, uerr.New(uerr.KindUnsupported, "gather",
			"%s in a Select is not there yet", r).
			Hint("in GroupBy(...).Agg(...) it gathers each group's values")
	}
	p, err := z.lift(r.Child)
	if err != nil {
		return zipPart{}, err
	}
	if p.lf == nil || p.one {
		return zipPart{}, uerr.New(uerr.KindType, r.Op.String(),
			"%s reshapes one value, which has no rows to reshape", r)
	}
	c, arg := Col(p.col), r.Args
	var out *LazyFrame
	switch r.Op {
	case expr.ReshapeSort:
		key := Asc(c)
		if arg[0] != 0 {
			key = Desc(c)
		}
		out = p.lf.Sort(key)
	case expr.ReshapeHead:
		out = p.lf.Head(int(arg[0]))
	case expr.ReshapeTail:
		out = p.lf.Tail(int(arg[0]))
	case expr.ReshapeSlice:
		// A negative offset counts from the end, as Polars' slice does.
		if off := int(arg[0]); off < 0 {
			out = p.lf.Tail(-off)
			if arg[1] >= 0 {
				out = out.Head(int(arg[1]))
			}
		} else {
			out = p.lf.Slice(off, int(arg[1]))
		}
	case expr.ReshapeReverse:
		out = p.lf.Reverse()
	case expr.ReshapeUnique:
		out = p.lf.Unique()
	case expr.ReshapeDropNulls:
		out = p.lf.Filter(c.IsNotNull())
	case expr.ReshapeDropNans:
		out = p.lf.Filter(c.IsNotNan().Or(c.IsNull()))
	case expr.ReshapeGatherEvery:
		every, off := arg[0], arg[1]
		idx := z.tmp()
		out = p.lf.WithRowIndex(idx, 0).
			Filter(Col(idx).Ge(off), Col(idx).Sub(off).Mod(every).Eq(0)).Select(c)
	default:
		return zipPart{}, uerr.Internalf("ursus: reshape %s has no frame form", r.Op)
	}
	return zipPart{lf: out, col: p.col}, nil
}

// sortBy lifts a sort by keys of the values' own rows: both projected together, then
// a stable sort by the keys.
func (z *zipper) sortBy(r *expr.Reshape) (zipPart, error) {
	if changesLength(r.Child) || changesLength(r.By) || expr.HasAgg(r.By) {
		return zipPart{}, uerr.New(uerr.KindUnsupported, "sort_by",
			"%s sorts a column by keys of another length", r).
			Hint("sort_by first, then reshape: Col(\"a\").SortBy(Col(\"b\")).Head(2)")
	}
	col, key := z.tmp(), z.tmp()
	k := Asc(Col(key))
	if r.Args[0] != 0 {
		k = Desc(Col(key))
	}
	out := z.in.Select(wrap(r.Child).Alias(col), wrap(r.By).Alias(key)).Sort(k).Select(Col(col))
	return zipPart{lf: out, col: col}, nil
}

// filter lifts Expr.Filter: the values and the predicate projected together, then a
// filter of the rows.
func (z *zipper) filter(f *expr.Filter) (zipPart, error) {
	if changesLength(f.Child) || changesLength(f.Pred) {
		return zipPart{}, uerr.New(uerr.KindUnsupported, "filter",
			"%s filters by a predicate of another length", f).
			Hint("filter first, then reshape: Col(\"a\").Filter(Col(\"b\").Gt(0)).Sort()")
	}
	col, pred := z.tmp(), z.tmp()
	out := z.in.Select(wrap(f.Child).Alias(col), wrap(f.Pred).Alias(pred)).
		Filter(Col(pred)).Select(Col(col))
	return zipPart{lf: out, col: col}, nil
}
