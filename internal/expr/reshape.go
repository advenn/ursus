package expr

import (
	"math"
	"strconv"
	"strings"

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
// limit rather than spill, as implode does. In a Select a reshape is a column of its
// own length, a plan of its own in the root package (step 258); anywhere else outside
// an aggregation it is refused (LooseReshape).

// ReshapeOp is a length-changing operation on a column.
type ReshapeOp uint8

const (
	ReshapeSort         ReshapeOp = iota // Args: descending, 0 or 1
	ReshapeHead                          // Args: n
	ReshapeTail                          // Args: n
	ReshapeSlice                         // Args: offset, length
	ReshapeReverse                       // no args
	ReshapeUnique                        // no args: first-seen order, null a value
	ReshapeDropNulls                     // no args
	ReshapeGet                           // Args: index; one value, not a column
	ReshapeDropNans                      // no args (step 253)
	ReshapeGatherEvery                   // Args: n, offset (step 253)
	ReshapeSortBy                        // Args: descending, 0 or 1; By the keys (step 253)
	ReshapeGather                        // no args; By the indices (step 257)
	ReshapeExplode                       // a List's elements (step 259); Args [1]: a generator's, an empty list no row (step 263)
	ReshapeArgSort                       // Args: descending, nulls last (step 267)
	ReshapeArgTrue                       // no args: the true rows' indices (step 267)
	ReshapeArgUnique                     // no args: each value's first index (step 267)
	ReshapeArgSortBy                     // Child: struct_of the keys; Args: each key's descending and nulls last (step 267)
	ReshapeMode                          // Args: maintain_order, 0 or 1: the most frequent values (step 277)
	ReshapeUniqueCounts                  // no args: each value's count, in first-seen order (step 280)
	ReshapeValueCounts                   // Args: sort, normalize; Label: the count's name; a Struct a value (step 280)
	ReshapeRle                           // no args: a Struct of each run's length and value (step 281)
	ReshapeAppend                        // By: the values appended; Args: their meeting, AppendStrict, AppendUpcast or AppendCast (step 282)
	ReshapeSample                        // Args: n, or -1 for a fraction; the fraction's bits; with_replacement; shuffle; the seed (step 282)
)

var reshapeNames = map[ReshapeOp]string{
	ReshapeSort: "sort", ReshapeHead: "head", ReshapeTail: "tail", ReshapeSlice: "slice",
	ReshapeReverse: "reverse", ReshapeUnique: "unique", ReshapeDropNulls: "drop_nulls",
	ReshapeGet: "get", ReshapeDropNans: "drop_nans", ReshapeGatherEvery: "gather_every",
	ReshapeSortBy: "sort_by", ReshapeGather: "gather", ReshapeExplode: "explode",
	ReshapeArgSort: "arg_sort", ReshapeArgTrue: "arg_true", ReshapeArgUnique: "arg_unique",
	ReshapeArgSortBy: "arg_sort_by", ReshapeMode: "mode",
	ReshapeUniqueCounts: "unique_counts", ReshapeValueCounts: "value_counts", ReshapeRle: "rle",
	ReshapeAppend: "append", ReshapeSample: "sample",
}

// How Append meets its two sides' types (step 282): the same type, as Polars'
// upcast=False; their common one, as its default; or the other side cast to this
// one's, a float truncated toward zero, as extend_constant casts its value.
const (
	AppendStrict int64 = iota
	AppendUpcast
	AppendCast
)

func (o ReshapeOp) String() string { return reshapeNames[o] }

// Reshape is a length-changing operation on Child. By is SortBy's keys and Gather's
// indices, nil for every other.
type Reshape struct {
	Op    ReshapeOp
	Child Node
	Args  []int64
	By    Node
	Label string // ValueCounts: its count's field name
}

func (r *Reshape) node() {}

func (r *Reshape) Children() []Node {
	if r.By != nil {
		return []Node{r.Child, r.By}
	}
	return []Node{r.Child}
}

func (r *Reshape) String() string {
	if g, ok := generatorText(r); ok {
		return g
	}
	args := ""
	switch r.Op {
	case ReshapeSort, ReshapeSortBy, ReshapeGather, ReshapeAppend:
		if r.Op == ReshapeAppend {
			args = r.By.String() + ", " + itoa(int(r.Args[0]))
			break
		}
		if r.By != nil {
			args = r.By.String()
		}
		if len(r.Args) > 0 && r.Args[0] != 0 {
			if args != "" {
				args += ", "
			}
			args += "descending"
		}
	default:
		for i, a := range r.Args {
			if i > 0 {
				args += ", "
			}
			args += itoa(int(a))
		}
	}
	if r.Label != "" {
		if args != "" {
			args += ", "
		}
		args += strconv.Quote(r.Label)
	}
	return r.Child.String() + "." + r.Op.String() + "(" + args + ")"
}

// AppendType is the type Append's answer is of, its sides of types a and b met by
// mode.
func AppendType(mode int64, a, b dtype.DataType) (dtype.DataType, error) {
	switch {
	case mode == AppendCast:
		return a, nil
	case a == b || b.IsNull():
		return a, nil
	case a.IsNull():
		return b, nil
	case mode == AppendUpcast:
		if t, ok := dtype.Promote(a, b); ok {
			return t, nil
		}
	}
	return dtype.Null, uerr.New(uerr.KindType, "append", "append of %s to %s, which do not meet", b, a).
		Hint("cast one first; Polars' upcast meets numbers, and text with anything")
}

// RleType is rle's Struct: each run's length, a UInt32, and its value, as Polars'.
func RleType(value dtype.DataType) dtype.DataType {
	return dtype.Struct(dtype.Field{Name: "len", Type: dtype.Uint32, Nullable: true},
		dtype.Field{Name: "value", Type: value, Nullable: true})
}

// ValueCountsType is value_counts' Struct: the value, named as its column, and its
// count, a UInt32, or with normalize its share, a Float64, named label.
func ValueCountsType(value dtype.Field, label string, normalize bool) dtype.DataType {
	count := dtype.Uint32
	if normalize {
		count = dtype.Float64
	}
	return dtype.Struct(dtype.Field{Name: value.Name, Type: value.Type, Nullable: true},
		dtype.Field{Name: label, Type: count, Nullable: true})
}

// Generators are the per-row functions whose list a scalar generator explodes,
// and the name the generator reads as (step 263).
var Generators = map[CallFn]string{
	FnIntRanges: "int_range", FnLinearSpaces: "linear_space", FnRepeatBy: "repeat",
	FnDateRanges: "date_range", FnDatetimeRanges: "datetime_range", FnTimeRanges: "time_range",
}

// IsGenerator reports whether r is a scalar generator: an explode in which an empty
// list is no row, Polars' int_range and the rest.
func (r *Reshape) IsGenerator() bool {
	return r.Op == ReshapeExplode && len(r.Args) > 0 && r.Args[0] == 1
}

// generatorText is a generator's text, int_range(lit(0), lit(5), lit(1)), apart from
// an Explode of the same list, whose empty list is a null row.
func generatorText(r *Reshape) (string, bool) {
	c, ok := r.Child.(*Call)
	if !ok || !r.IsGenerator() {
		return "", false
	}
	name, ok := Generators[c.Fn]
	if !ok {
		return "", false
	}
	parts := make([]string, len(c.Args))
	for i, a := range c.Args {
		parts[i] = a.String()
	}
	return name + "(" + strings.Join(parts, ", ") + ")", true
}

// Field is the child's: a reshape keeps its values' type, but Explode, whose values
// are its List's elements.
func (r *Reshape) Field(in *dtype.Schema) (dtype.Field, error) {
	f, err := r.Child.Field(in)
	if err != nil {
		return f, err
	}
	switch r.Op {
	case ReshapeArgTrue:
		if f.Type.ID() != dtype.TypeBool && !f.Type.IsNull() {
			return dtype.Field{}, uerr.New(uerr.KindType, "arg_true",
				"arg_true takes Booleans, and %s is a %s", f.Name, f.Type)
		}
		fallthrough
	case ReshapeArgSort, ReshapeArgUnique, ReshapeArgSortBy:
		// Indices, as Polars' are: UInt32.
		f.Type, f.Nullable = dtype.Uint32, false
		return f, nil
	case ReshapeAppend:
		g, err := r.By.Field(in)
		if err != nil {
			return dtype.Field{}, err
		}
		t, err := AppendType(r.Args[0], f.Type, g.Type)
		if err != nil {
			return dtype.Field{}, err
		}
		f.Type, f.Nullable = t, f.Nullable || g.Nullable
		return f, nil
	case ReshapeSample:
		return f, nil
	case ReshapeRle:
		// Runs of equal keys, as the group-by's keys are equal.
		if !f.Type.IsHashable() && !f.Type.IsNull() {
			return dtype.Field{}, uerr.New(uerr.KindType, "rle", "rle compares values, and %s cannot be a key", f.Type)
		}
		f.Type, f.Nullable = RleType(f.Type), false
		return f, nil
	case ReshapeUniqueCounts, ReshapeValueCounts:
		// Counted by the keys a group-by takes, as mode's are.
		if !f.Type.IsHashable() && !f.Type.IsNull() {
			return dtype.Field{}, uerr.New(uerr.KindType, r.Op.String(),
				"%s counts values, and %s cannot be a key", r.Op, f.Type)
		}
		if r.Op == ReshapeUniqueCounts {
			f.Type, f.Nullable = dtype.Uint32, false
			return f, nil
		}
		f.Type, f.Nullable = ValueCountsType(f, r.Label, len(r.Args) > 1 && r.Args[1] != 0), false
		return f, nil
	case ReshapeExplode:
	default:
		return f, nil
	}
	if f.Type.ID() != dtype.TypeList {
		return dtype.Field{}, uerr.New(uerr.KindType, "explode",
			"explode requires a List column, got %s", f.Type).
			Hint("a column that is not a List has nothing to explode")
	}
	f.Type, f.Nullable = f.Type.Inner(), true
	return f, nil
}

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
		"%s is a column of its own length, which only an aggregation and a Select take yet",
		r.String()).
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
	ReshapeExplode: "lf.Explode",
	// The .Or is required: IsNotNan is null on a null row, and Filter drops a null
	// predicate, so the bare spelling would drop the nulls too.
	ReshapeDropNans: `lf.Filter(Col("x").IsNotNan().Or(Col("x").IsNull()))`,
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
			kids := t.Children()
			out := make([]Node, len(kids))
			for i, c := range kids {
				out[i] = LowerReshapes(c, false)
			}
			return Rebuild(t, out)
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
	if r.Op == ReshapeArgSortBy {
		// One key's order is its arg_sort's. Of several, Polars 1.44.1 answers with
		// indices past the group's end; this refuses them.
		keys := r.Child.Children()
		if len(keys) != 1 {
			return &Err{E: uerr.New(uerr.KindUnsupported, "arg_sort_by",
				"%s sorts a group by %d keys, which is not there yet", r.String(), len(keys)).
				Hint("in a Select it sorts by them all; in an aggregation, by one key: its ArgSort")}
		}
		return lowerReshape(&Reshape{Op: ReshapeArgSort, Child: keys[0], Args: r.Args[:2]})
	}
	if r.Op == ReshapeExplode && (IsConstant(r.Child) || HasAgg(r.Child) && LooseReshape(r.Child) == nil) {
		// A List that is one value a group, a generator's or an aggregate's (step
		// 263): exploded, its elements are the group's values, which is the list.
		return r.Child
	}
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
	case ReshapeDropNans:
		return &Call{Fn: FnListDropNans, Args: []Node{list}}
	case ReshapeGatherEvery:
		return &Call{Fn: FnListGatherEvery, Args: []Node{list, arg(0), arg(1)}}
	case ReshapeExplode:
		return &Call{Fn: FnListExplode, Args: []Node{list}}
	case ReshapeArgSort:
		return &Call{Fn: FnListArgSort, Args: []Node{list, &Lit{Value: r.Args[0] != 0, DT: dtype.Bool},
			&Lit{Value: r.Args[1] != 0, DT: dtype.Bool}}}
	case ReshapeArgTrue:
		return &Call{Fn: FnListArgTrue, Args: []Node{list}}
	case ReshapeArgUnique:
		return &Call{Fn: FnListArgUnique, Args: []Node{list}}
	case ReshapeMode:
		return &Call{Fn: FnListMode, Args: []Node{list, &Lit{Value: r.Args[0] != 0, DT: dtype.Bool}}}
	case ReshapeUniqueCounts:
		return &Call{Fn: FnListUniqueCounts, Args: []Node{list}}
	case ReshapeRle:
		return &Call{Fn: FnListRle, Args: []Node{list}}
	case ReshapeSample:
		frac := math.Float64frombits(uint64(r.Args[1]))
		return &Call{Fn: FnListSample, Args: []Node{list, arg(0), &Lit{Value: frac, DT: dtype.Float64},
			&Lit{Value: r.Args[2] != 0, DT: dtype.Bool}, &Lit{Value: r.Args[3] != 0, DT: dtype.Bool}, arg(4)}}
	case ReshapeAppend:
		// The appended side a list a group too: a constant's one value, a generator's
		// list, a reshape's lowered, or its values imploded.
		var other Node
		switch b := r.By.(type) {
		case *Lit:
			other = &Lit{Value: []*Lit{b}, DT: dtype.List(b.DT)}
		case *Reshape:
			if b.IsGenerator() {
				other = b.Child
			} else {
				other = lowerReshape(b)
			}
		default:
			if IsConstant(b) {
				other = &Agg{Op: AggImplode, Child: &Reshape{Op: ReshapeHead, Child: b, Args: []int64{1}}}
			} else {
				other = &Agg{Op: AggImplode, Child: b}
			}
		}
		return &Call{Fn: FnListAppend, Args: []Node{list, other, arg(0)}}
	case ReshapeValueCounts:
		// The value's name beside the count's, which a list's type has no other way to
		// carry: the struct's first field is named as the column.
		return &Call{Fn: FnListValueCounts, Args: []Node{list,
			&Lit{Value: r.Args[0] != 0, DT: dtype.Bool}, &Lit{Value: r.Args[1] != 0, DT: dtype.Bool},
			&Lit{Value: r.Label, DT: dtype.String}, &Lit{Value: OutputName(r.Child), DT: dtype.String}}}
	case ReshapeSortBy:
		// The keys imploded beside the values: each group's keys are a list of its
		// length. A chain before sort_by would leave the two of different lengths.
		if _, chained := r.Child.(*Reshape); chained {
			return &Err{E: uerr.New(uerr.KindUnsupported, "sort_by",
				"%s sorts a reshaped column by keys of the group's length", r.String()).
				Hint("sort_by first, then reshape: %s", "Col(\"a\").SortBy(Col(\"b\")).Head(2)")}
		}
		if HasAgg(r.By) || LooseReshape(r.By) != nil || LooseFilter(r.By) != nil {
			return &Err{E: uerr.New(uerr.KindType, "sort_by",
				"%s sorts by keys that are not a value a row", r.String())}
		}
		fn := FnListSortBy
		if r.Args[0] != 0 {
			fn = FnListSortByDesc
		}
		return &Call{Fn: fn, Args: []Node{list, &Agg{Op: AggImplode, Child: r.By}}}
	case ReshapeGather:
		// A List literal is the same indices for every group; a column is each
		// group's own, imploded beside its values, which may be reshaped first: the
		// indices are positions, not partners.
		if l, ok := r.By.(*Lit); ok && l.DT.ID() == dtype.TypeList {
			return &Call{Fn: FnListGather, Args: []Node{list, l}}
		}
		if _, ok := r.By.(*Lit); ok {
			return &Err{E: uerr.New(uerr.KindType, "gather",
				"%s gathers by one index, and takes a List of them", r.String()).
				Hint("for one element, Get(i); for several, Gather(Lit([]int64{0, 2}))")}
		}
		if HasAgg(r.By) || LooseReshape(r.By) != nil || LooseFilter(r.By) != nil {
			return &Err{E: uerr.New(uerr.KindType, "gather",
				"%s gathers by indices that are not a value a row", r.String())}
		}
		return &Call{Fn: FnListGather, Args: []Node{list, &Agg{Op: AggImplode, Child: r.By}}}
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
	case AggMedian:
		return call(FnListMedian)
	case AggStd:
		return call(FnListStd, &Lit{Value: int64(a.Params.DDof), DT: dtype.Int64})
	case AggVar:
		return call(FnListVar, &Lit{Value: int64(a.Params.DDof), DT: dtype.Int64})
	case AggQuantile:
		return call(FnListQuantile, &Lit{Value: a.Params.Q, DT: dtype.Float64},
			&Lit{Value: int64(a.Params.Interp), DT: dtype.Int64})
	}
	return &Err{E: uerr.New(uerr.KindUnsupported, a.Op.String(),
		"%s over a reshaped column is not there yet", a.Op).
		Hint("first, last, sum, mean, min, max, len, count, n_unique, arg_min, arg_max, " +
			"median, std, var and quantile are")}
}
