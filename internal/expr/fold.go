package expr

import (
	"strings"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// Fold is Polars' fold, reduce, cum_fold, cum_reduce and cum_sum_horizontal (step
// 266, F1 of v0.6.6-scope.md): Fn applied from an accumulator across Exprs. Polars
// runs its function over Series per batch; here Fn builds the expression once, when
// the plan is resolved, so the optimizer sees an ordinary expression and no UDF runs.
//
// Expand writes it out after the input's schema has expanded its Exprs, so a fold
// over All() folds every column, as Polars' does:
//
//   - fold and reduce are the last accumulator, named after the first one: Acc's
//     name, or a reduce's, whose accumulator is its first expression;
//   - the cumulative forms are a Struct of the running values, a field each named
//     after the expression folded in, the Struct named Op.
type Fold struct {
	Op    string // fold, reduce, cum_fold, cum_reduce or cum_sum
	Acc   Node   // nil for a reduce
	Exprs []Node
	Fn    func(acc, x Node) Node
	// Promote casts the running values to the last one's type, as Polars' fields of
	// cum_sum_horizontal are of one type.
	Promote bool
}

func (f *Fold) node() {}

func (f *Fold) Children() []Node {
	if f.Acc == nil {
		return f.Exprs
	}
	return append([]Node{f.Acc}, f.Exprs...)
}

func (f *Fold) String() string {
	parts := make([]string, 0, len(f.Exprs)+1)
	for _, c := range f.Children() {
		parts = append(parts, c.String())
	}
	return f.Op + "(" + strings.Join(parts, ", ") + ")"
}

// Field refuses: Expand writes a Fold out before anything types it.
func (f *Fold) Field(*dtype.Schema) (dtype.Field, error) {
	return dtype.Field{}, uerr.Internalf("expr: %s reached typing unexpanded", f.Op)
}

// rebuild is f over new children, in Children's order.
func (f *Fold) rebuild(kids []Node) *Fold {
	c := *f
	if f.Acc != nil {
		c.Acc, kids = kids[0], kids[1:]
	}
	c.Exprs = append([]Node(nil), kids...)
	return &c
}

// buildFolds writes out every Fold in n, innermost first, over the schema in.
func buildFolds(n Node, in *dtype.Schema) (Node, error) {
	kids := n.Children()
	if len(kids) > 0 {
		out := make([]Node, len(kids))
		changed := false
		for i, c := range kids {
			b, err := buildFolds(c, in)
			if err != nil {
				return nil, err
			}
			out[i], changed = b, changed || b != c
		}
		if changed {
			n = Rebuild(n, out)
		}
	}
	f, ok := n.(*Fold)
	if !ok {
		return n, nil
	}
	exprs, err := ExpandAll(f.Exprs, in)
	if err != nil {
		return nil, err
	}
	acc := f.Acc
	if acc != nil {
		accs, err := Expand(acc, in)
		if err != nil {
			return nil, err
		}
		if len(accs) != 1 {
			return nil, uerr.New(uerr.KindValue, f.Op, "%s's accumulator %s is %d columns, not one", f.Op, acc, len(accs))
		}
		acc = accs[0]
	}
	return f.write(acc, exprs, in)
}

// write is f over its accumulator and expressions, written out. in types the running
// values Promote casts; a list's body has no schema yet, and passes nil, refusing
// Promote.
func (f *Fold) write(acc Node, exprs []Node, in *dtype.Schema) (Node, error) {
	if f.Fn == nil {
		return nil, uerr.New(uerr.KindValue, f.Op, "%s has no function", f.Op)
	}
	if acc == nil {
		if len(exprs) == 0 {
			return nil, uerr.New(uerr.KindValue, f.Op, "%s of no expression", f.Op).
				Hint("a selection that matched no column leaves nothing to reduce")
		}
		acc, exprs = exprs[0], exprs[1:]
	}
	cumulative := strings.HasPrefix(f.Op, "cum_")
	var fields []Node
	if cumulative && f.Acc == nil {
		fields = append(fields, &Alias{Name: OutputName(acc), Child: acc})
	}
	name := OutputName(acc)
	for _, x := range exprs {
		acc = f.Fn(acc, x)
		if cumulative {
			fields = append(fields, &Alias{Name: OutputName(x), Child: acc})
		}
	}
	if !cumulative {
		return &Alias{Name: name, Child: acc}, nil
	}
	if len(fields) == 0 {
		return nil, uerr.New(uerr.KindValue, f.Op, "%s of no expression", f.Op)
	}
	if f.Promote {
		if in == nil {
			return nil, uerr.New(uerr.KindUnsupported, f.Op,
				"%s inside a list's body is not supported", f.Op).
				Hint("its fields are of one type, which a list's body cannot know when it is written; use CumFold")
		}
		last, err := fields[len(fields)-1].Field(in)
		if err != nil {
			return nil, err
		}
		for i, fl := range fields {
			a := fl.(*Alias)
			fields[i] = &Alias{Name: a.Name, Child: &Cast{Child: a.Child, To: last.Type, Strict: true}}
		}
	}
	return &Alias{Name: f.Op, Child: &Call{Fn: FnStructOf, Args: fields}}, nil
}
