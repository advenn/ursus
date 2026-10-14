package expr

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// Length-changing expressions in an aggregation (v0.6.5 step 252, E3 of
// v0.6.5-scope.md).
//
// Polars' col("a").sort() is the column sorted, its length its own; in group_by().agg
// it is each group's values sorted, a list a group, and col("a").sort().first() the
// first of those. Here a Reshape is that operation, and LowerReshapes rewrites it in
// an aggregation into what ursus already computes: the group's values imploded into
// a list, then the list kernel of the same name — list.sort, list.head and the rest
// — and an aggregate over a reshape into that list's aggregate. So
// a.sort().first() is list.first(list.sort(a.implode())).
//
// The implode holds a group's values, as Polars' does, and refuses past a memory
// limit rather than spill, as implode does. A reshape outside an aggregation, a
// column of another length in Select, is refused (LooseReshape) until E4.

// ReshapeOp is a length-changing operation on a column.
type ReshapeOp uint8

const (
	ReshapeSort      ReshapeOp = iota // Args: descending, 0 or 1
	ReshapeHead                       // Args: n
	ReshapeTail                       // Args: n
	ReshapeSlice                      // Args: offset, length
	ReshapeReverse                    // no args
	ReshapeUnique                     // no args: first-seen order, null a value
	ReshapeDropNulls                  // no args
	ReshapeGet                        // Args: index; one value, not a column
)

var reshapeNames = map[ReshapeOp]string{
	ReshapeSort: "sort", ReshapeHead: "head", ReshapeTail: "tail", ReshapeSlice: "slice",
	ReshapeReverse: "reverse", ReshapeUnique: "unique", ReshapeDropNulls: "drop_nulls",
	ReshapeGet: "get",
}

func (o ReshapeOp) String() string { return reshapeNames[o] }

// Reshape is a length-changing operation on Child.
type Reshape struct {
	Op    ReshapeOp
	Child Node
	Args  []int64
}

func (r *Reshape) node()            {}
func (r *Reshape) Children() []Node { return []Node{r.Child} }

func (r *Reshape) String() string {
	args := ""
	switch r.Op {
	case ReshapeSort:
		if len(r.Args) > 0 && r.Args[0] != 0 {
			args = "descending"
		}
	default:
		for i, a := range r.Args {
			if i > 0 {
				args += ", "
			}
			args += itoa(int(a))
		}
	}
	return r.Child.String() + "." + r.Op.String() + "(" + args + ")"
}

// Field is the child's: a reshape keeps its values' type.
func (r *Reshape) Field(in *dtype.Schema) (dtype.Field, error) { return r.Child.Field(in) }

// LooseReshape is the first Reshape in n that LowerReshapes left, or nil: one outside
// an aggregation.
func LooseReshape(n Node) *Reshape {
	if r, ok := n.(*Reshape); ok {
		return r
	}
	for _, c := range n.Children() {
		if l := LooseReshape(c); l != nil {
			return l
		}
	}
	return nil
}

func looseReshapeErr(r *Reshape) error {
	e := uerr.New(uerr.KindUnsupported, r.Op.String(),
		"%s is a column of its own length, which is only an aggregation's yet", r.String()).
		Hint("in GroupBy(...).Agg(...) it is a list a group, and an aggregate over it one value: %s.first()",
			r.String())
	if frame, ok := frameForms[r.Op]; ok {
		e.Hint("to reshape the frame's rows, use the frame-level form: %s", frame)
	}
	return e
}

// frameForms are the frame-level operations that do to rows what a reshape does to
// a column.
var frameForms = map[ReshapeOp]string{
	ReshapeSort: "lf.Sort", ReshapeHead: "lf.Head", ReshapeTail: "lf.Tail", ReshapeSlice: "lf.Slice",
	ReshapeReverse: "lf.Reverse", ReshapeUnique: "lf.Unique", ReshapeDropNulls: "lf.DropNulls",
}

