package expr

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// The categorical namespace (step 273, C of v0.6.6-scope.md).

// EnumPhysical is an Enum's indices' type as Polars gives it: UInt8 up to 255
// categories, UInt16 up to 65,535, and UInt32 past them. ursus stores UInt32.
func EnumPhysical(t dtype.DataType) dtype.DataType {
	switch n := len(t.Categories()); {
	case n <= 255:
		return dtype.Uint8
	case n <= 65_535:
		return dtype.Uint16
	}
	return dtype.Uint32
}

// HasCategories reports whether n holds a get_categories.
func HasCategories(n Node) bool {
	if c, ok := n.(*Call); ok && c.Fn == FnCatGetCategories {
		return true
	}
	for _, k := range n.Children() {
		if HasCategories(k) {
			return true
		}
	}
	return false
}

// BuildCategories writes each get_categories in n out. Where n is one, the explode
// is named as well, as a selection names its expressions by their outermost alias;
// below an aggregate an alias there would hide the explode from its lowering.
func BuildCategories(n Node, in *dtype.Schema) (Node, error) {
	out, err := buildCategories(n, in)
	if err != nil {
		return nil, err
	}
	if c, ok := n.(*Call); ok && c.Fn == FnCatGetCategories {
		f, err := out.Field(in)
		if err != nil {
			return nil, err
		}
		out = &Alias{Child: out, Name: f.Name}
	}
	return out, nil
}

// buildCategories writes each get_categories out as its receiver's categories, a
// String List literal exploded and named as the receiver: a column of its own length
// in a selection and a list a group in an aggregation, as Polars' are. It runs once
// the expression is expanded, when the receiver's type is known.
func buildCategories(n Node, in *dtype.Schema) (Node, error) {
	kids := n.Children()
	if len(kids) > 0 {
		out := make([]Node, len(kids))
		changed := false
		for i, c := range kids {
			b, err := buildCategories(c, in)
			if err != nil {
				return nil, err
			}
			out[i], changed = b, changed || b != c
		}
		if changed {
			n = Rebuild(n, out)
		}
	}
	c, ok := n.(*Call)
	if !ok || c.Fn != FnCatGetCategories {
		return n, nil
	}
	if len(c.Args) != 1 {
		return nil, uerr.Internalf("expr: %s with %d arguments", c.Fn, len(c.Args))
	}
	f, err := c.Args[0].Field(in)
	if err != nil {
		return nil, err
	}
	if f.Type.ID() == dtype.TypeCategorical {
		// Polars: "cat.get_categories() is deprecated. To get the distinct values
		// present in a Categorical column, use Expr.unique()" (step 351).
		return nil, uerr.New(uerr.KindType, "cat", "%s is an Enum's; a Categorical has no fixed categories", c.Fn).
			Hint("its distinct values are .Unique(), as Polars' deprecation of get_categories says")
	}
	if f.Type.ID() != dtype.TypeEnum {
		return nil, uerr.New(uerr.KindType, "cat", "%s requires an Enum operand, got %s", c.Fn, f.Type)
	}
	cats := f.Type.Categories()
	elems := make([]*Lit, len(cats))
	for i, s := range cats {
		elems[i] = &Lit{Value: s, DT: dtype.String}
	}
	// Named on the literal, which the explode and an aggregate over it keep.
	list := &Alias{Child: &Lit{Value: elems, DT: dtype.List(dtype.String)}, Name: f.Name}
	return &Reshape{Op: ReshapeExplode, Child: list}, nil
}
