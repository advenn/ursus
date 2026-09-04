package plan

import (
	"errors"
	"strings"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// JoinSide says which input an output column came from.
type JoinSide uint8

const (
	FromLeft JoinSide = iota
	FromRight
)

// JoinColumn is the provenance of one output column, positionally aligned with
// JoinLayout.Schema.
type JoinColumn struct {
	Side  JoinSide
	Index int // ordinal in THAT side's input schema

	// CoalesceWith is the RIGHT input ordinal whose value merges into this column,
	// or -1. Only ever set on a FromLeft column. The operator emits
	// coalesce(left, right); Schema records the promoted type.
	CoalesceWith int
}

// JoinLayout is the single authority on a join's output.
//
// # Why this returns more than a schema
//
// Step 2's audit found WithColumns.Schema() and resolveWithColumns() had
// implemented DIFFERENT semantics, which surfaced as "the optimizer produced a
// plan whose schema does not resolve". The fix was one function.
//
// A join has a worse version of the same exposure, because the physical operator
// needs more than the schema: it needs to know, per output column, which side and
// which ordinal to gather from, and which right column to merge with. Returning
// only a *dtype.Schema would force the operator to re-derive "which right columns
// did I drop, which did I rename" — a second implementation of the collision
// algorithm, which would diverge.
//
// So Layout returns everything, and Schema() is a one-line projection of it.
type JoinLayout struct {
	Schema  *dtype.Schema
	Columns []JoinColumn // len == Schema.Len()

	// KeyTypes[i] is the promoted type in which key pair i is COMPARED. Both sides
	// cast to it. This is the expr.Binding lesson: the evaluator must use the
	// resolved type rather than re-deriving it, or two copies of the promotion
	// rules drift.
	KeyTypes []dtype.DataType
}

// Layout computes the join's output from its children's schemas.
//
// It is the ONLY definition of the collision, suffixing and coalescing rules.
// Join.Schema, resolveJoin and the physical planner all go through it.
//
// Deliberately NOT cached on the node: projection pushdown narrows a child via
// WithChildren, and a cached layout would go stale exactly then. WithColumns.Schema
// re-walks on every call for the same reason.
func (j *Join) Layout() (*JoinLayout, error) {
	ls, err := j.Left.Schema()
	if err != nil {
		return nil, err
	}
	rs, err := j.Right.Schema()
	if err != nil {
		return nil, err
	}

	keyTypes, err := j.keyTypes(ls, rs)
	if err != nil {
		return nil, err
	}

	// Semi and Anti are filters over the left frame: no right column is ever
	// added, so no collision and no coalescing is possible. This is an equality,
	// not an approximation.
	if j.Kind.filtersLeft() {
		cols := make([]JoinColumn, ls.Len())
		for i := range cols {
			cols[i] = JoinColumn{Side: FromLeft, Index: i, CoalesceWith: -1}
		}
		return &JoinLayout{Schema: ls, Columns: cols, KeyTypes: keyTypes}, nil
	}

	// Which right key columns are merged away, and which left column absorbs each.
	drop := map[int]bool{}
	pairKey := map[int]int{} // left ordinal -> key index
	pairRight := map[int]int{}
	for i := range j.LeftOn {
		lc := bareColumn(j.LeftOn[i], ls)
		rc := bareColumn(j.RightOn[i], rs)
		if lc < 0 || rc < 0 {
			// A computed key merges nothing: there is no column to merge.
			if j.Coalesce == CoalesceOn {
				return nil, uerr.New(uerr.KindValue, "join",
					"cannot coalesce key %d: it is a computed expression, not a column", i+1).
					Hint("left key:  %s", j.LeftOn[i].String()).
					Hint("right key: %s", j.RightOn[i].String())
			}
			continue
		}
		if !j.coalescesKey(ls.Field(lc).Name, rs.Field(rc).Name) {
			continue
		}
		drop[rc] = true
		pairKey[lc] = i
		pairRight[lc] = rc
	}

	fields := make([]dtype.Field, 0, ls.Len()+rs.Len())
	cols := make([]JoinColumn, 0, ls.Len()+rs.Len())

	// Left columns, in left order.
	for i, f := range ls.All() {
		merge := -1
		if ki, ok := pairKey[i]; ok {
			merge = pairRight[i]
			// The merged column takes the PROMOTED type, for every kind. Taking the
			// left type would put right-side values into a narrower column on a
			// Right or Full join, and making it kind-dependent would make the output
			// schema depend on Kind for no benefit.
			f = f.WithType(keyTypes[ki])
		}
		if j.Kind.leftCanBeNull() {
			f = f.AsNullable()
		}
		fields = append(fields, f)
		cols = append(cols, JoinColumn{Side: FromLeft, Index: i, CoalesceWith: merge})
	}

	// Right columns, in right order, minus the merged keys. The collision test is
	// against the LEFT name set, because left names are never renamed.
	for i, f := range rs.All() {
		if drop[i] {
			continue
		}
		if ls.Has(f.Name) {
			f = f.Rename(f.Name + j.suffix())
		}
		if j.Kind.rightCanBeNull() {
			f = f.AsNullable()
		}
		fields = append(fields, f)
		cols = append(cols, JoinColumn{Side: FromRight, Index: i, CoalesceWith: -1})
	}

	if err := j.checkNames(fields, ls, rs); err != nil {
		return nil, err
	}

	s, err := dtype.NewSchema(fields...)
	if err != nil {
		// checkNames should have caught every duplicate with a join-shaped message.
		// Reaching here means it missed a case.
		return nil, uerr.Wrap(err, uerr.KindInternal, "join",
			"join layout produced a schema NewSchema rejected")
	}
	return &JoinLayout{Schema: s, Columns: cols, KeyTypes: keyTypes}, nil
}

// keyTypes resolves each key pair against its OWN side and promotes.
func (j *Join) keyTypes(ls, rs *dtype.Schema) ([]dtype.DataType, error) {
	if len(j.LeftOn) != len(j.RightOn) {
		return nil, uerr.New(uerr.KindValue, "join",
			"join has %d left keys and %d right keys", len(j.LeftOn), len(j.RightOn))
	}
	out := make([]dtype.DataType, len(j.LeftOn))
	for i := range j.LeftOn {
		lf, err := expr.Resolve(j.LeftOn[i], ls)
		if err != nil {
			return nil, annotateKeySide(err, "left", i, ls, rs)
		}
		rf, err := expr.Resolve(j.RightOn[i], rs)
		if err != nil {
			return nil, annotateKeySide(err, "right", i, rs, ls)
		}
		kt, ok := dtype.Promote(lf.Type, rf.Type)
		if !ok {
			return nil, uerr.New(uerr.KindType, "join",
				"cannot join key %s to %s: no common type for %s and %s",
				j.LeftOn[i].String(), j.RightOn[i].String(), lf.Type, rf.Type).
				Hint("cast one side explicitly, e.g. .Cast(ursus.Int64)")
		}
		if !kt.IsHashable() {
			return nil, uerr.New(uerr.KindType, "join",
				"cannot join on a %s key", kt).
				Hint("join keys must be hashable: numeric, temporal, string or boolean")
		}
		out[i] = kt
	}
	return out, nil
}

// annotateKeySide improves an unknown-column error from a key expression.
//
// Each side's keys resolve against its OWN schema, so the "available" list names
// one frame. Without the hint below, a user who wrote JoinOn for columns that are
// named differently on the two sides gets a list that does not contain their
// column and no indication that it exists on the other side — which is the single
// most likely mistake in the first two-child node.
func annotateKeySide(err error, side string, i int, own, other *dtype.Schema) error {
	e := uerr.Annotate(err, "join", "")
	var ue *uerr.Error
	if !errors.As(e, &ue) || ue.Kind != uerr.KindSchema {
		return e
	}
	ue.Hint("this is the %s side of key %d", side, i+1)
	for _, n := range other.Names() {
		if strings.Contains(ue.Msg, "\""+n+"\"") {
			ue.Hint("the other frame has a column named %q", n)
			ue.Hint("use JoinLeftOn(...) / JoinRightOn(...) when the key columns are named differently")
			break
		}
	}
	return ue
}

// checkNames reports a duplicate with a message that names the collision, rather
// than letting NewSchema report an anonymous duplicate position.
//
// The check runs over the COMPLETE emitted list, not just against the left set,
// because a suffixed right name can collide with another suffixed right name:
//
//	left {a} ⋈ right {a, a_right}  ->  "a" suffixes to "a_right", which right also emits
//
// Never suffix twice. "a_right_right" is a name nobody asked for, it depends on
// iteration order, and the second application would need its own recursive check.
func (j *Join) checkNames(fields []dtype.Field, ls, rs *dtype.Schema) error {
	seen := make(map[string]int, len(fields))
	for i, f := range fields {
		prev, dup := seen[f.Name]
		if !dup {
			seen[f.Name] = i
			continue
		}
		e := uerr.New(uerr.KindSchema, "join",
			"joining these frames produces two columns named %q", f.Name)
		if orig := strings.TrimSuffix(f.Name, j.suffix()); orig != f.Name && ls.Has(orig) {
			e.Hint("the right frame's %q collides with the left frame's %q and was suffixed with %q",
				orig, orig, j.suffix())
			e.Hint("but %q is already taken", f.Name)
		} else {
			e.Hint("output positions %d and %d", prev, i)
		}
		e.Hint("use JoinSuffix(...) to choose a different suffix, or rename a column before the join")
		e.Hint("left columns:  %s", strings.Join(ls.Names(), ", "))
		e.Hint("right columns: %s", strings.Join(rs.Names(), ", "))
		return e
	}
	return nil
}

// bareColumn returns the schema ordinal of a key that is a plain column
// reference, or -1.
//
// This is what makes "merge the key column" well defined rather than a guess: a
// computed key has no column to merge, and LeftOn(Col("a")) / RightOn(Col("b"))
// has two differently-named ones.
func bareColumn(e expr.Node, s *dtype.Schema) int {
	c, ok := stripNaming(e).(*expr.Col)
	if !ok {
		return -1
	}
	return s.IndexOf(c.Name)
}