// LowerReshapes rewrites n's reshapes into implodes and list kernels: an aggregate
// over a reshape anywhere, and in an aggregation (agg) every reshape, which is a list
// a group there. What it cannot lower is left as an error node naming why.
func LowerReshapes(n Node, agg bool) Node {
	switch t := n.(type) {
	case *Reshape:
		if !agg {
			// A reshape outside an aggregation is left for LooseReshape to refuse,
			// but an aggregate over one inside it is still lowered.
			return Rebuild(t, []Node{LowerReshapes(t.Child, false)})
		}
		return lowerReshape(t)
	case *Agg:
		if r, ok := t.Child.(*Reshape); ok {
			return lowerAggOver(t, lowerReshape(r))
		}
	}
	kids := n.Children()
	if len(kids) == 0 {
		return n
	}
	out := make([]Node, len(kids))
	for i, c := range kids {
		out[i] = LowerReshapes(c, agg)
	}
	return Rebuild(n, out)
}

// lowerReshape is a reshape as a list a group: its child imploded, or reshaped
// first, then the list kernel.
func lowerReshape(r *Reshape) Node {
	var list Node
	if inner, ok := r.Child.(*Reshape); ok {
		list = lowerReshape(inner)
	} else {
		if HasAgg(r.Child) {
			return &Err{E: uerr.New(uerr.KindType, r.Op.String(),
				"%s reshapes an aggregate, which is one value a group", r.String())}
		}
		if f := LooseFilter(r.Child); f != nil {
			return &Err{E: uerr.New(uerr.KindUnsupported, r.Op.String(),
				"%s reshapes a filter, which is not there yet", r.String()).
				Hint("an aggregate reads a filter directly: %s.sum()", f.String())}
		}
		list = &Agg{Op: AggImplode, Child: r.Child}
	}
	arg := func(i int) Node { return &Lit{Value: r.Args[i], DT: dtype.Int64} }
	switch r.Op {
	case ReshapeSort:
		return &Call{Fn: FnListSort, Args: []Node{list, &Lit{Value: r.Args[0] != 0, DT: dtype.Bool}}}
	case ReshapeHead:
		return &Call{Fn: FnListHead, Args: []Node{list, arg(0)}}
	case ReshapeTail:
		return &Call{Fn: FnListTail, Args: []Node{list, arg(0)}}
	case ReshapeSlice:
		return &Call{Fn: FnListSlice, Args: []Node{list, arg(0), arg(1)}}
	case ReshapeReverse:
		return &Call{Fn: FnListReverse, Args: []Node{list}}
	case ReshapeUnique:
		return &Call{Fn: FnListUnique, Args: []Node{list}}
	case ReshapeDropNulls:
		return &Call{Fn: FnListDropNulls, Args: []Node{list}}
	case ReshapeGet:
		return &Call{Fn: FnListGet, Args: []Node{list, arg(0)}}
	}
	return &Err{E: uerr.Internalf("expr: reshape %d has no list form", r.Op)}
}

// lowerAggOver is aggregate a over list, the list kernel of the same reduction.
func lowerAggOver(a *Agg, list Node) Node {
	call := func(fn CallFn, args ...Node) Node { return &Call{Fn: fn, Args: append([]Node{list}, args...)} }
	at := func(i int64) Node { return &Lit{Value: i, DT: dtype.Int64} }
	switch a.Op {
	case AggFirst:
		return call(FnListGet, at(0))
	case AggLast:
		return call(FnListGet, at(-1))
	case AggSum:
		return call(FnListSum)
	case AggMean:
		return call(FnListMean)
	case AggMin:
		return call(FnListMin)
	case AggMax:
		return call(FnListMax)
	case AggLen:
		return call(FnListLen)
	case AggCount:
		return &Call{Fn: FnListLen, Args: []Node{&Call{Fn: FnListDropNulls, Args: []Node{list}}}}
	case AggNUnique:
		return call(FnListNUnique)
	case AggArgMin:
		return call(FnListArgMin)
	case AggArgMax:
		return call(FnListArgMax)
	}
	return &Err{E: uerr.New(uerr.KindUnsupported, a.Op.String(),
		"%s over a reshaped column is not there yet", a.Op).
		Hint("first, last, sum, mean, min, max, len, count, n_unique, arg_min and arg_max are")}
}
