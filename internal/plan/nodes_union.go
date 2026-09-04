package plan

import (
	"strconv"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// ConcatMode says how much a Union will reconcile.
type ConcatMode uint8

const (
	// ConcatStrict requires the same columns, in the same order. Types still
	// promote and nullability still widens — see Union's doc for why those are not
	// a relaxation.
	ConcatStrict ConcatMode = iota

	// ConcatDiagonal takes the union of the columns, filling a frame's missing ones
	// with nulls.
	ConcatDiagonal

	concatModeCount
)

var concatModeNames = [concatModeCount]string{
	ConcatStrict: "strict", ConcatDiagonal: "diagonal",
}

func (m ConcatMode) String() string {
	// The `!= ""` half is the load-bearing one. concatModeNames is sized by the enum's
	// sentinel, so `int(m) < len(...)` is true for every DECLARED constant and this
	// fallback would be unreachable without it — a forgotten entry would render as
	// the EMPTY STRING, and two ops both missing names would render identically and
	// collide in the three maps that dedup on String(). dtype.TypeID.String() has
	// had the correct guard since step 1; these eight did not.
	if int(m) < len(concatModeNames) && concatModeNames[m] != "" {
		return concatModeNames[m]
	}
	return "?"
}

// Union stacks frames vertically — the first n-ary node in the plan.
//
// # Arity was the cheap part; reconciliation is the cost
//
// Join's doc records that adding the first TWO-child node cost almost nothing in
// traversal — "the generic traversals were already written N-ary and need nothing"
// — and everything in SIDEDNESS. A Union has no sides: every child produces the
// same output, so none of Join's layout, suffixing or per-side pushdown machinery
// has an analogue here.
//
// What it has instead is a schema that is a function of ALL its children jointly,
// where every other node's is a function of one.
//
// # Strict is not schema equality
//
// dtype.Field compares by struct equality, and that includes Nullable — which is "a
// static property of the schema, not a count of nulls actually present". So two
// frames differing only in a nullability flag are NOT Equal, and refusing to
// concatenate them would be absurd: it is the ordinary result of filtering one of
// them. Strict therefore means "the same columns in the same order", with types
// promoted and nullability unioned.
//
// # The schema is never cached
//
// Deliberately, for the reason JoinLayout gives: projection pushdown narrows a
// child through WithChildren, and a cached reconciliation would go stale exactly
// then.
type Union struct {
	Inputs []Node
	Mode   ConcatMode
}

func (u *Union) planNode()        {}
func (u *Union) Children() []Node { return u.Inputs }

// WithChildren cannot assert an arity, which every other node does.
//
// Join panics on != 2 and the single-child nodes on != 1, so a rewrite that loses
// a child is caught immediately. A Union takes any number, so the only check left
// is that it kept at least one.
func (u *Union) WithChildren(kids []Node) Node {
	if len(kids) == 0 {
		panic("plan: Union takes at least one child")
	}
	c := *u
	c.Inputs = append([]Node(nil), kids...)
	return &c
}

func (u *Union) Schema() (*dtype.Schema, error) {
	schemas := make([]*dtype.Schema, len(u.Inputs))
	for i, in := range u.Inputs {
		s, err := in.Schema()
		if err != nil {
			return nil, err
		}
		schemas[i] = s
	}
	return reconcileSchemas(schemas, u.Mode)
}

func (u *Union) Label() string {
	return "UNION " + u.Mode.String() + " (" + strconv.Itoa(len(u.Inputs)) + " inputs)"
}

// reconcileSchemas computes the output schema of stacking schemas under mode.
//
// It was exported on the argument that the physical planner would need the same
// answer the plan reports. It never did — resolveUnion adapts every child inside
// the plan, so by the time the physical planner sees a Union the schemas already
// agree and it just asks the node. Unexported until something outside actually
// needs it.
func reconcileSchemas(schemas []*dtype.Schema, mode ConcatMode) (*dtype.Schema, error) {
	if len(schemas) == 0 {
		return nil, uerr.Internalf("plan: cannot reconcile zero schemas")
	}
	if len(schemas) == 1 {
		return schemas[0], nil
	}

	// Column order is FIRST APPEARANCE across the inputs, in order. Any rule
	// derived from map iteration would make the output schema differ between runs
	// of the same query.
	var names []string
	seen := map[string]int{}
	for _, s := range schemas {
		for _, f := range s.FieldSlice() {
			if _, dup := seen[f.Name]; !dup {
				seen[f.Name] = len(names)
				names = append(names, f.Name)
			}
		}
	}

	if mode == ConcatStrict {
		for i, s := range schemas {
			if err := strictMatch(schemas[0], s, i); err != nil {
				return nil, err
			}
		}
	}

	fields := make([]dtype.Field, len(names))
	for i, name := range names {
		var acc dtype.Field
		have := false
		missing := false
		for fi, s := range schemas {
			f, ok := s.ByName(name)
			if !ok {
				// Absent here, so this column is null for every row this frame
				// contributes — which makes the result nullable regardless of what
				// the frames that do have it declare.
				missing = true
				continue
			}
			if !have {
				acc, have = f, true
				continue
			}
			t, ok := dtype.Promote(acc.Type, f.Type)
			if !ok {
				return nil, uerr.New(uerr.KindType, "concat",
					"cannot stack column %q: no common type for %s and %s",
					name, acc.Type, f.Type).
					Hint("frame 0 has it as %s, frame %d as %s", acc.Type, fi, f.Type).
					Hint("cast one side explicitly, e.g. .Cast(ursus.Float64)")
			}
			acc = acc.WithType(t)
			if f.Nullable {
				acc = acc.AsNullable()
			}
		}
		if missing {
			acc = acc.AsNullable()
		}
		fields[i] = acc
	}
	return dtype.NewSchema(fields...)
}

// strictMatch reports whether s has the same columns as want, in the same order.
func strictMatch(want, s *dtype.Schema, idx int) error {
	if want.Len() != s.Len() {
		return uerr.New(uerr.KindSchema, "concat",
			"frame %d has %d columns, frame 0 has %d", idx, s.Len(), want.Len()).
			Hint("frame 0: %s", want).
			Hint("frame %d: %s", idx, s).
			Hint("use the diagonal mode to stack frames with different columns")
	}
	for i := range want.Len() {
		if a, b := want.Field(i).Name, s.Field(i).Name; a != b {
			return uerr.New(uerr.KindSchema, "concat",
				"frame %d has %q at position %d where frame 0 has %q", idx, b, i, a).
				Hint("strict concat matches columns by POSITION, so the order must " +
					"agree as well as the names").
				Hint("use the diagonal mode to match by name instead")
		}
	}
	return nil
}

// HStack places frames side by side: same rows, columns concatenated.
//
// # Structurally unlike Union, despite reading like its twin
//
// Union appends rows and can stream, because a row belongs to exactly one child.
// HStack pairs rows ACROSS children, and nothing in the engine aligns batch
// boundaries between independent pipelines — child A may deliver 8192 rows while
// child B delivers 100. So it buffers, and it is the only operation here that has
// to.
//
// Column names must be distinct across the inputs. Silently suffixing, as the join
// does, would be wrong: a join has a principled left/right asymmetry to name the
// suffix after, and here the inputs are peers.
type HStack struct{ Inputs []Node }

func (h *HStack) planNode()        {}
func (h *HStack) Children() []Node { return h.Inputs }

func (h *HStack) WithChildren(kids []Node) Node {
	if len(kids) == 0 {
		panic("plan: HStack takes at least one child")
	}
	return &HStack{Inputs: append([]Node(nil), kids...)}
}

func (h *HStack) Schema() (*dtype.Schema, error) {
	var fields []dtype.Field
	at := map[string]int{}
	for i, in := range h.Inputs {
		s, err := in.Schema()
		if err != nil {
			return nil, err
		}
		for _, f := range s.FieldSlice() {
			if prev, dup := at[f.Name]; dup {
				return nil, uerr.New(uerr.KindSchema, "hstack",
					"column %q appears in both frame %d and frame %d", f.Name, prev, i).
					Hint("rename one of them first — hstack will not suffix, because " +
						"its inputs are peers and there is no side to name a suffix after")
			}
			at[f.Name] = i
			fields = append(fields, f)
		}
	}
	return dtype.NewSchema(fields...)
}

func (h *HStack) Label() string {
	return "HSTACK (" + strconv.Itoa(len(h.Inputs)) + " inputs)"
}
