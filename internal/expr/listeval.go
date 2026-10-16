package expr

import (
	"strconv"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// List().Eval and Element() (step 268, §3 of v0.6.6-scope.md): an expression run
// over each list's elements, the list its group, as Polars' list.eval.
//
// LowerListEval rewrites the body by what it does to a group, as LowerReshapes
// rewrites an aggregation's reshapes:
//
//   - a value an element, arithmetic on Element and the like, is a ListMap: the body
//     evaluated over the lists' elements end to end, the answers keeping the lists'
//     offsets. A window function over Element, rank or cum_sum, runs a list at a time;
//   - an aggregate is one value a list, the list kernel of the same reduction over the
//     ListMap of its input;
//   - a reshape and a filter are the list kernels, applied to the list, which is
//     already a group's values;
//   - a value a list inside an element's expression, Element minus its mean, is
//     computed a list at a time and read by each of its elements.
//
// The body reads nothing but Element: another column is refused, as Polars refuses
// it.

// ElementName and OuterName are the columns a ListMap's body reads its elements and
// its values a list from, names no frame's column can have.
const ElementName = "\x00element"

func OuterName(i int) string { return "\x00outer" + strconv.Itoa(i) }

// Element is a list's elements, in List().Eval's body: Polars' pl.element().
type Element struct{}

func (e *Element) node()            {}
func (e *Element) Children() []Node { return nil }
func (e *Element) String() string   { return "element()" }

func (e *Element) Field(in *dtype.Schema) (dtype.Field, error) {
	if f, ok := in.ByName(ElementName); ok {
		f.Name = ""
		return f, nil
	}
	return dtype.Field{}, uerr.New(uerr.KindUnsupported, "element",
		"element() is a list's elements, read only inside List().Eval").
		Hint("Col(\"l\").List().Eval(Element().Mul(2))")
}

// OuterRef is a ListMap body's I-th value a list, computed over the frame and read by
// each of the list's elements.
type OuterRef struct {
	I    int
	Text string // the value's expression, for the body's text
}

func (o *OuterRef) node()            {}
func (o *OuterRef) Children() []Node { return nil }
func (o *OuterRef) String() string   { return o.Text }

func (o *OuterRef) Field(in *dtype.Schema) (dtype.Field, error) {
	if f, ok := in.ByName(OuterName(o.I)); ok {
		return f, nil
	}
	return dtype.Field{}, uerr.Internalf("expr: %s read outside its list's body", o.Text)
}

// ListMap is Body evaluated over List's elements: a List of the answers with the
// list's offsets and nulls. Its children are List and Outer, the values a list its
// body reads; the body is its own, so no pass over the frame's expressions reaches
// it, and a window function in it is the list's, not the frame's.
type ListMap struct {
	List  Node
	Body  Node
	Outer []Node
}

func (m *ListMap) node()            {}
func (m *ListMap) Children() []Node { return append([]Node{m.List}, m.Outer...) }

func (m *ListMap) String() string {
	return m.List.String() + ".list.eval(" + m.Body.String() + ")"
}

// Scope is the schema the body is typed against: the list's elements and its values
// a list.
func (m *ListMap) Scope(in *dtype.Schema) (*dtype.Schema, error) {
	lf, err := m.List.Field(in)
	if err != nil {
		return nil, err
	}
	if lf.Type.ID() != dtype.TypeList {
		return nil, uerr.New(uerr.KindType, "list.eval", "list.eval requires a List column, got %s", lf.Type)
	}
	fields := []dtype.Field{{Name: ElementName, Type: lf.Type.Inner(), Nullable: true}}
	for i, o := range m.Outer {
		f, err := o.Field(in)
		if err != nil {
			return nil, err
		}
		fields = append(fields, dtype.Field{Name: OuterName(i), Type: f.Type, Nullable: true})
	}
	return dtype.NewSchema(fields...)
}

func (m *ListMap) Field(in *dtype.Schema) (dtype.Field, error) {
	lf, err := m.List.Field(in)
	if err != nil {
		return dtype.Field{}, err
	}
	scope, err := m.Scope(in)
	if err != nil {
		return dtype.Field{}, err
	}
	bf, err := m.Body.Field(scope)
	if err != nil {
		return dtype.Field{}, err
	}
	return dtype.Field{Name: lf.Name, Type: dtype.List(bf.Type), Nullable: true}, nil
}

// evalKind is what a lowered body is to a list.
type evalKind uint8

const (
	kindConst evalKind = iota // no element, no column: the same for every list
	kindElem                  // a value an element
	kindRow                   // a value a list
	kindList                  // a list a list
)

// LowerListEval is Polars' list.eval(body) of list, or with agg list.agg(body): an
// aggregate is then the value itself, where eval makes it a list of one.
func LowerListEval(list, body Node, agg bool) Node {
	op := "list.eval"
	if agg {
		op = "list.agg"
	}
	b, kind, err := lowerEvalNode(op, list, LowerReshapes(body, true))
	if err != nil {
		return &Err{E: err}
	}
	switch kind {
	case kindElem, kindConst:
		return newListMap(list, b)
	case kindList:
		return b
	}
	if agg {
		return b
	}
	// A value a list, as a list of one; a null list stays null. concat_list makes a
	// row null where a List operand is: no element of a list of the value, taken for
	// each list, is null where the list is and empty elsewhere.
	none := &Call{Fn: FnListHead, Args: []Node{newListMap(list, b), &Lit{Value: int64(0), DT: dtype.Int64}}}
	return &Call{Fn: FnConcatList, Args: []Node{none, b}}
}

// listValued are the list functions lowering produces whose answer is a list, not a
// value.
var listValued = map[CallFn]bool{
	FnListSort: true, FnListHead: true, FnListTail: true, FnListSlice: true, FnListReverse: true,
	FnListUnique: true, FnListDropNulls: true, FnListDropNans: true, FnListGather: true,
	FnListGatherOrNull: true, FnListGatherEvery: true, FnListSortBy: true, FnListSortByDesc: true,
	FnListExplode: true, FnListArgSort: true, FnListArgTrue: true, FnListArgUnique: true,
}

// lowerEvalNode lowers n of list's body, and says what it is to a list.
func lowerEvalNode(op string, list, n Node) (Node, evalKind, error) {
	switch t := n.(type) {
	case *Element:
		return n, kindElem, nil
	case *Lit:
		return n, kindConst, nil
	case *Err:
		return nil, 0, t.E
	case *Col, *Match:
		return nil, 0, uerr.New(uerr.KindUnsupported, op,
			"%s reads %s, and only Element() is read inside %s", op, n, op).
			Hint("a list's own values are Element(); join another column to the list first")
	case *Agg:
		x, kx, err := lowerEvalNode(op, list, t.Child)
		if err != nil {
			return nil, 0, err
		}
		var group Node
		switch kx {
		case kindElem, kindConst:
			group = newListMap(list, x)
		case kindList:
			group = x
		default:
			return nil, 0, uerr.New(uerr.KindType, op, "%s aggregates %s, which is one value a list", op, t)
		}
		if t.Op == AggImplode {
			return group, kindList, nil
		}
		r := lowerAggOver(t, group)
		if e, ok := r.(*Err); ok {
			return nil, 0, e.E
		}
		return r, kindRow, nil
	case *Filter:
		x, kx, err := lowerEvalNode(op, list, t.Child)
		if err != nil {
			return nil, 0, err
		}
		p, kp, err := lowerEvalNode(op, list, t.Pred)
		if err != nil {
			return nil, 0, err
		}
		if (kx != kindElem && kx != kindConst) || (kp != kindElem && kp != kindConst) {
			return nil, 0, uerr.New(uerr.KindUnsupported, op, "%s filters %s by a predicate of another length", op, t)
		}
		// The values where the predicate is true: their positions, gathered.
		keep := &Call{Fn: FnListArgTrue, Args: []Node{newListMap(list, p)}}
		return &Call{Fn: FnListGather, Args: []Node{newListMap(list, x), keep}}, kindList, nil
	case *Window:
		if len(t.PartitionBy) > 0 {
			return nil, 0, uerr.New(uerr.KindUnsupported, op,
				"%s partitions %s, and inside %s a window is the list", op, t, op)
		}
	}
	kids := n.Children()
	if len(kids) == 0 {
		return n, kindConst, nil
	}
	out := make([]Node, len(kids))
	kinds := make([]evalKind, len(kids))
	var elems, lists, rows int
	for i, c := range kids {
		x, k, err := lowerEvalNode(op, list, c)
		if err != nil {
			return nil, 0, err
		}
		out[i], kinds[i] = x, k
		switch k {
		case kindElem:
			elems++
		case kindList:
			lists++
		case kindRow:
			rows++
		}
	}
	n = Rebuild(n, out)
	if c, ok := n.(*Call); ok && c.Fn.IsList() && lists > 0 {
		// A list function lowering made, of the list or one made of it.
		if listValued[c.Fn] {
			return n, kindList, nil
		}
		return n, kindRow, nil
	}
	switch {
	case lists > 0 && elems > 0:
		return nil, 0, uerr.New(uerr.KindUnsupported, op,
			"%s combines a list's elements with a list of another length: %s", op, n)
	case lists > 1:
		return nil, 0, uerr.New(uerr.KindUnsupported, op, "%s combines two lists: %s", op, n)
	case lists == 1:
		// Values a list beside one list: each of its elements, by the same ListMap.
		var inner Node
		for i, k := range kinds {
			if k == kindList {
				inner, out[i] = out[i], &Element{}
			}
		}
		return newListMap(inner, Rebuild(n, out)), kindList, nil
	case elems > 0:
		return n, kindElem, nil
	case rows > 0:
		return n, kindRow, nil
	}
	return n, kindConst, nil
}

// newListMap is body over list's elements, each largest part of body that is a value
// a list, reading no element and not a constant, made an OuterRef.
func newListMap(list, body Node) *ListMap {
	m := &ListMap{List: list}
	m.Body = outerRefs(body, m)
	return m
}

func outerRefs(n Node, m *ListMap) Node {
	if !freeElement(n) && !constantOnly(n) {
		ref := &OuterRef{I: len(m.Outer), Text: n.String()}
		m.Outer = append(m.Outer, n)
		return ref
	}
	kids := n.Children()
	if len(kids) == 0 {
		return n
	}
	out := make([]Node, len(kids))
	for i, c := range kids {
		out[i] = outerRefs(c, m)
	}
	return Rebuild(n, out)
}

// freeElement reports whether n reads Element outside a ListMap of its own.
func freeElement(n Node) bool {
	if _, ok := n.(*Element); ok {
		return true
	}
	for _, c := range n.Children() {
		if freeElement(c) {
			return true
		}
	}
	return false
}

// constantOnly reports whether n is literals alone.
func constantOnly(n Node) bool {
	switch n.(type) {
	case *Lit:
		return true
	case *Element, *OuterRef, *Col, *Match, *ListMap:
		return false
	}
	for _, c := range n.Children() {
		if !constantOnly(c) {
			return false
		}
	}
	return len(n.Children()) > 0
}
