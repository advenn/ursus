package plan

import (
	"github.com/advenn/ursus/internal/expr"
)

// buildSide hashes the smaller input of an inner join, by swapping the inputs
// when the right one is estimated much larger.
//
// # Why
//
// The physical join always builds its hash table from the RIGHT input and streams
// the left past it. Step 101's profile of PDS-H q7 found the build at 26% of the
// query's CPU, serial, in KeyTable.GetOrInsert: q7's port joins a few thousand
// customers to orders, and that to lineitem, so the side being hashed was the fact
// table each time while the side streamed past it was the small one.
//
// # The rewrite
//
//	Join(L, R)  →  Project(Join(R, L))
//
// The Project restores every output column's name, position and type, so nothing
// above the join can tell — the optimizer's schema check holds the rewrite to
// that. A column that would collide in the swapped join is suffixed with
// swapSuffix there and renamed back here; a merged key, which the swapped join
// merges the other way round, is mapped by its provenance in the two layouts.
// Whenever any of that cannot be done exactly, the join is left as it was: an
// optimization never fails a query.
//
// # What it gives up, and when it does not apply
//
// The output's ROW ORDER. A join emits rows in the order of the side streamed past
// the table, so a swapped join follows the right input's order. That is why only an
// inner join swaps — the one kind whose answer is symmetric — and why
// Join.MaintainOrder turns it off, as Polars' maintain_order does. It also skips:
//
//   - a residual, which belongs to a semi or anti join;
//   - a Validate, which names one side's keys as the unique ones;
//   - any input whose size is unknown: a CSV source, or a node EstimateRows has no
//     rule for.
//
// # The estimate
//
// EstimateRows is an upper bound through every filter, and through a join it takes
// the larger input, which is right for the key-to-foreign-key joins that dominate
// real queries. It swaps only when the right side is more than twice the left, so
// an estimate that is merely loose does not flip a join of similar sides.
type buildSide struct{}

func (buildSide) Name() string { return "build_side" }

// swapSuffix suffixes a colliding column inside a swapped join. It never reaches
// the output: the Project above renames every column back.
const swapSuffix = "__build_side_swap"

// buildSideRatio is how much larger the right input must be estimated before the
// join is swapped.
const buildSideRatio = 2

func (buildSide) Apply(n Node, _ Flags) (Node, bool, error) {
	changed := false
	out, err := TransformUp(n, func(x Node) (Node, error) {
		j, ok := x.(*Join)
		if !ok || !swappable(j) {
			return x, nil
		}
		l, lok := EstimateRows(j.Left)
		r, rok := EstimateRows(j.Right)
		if !lok || !rok || r <= buildSideRatio*l {
			return x, nil
		}
		if p := swapJoin(j); p != nil {
			changed = true
			return p, nil
		}
		return x, nil
	})
	return out, changed, err
}

// swappable reports whether j's inputs can be exchanged without changing its
// answer, as a multiset of rows.
func swappable(j *Join) bool {
	return j.Kind == JoinInner && !j.HasResidual() && j.Validate == ValidateNone && !j.MaintainOrder
}

// swapJoin returns Join(R, L) under a Project that restores j's output exactly, or
// nil when it cannot.
func swapJoin(j *Join) Node {
	orig, err := j.Layout()
	if err != nil {
		return nil
	}
	s := &Join{
		Left: j.Right, Right: j.Left,
		LeftOn: j.RightOn, RightOn: j.LeftOn,
		Kind: JoinInner, Suffix: swapSuffix,
		Coalesce: j.Coalesce, NullsEqual: j.NullsEqual,
	}
	sw, err := s.Layout()
	if err != nil {
		return nil
	}
	// Where each input column landed in the swapped output. Its LEFT input is j's
	// right, and its right is j's left.
	fromOrigRight := map[int]int{}
	fromOrigLeft := map[int]int{}
	for p, c := range sw.Columns {
		if c.Side == FromLeft {
			fromOrigRight[c.Index] = p
		} else {
			fromOrigLeft[c.Index] = p
		}
	}
	exprs := make([]expr.Node, len(orig.Columns))
	for i, c := range orig.Columns {
		var (
			p  int
			ok bool
		)
		switch {
		case c.Side == FromRight:
			p, ok = fromOrigRight[c.Index]
		case c.CoalesceWith >= 0:
			// A merged key. The swapped join merges the same pair into its own left
			// input's column, which is j's right key.
			p, ok = fromOrigRight[c.CoalesceWith]
			ok = ok && sw.Columns[p].CoalesceWith == c.Index
		default:
			p, ok = fromOrigLeft[c.Index]
		}
		if !ok {
			return nil
		}
		var e expr.Node = &expr.Col{Name: sw.Schema.Field(p).Name}
		if want := orig.Schema.Field(i).Name; sw.Schema.Field(p).Name != want {
			e = &expr.Alias{Child: e, Name: want}
		}
		exprs[i] = e
	}
	out := &Project{Input: s, Exprs: exprs}
	got, err := out.Schema()
	if err != nil || !got.Equal(orig.Schema) {
		return nil
	}
	return out
}

// EstimateRows estimates how many rows n produces, from the sources' own counts,
// and reports false when it cannot.
//
// It is an upper bound through everything that only removes rows — a filter, a
// group-by, a distinct — because nothing here can estimate a selectivity, and a
// bound is honest where a guess would not be. Through a join it is the larger
// input: a key-to-foreign-key join produces about as many rows as its larger side,
// and that is the shape most joins have.
func EstimateRows(n Node) (int64, bool) {
	switch t := n.(type) {
	case *Scan:
		e, ok := t.Src.(RowEstimator)
		if !ok {
			return 0, false
		}
		rows, ok := e.EstimatedRows()
		if ok && t.MaxRows > 0 && int64(t.MaxRows) < rows {
			rows = int64(t.MaxRows)
		}
		return rows, ok
	case *Filter:
		return EstimateRows(t.Input)
	case *Project:
		return EstimateRows(t.Input)
	case *WithColumns:
		return EstimateRows(t.Input)
	case *Sort:
		return EstimateRows(t.Input)
	case *Aggregate:
		return EstimateRows(t.Input)
	case *Distinct:
		return EstimateRows(t.Input)
	case *Window:
		return EstimateRows(t.Input)
	case *RowIndex:
		return EstimateRows(t.Input)
	case *Limit:
		in, ok := EstimateRows(t.Input)
		return min(in, int64(t.N)), ok
	case *Tail:
		in, ok := EstimateRows(t.Input)
		return min(in, int64(t.N)), ok
	case *Join:
		l, lok := EstimateRows(t.Left)
		if t.Kind.filtersLeft() {
			return l, lok
		}
		r, rok := EstimateRows(t.Right)
		return max(l, r), lok && rok
	case *Union:
		var total int64
		for _, in := range t.Inputs {
			n, ok := EstimateRows(in)
			if !ok {
				return 0, false
			}
			total += n
		}
		return total, true
	default:
		return 0, false
	}
}
