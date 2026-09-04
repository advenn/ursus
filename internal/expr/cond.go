package expr

import (
	"ursus/dtype"
	"ursus/internal/uerr"
)

// Cond is a conditional: the value of Then where Pred is true, of Else otherwise.
//
// # Why this is a node and IsBetween is not
//
// IsBetween is two lines of sugar — `e.Ge(lo).And(e.Le(hi))` — and earns Field,
// evaluation, expansion, pushdown and Parquet row-group pruning for free by being
// built out of nodes that already exist. That is the cheapest way to add anything,
// and it was the first thing tried here.
//
// It does not work for a conditional, for reasons that are properties of this
// codebase rather than of conditionals in general:
//
//   - There was no masked-select kernel. Sugar has to desugar INTO something, and
//     nothing in kernel took (mask, a, b). kernel.Select was written for this node.
//   - The arithmetic dodge does not type-check. `cond*a + !cond*b` needs Bool to be
//     numeric, and dtype.Promote refuses on purpose: "true + true is rejected".
//   - There is no CSE pass, so any desugaring that names an operand twice evaluates
//     it twice.
//
// # Three children, not parallel slices
//
// A chain — when/then/when/then/otherwise — is built by the root package into a
// right-nested tree of these, rather than represented as []Pred and []Then. The
// reason is type unification: dtype.Promote is strictly binary, there is no n-ary
// helper, and a left fold is NOT safe to invent because Promote's mixed-sign rule
// (the smallest signed type holding both) makes the result depend on association
// order for operand sets like (Uint8, Int8, Uint32). Nesting makes the association
// explicit and identical to the order the user wrote.
//
// All three are ordinary children, so RootNames, Walk, expansion and projection
// pushdown see through a Cond with no special case.
type Cond struct {
	Pred Node
	Then Node
	Else Node
}

func (c *Cond) node()            {}
func (c *Cond) Children() []Node { return []Node{c.Pred, c.Then, c.Else} }

func (c *Cond) String() string {
	return "when(" + c.Pred.String() +
		").then(" + c.Then.String() +
		").otherwise(" + c.Else.String() + ")"
}

func (c *Cond) Field(in *dtype.Schema) (dtype.Field, error) {
	pf, err := c.Pred.Field(in)
	if err != nil {
		return dtype.Field{}, err
	}
	// A Null-typed predicate is an untyped null literal, which is legal and means
	// every row takes neither branch. Anything else non-Boolean is a mistake worth
	// naming at plan time.
	if !pf.Type.IsBool() && !pf.Type.IsNull() {
		return dtype.Field{}, uerr.New(uerr.KindType, "when",
			"a condition must be Boolean, got %s", pf.Type).
			Hint("compare it first, e.g. Col(%q).Gt(0)", pf.Name)
	}

	tf, err := c.Then.Field(in)
	if err != nil {
		return dtype.Field{}, err
	}
	ef, err := c.Else.Field(in)
	if err != nil {
		return dtype.Field{}, err
	}

	out, err := ResolveCond(c.Then, c.Else, tf.Type, ef.Type)
	if err != nil {
		return dtype.Field{}, err
	}

	// Nullable if EITHER branch is, or if the predicate is — a null condition takes
	// neither branch and yields null, so a nullable predicate manufactures nulls
	// that neither branch contains. This is the same reasoning that makes a
	// non-strict Cast always nullable.
	nullable := pf.Nullable || tf.Nullable || ef.Nullable
	return dtype.Field{Name: OutputName(c), Type: out, Nullable: nullable}, nil
}

// ResolveCond gives a conditional's output type from its two branches.
//
// It is the single authority, consulted by Field for the plan's schema and by the
// evaluator for the type it casts both branches to before the kernel. Two copies
// would drift, which is the Binding lesson.
//
// # Why it takes NODES as well as types
//
// Weak literal typing needs to know which branch is a literal whose type was a
// default rather than a choice, and a dtype.DataType cannot carry that. The
// evaluator in particular has only the evaluated COLUMNS by the time it asks, and
// a column has no weakness — so the nodes have to come from the call site. Both
// callers hold them, so this is a signature change rather than a redesign; the
// alternative, a second resolver that only the planner consults, is exactly the
// drift this function exists to prevent.
//
// The temporal refusal is worth a specific hint. Promote deliberately declines two
// DIFFERENT temporal types — it returns early unless both sides are numeric — even
// though CanCast permits temporal-to-temporal and kernel.Cast rescales such a pair
// correctly. Widening Promote would edit a rule every binary operator shares, so
// the conditional points at the explicit cast instead of quietly guessing.
func ResolveCond(thenN, elseN Node, then, els dtype.DataType) (dtype.DataType, error) {
	if t, ok := weakTarget(thenN, elseN, then, els); ok {
		return t, nil
	}
	out, ok := dtype.Promote(then, els)
	if ok {
		return out, nil
	}
	e := uerr.New(uerr.KindType, "when",
		"the then and otherwise branches have no common type: %s and %s", then, els)
	if then.IsTemporal() && els.IsTemporal() {
		return dtype.Null, e.Hint("cast one branch to the other's unit, e.g. .Cast(%s)", then)
	}
	return dtype.Null, e.Hint("cast one branch so both agree")
}
