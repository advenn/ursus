package plan

import (
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// resolveUnion reconciles the inputs' schemas and makes every child produce the
// result schema exactly.
//
// # Adaptation is a Project, not something inside the operator
//
// A child that is missing a column, or has one at a narrower type, is wrapped in a
// Project that supplies the null and performs the cast. Three things follow, and
// all three are the reason it is done here rather than in the concat operator:
//
//   - kernel.Concat pairs columns POSITIONALLY and derives every name and dtype
//     from batch 0. It never checks the schema it is handed. So reconciliation has
//     to happen before it, or a mismatch becomes a spliced column rather than an
//     error.
//   - Explain shows the padding, so "why is this column all null" has a visible
//     answer in the plan.
//   - Projection pushdown then narrows each child independently through an ordinary
//     Project, with no Union-specific rule needed.
//
// A child whose schema already matches is left alone, so the common case — frames
// with identical schemas — adds no node at all.
func resolveUnion(u *Union) (Node, error) {
	if len(u.Inputs) == 0 {
		return nil, uerr.New(uerr.KindSchema, "concat",
			"concat requires at least one frame")
	}
	if len(u.Inputs) == 1 {
		return u.Inputs[0], nil // nothing to stack; the node would be a no-op
	}

	out, err := u.Schema()
	if err != nil {
		return nil, err
	}

	adapted := make([]Node, len(u.Inputs))
	changed := false
	for i, in := range u.Inputs {
		cs, err := in.Schema()
		if err != nil {
			return nil, err
		}
		if cs.Equal(out) {
			adapted[i] = in
			continue
		}
		exprs := make([]expr.Node, out.Len())
		for j, f := range out.FieldSlice() {
			cf, ok := cs.ByName(f.Name)
			if !ok {
				// Absent from this frame: every row it contributes is null here.
				// A typed null literal, aliased, so the column lands with the right
				// name and type without a value ever being read.
				exprs[j] = &expr.Alias{
					Child: &expr.Lit{Value: nil, DT: f.Type},
					Name:  f.Name,
				}
				continue
			}
			var e expr.Node = &expr.Col{Name: f.Name}
			if cf.Type != f.Type {
				// Strict: Promote only ever chose a type that holds both exactly, so
				// a value that fails to convert is a bug rather than a lossy
				// conversion anyone asked for.
				e = &expr.Cast{Child: e, To: f.Type, Strict: true}
				e = &expr.Alias{Child: e, Name: f.Name}
			}
			exprs[j] = e
		}
		adapted[i] = &Project{Input: in, Exprs: exprs}
		changed = true
	}

	if !changed {
		return u, nil
	}
	c := *u
	c.Inputs = adapted
	return &c, nil
}
