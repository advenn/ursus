package expr

import (
	"strings"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// Variadic is a function whose operands are a list, which Polars writes out before
// it calls the function (step 302): sum_horizontal(pl.all()) sums every column into
// one, concat_str(pl.all()) joins them, struct(pl.all()) packs them, coalesce(...)
// takes the first value, and hash_rows hashes them. Expand writes out each of Exprs
// over the input's schema, a selection into its columns in order, and Build makes
// the node over them, as buildFolds builds a Fold.
//
// Before step 302 these were built at once, and a selection among their operands
// expanded the whole function once per column, as it expands any other expression:
// MaxHorizontal(All()) gave each column its own row's maximum, itself. A list's
// body, lowered when it is written, builds one over its operands as written
// (buildAsWritten); OutputName names one after its first operand, as the function
// built is named.
type Variadic struct {
	Op    string
	Exprs []Node
	Build func(exprs []Node) (Node, error)
}

func (v *Variadic) node() {}

func (v *Variadic) Children() []Node { return v.Exprs }

func (v *Variadic) String() string {
	parts := make([]string, len(v.Exprs))
	for i, c := range v.Exprs {
		parts[i] = c.String()
	}
	return v.Op + "(" + strings.Join(parts, ", ") + ")"
}

// Field refuses: Expand builds a Variadic before anything types it.
func (v *Variadic) Field(*dtype.Schema) (dtype.Field, error) {
	return dtype.Field{}, uerr.Internalf("expr: %s reached typing unexpanded", v.Op)
}

// rebuild is v over new operands.
func (v *Variadic) rebuild(kids []Node) *Variadic {
	c := *v
	c.Exprs = append([]Node(nil), kids...)
	return &c
}

// buildVariadics builds every Variadic in n, innermost first, over the schema in:
// its operands written out, then Build over them.
func buildVariadics(n Node, in *dtype.Schema) (Node, error) {
	kids := n.Children()
	if len(kids) > 0 {
		out := make([]Node, len(kids))
		changed := false
		for i, c := range kids {
			b, err := buildVariadics(c, in)
			if err != nil {
				return nil, err
			}
			out[i], changed = b, changed || b != c
		}
		if changed {
			n = Rebuild(n, out)
		}
	}
	v, ok := n.(*Variadic)
	if !ok {
		return n, nil
	}
	exprs, err := ExpandAll(v.Exprs, in)
	if err != nil {
		return nil, err
	}
	if len(exprs) == 0 {
		return nil, uerr.New(uerr.KindSchema, v.Op, "%s of no expression", v.Op).
			Hint("a selection that matched no column leaves nothing to take")
	}
	return v.Build(exprs)
}
