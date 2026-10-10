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
//     rule for;
//   - a join whose order something above depends on: an input of an as-of join or
//     of MergeSorted, reached through nodes that keep their input's order. Those
//     refuse an unsorted input, so a swap turned a query that ran into one that
//     failed, depending on the tables' sizes;
//   - a join whose left input the caller sorted, reached the same way:
//     `Sort(ts).Join(users)` asks for rows by ts, and `.Head(n)` after it for the
//     first n of them;
//   - a float key. A merged key is taken from the side the table is built from,
//     and the hash equates -0.0 with +0.0, so a swap could change a key's sign.
//
// # The estimate
//
// EstimateRows is an upper bound through every filter, and through a join it takes
// the larger input, which is right for the key-to-foreign-key joins that dominate
// real queries. It swaps only when the right side is more than twice the left, so
// an estimate that is merely loose does not flip a join of similar sides.
//
// # Marked, for the physical join (step 162)
//
// An upper bound through every filter is wrong in both directions: PDS-H q12's
// lineitem is estimated at six million rows and its filters leave 31 thousand, so
// the rule built orders' 1.5 million keys. So every join it could exchange is also
// marked SwapAtRuntime, exchanged or not, and the physical join, which knows its
// build side's rows before it inserts a key, exchanges it again where the probe side
// turns out the smaller. Exchanging a swapped join's inputs again is the join as
// written, so the inner join of a swap is marked too.
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
	// A Cache's subtree must be rewritten the same at every site, so it keeps its
	// order if ANY site needs it: a swap below one copy and not another would make
	// the copies differ, and the physical planner runs only one (step 143).
	cacheOrdered := map[int]bool{}
	var need func(x Node, ordered bool)
	need = func(x Node, ordered bool) {
		if c, ok := x.(*Cache); ok && ordered {
			cacheOrdered[c.ID] = true
		}
		for i, c := range x.Children() {
			need(c, keepsOrderOf(x, i, ordered))
		}
	}
	need(n, false)

	// Bottom-up, as TransformUp is, but each node is told whether its parent
	// depends on its row order, which TransformUp cannot say.
	var walk func(x Node, ordered bool) Node
	walk = func(x Node, ordered bool) Node {
		if kids := x.Children(); len(kids) > 0 {
			next := make([]Node, len(kids))
			moved := false
			for i, c := range kids {
				o := keepsOrderOf(x, i, ordered)
				if cache, ok := x.(*Cache); ok {
					o = cacheOrdered[cache.ID]
				}
				next[i] = walk(c, o)
				moved = moved || next[i] != c
			}
			if moved {
				x = x.WithChildren(next)
			}
		}
		j, ok := x.(*Join)
		if !ok || ordered || !swappable(j) || sortedBelow(j.Left) {
			return x
		}
		sw := swapJoin(j)
		if sw == nil {
			return x
		}
		l, lok := EstimateRows(j.Left)
		r, rok := EstimateRows(j.Right)
		if !lok || !rok || r <= buildSideRatio*l {
			// Kept, and marked: the estimates are upper bounds, blind to filters, so
			// the physical join may still find its build side the larger (step 162).
			return markSwap(j, &changed)
		}
		changed = true
		p := sw.(*Project)
		// Exchanging the swapped join's inputs again is the join as written, so the
		// physical join may undo a swap that the filters made wrong.
		p.Input = markSwap(p.Input.(*Join), &changed)
		return p
	}
	return walk(n, false), changed, nil
}

// keepsOrderOf reports whether child i of x must keep its row order.
//
// An as-of join and MergeSorted require sorted inputs. A node that keeps its
// input's order passes its parent's need down; a join passes it to its left
// input, whose order its output follows. Everything else — a sort, a group-by, a
// union — sets its own order, so its inputs owe it none.
func keepsOrderOf(x Node, i int, ordered bool) bool {
	switch x.(type) {
	case *AsOfJoin, *MergeSorted:
		return true
	case *Join:
		return ordered && i == 0
	case *Filter, *Project, *WithColumns, *Window, *RowIndex, *Limit, *Slice, *Tail,
		*Reverse, *Explode, *Unnest, *Cache:
		return ordered
	}
	return false
}

// sortedBelow reports whether n's rows come in an order the caller chose: a Sort,
// reached through nodes that keep their input's order.
func sortedBelow(n Node) bool {
	switch t := n.(type) {
	case *Sort:
		return true
	case *Filter:
		return sortedBelow(t.Input)
	case *Project:
		return sortedBelow(t.Input)
	case *WithColumns:
		return sortedBelow(t.Input)
	case *Window:
		return sortedBelow(t.Input)
	case *RowIndex:
		return sortedBelow(t.Input)
	case *Limit:
		return sortedBelow(t.Input)
	case *Slice:
		return sortedBelow(t.Input)
	case *Tail:
		return sortedBelow(t.Input)
	case *Reverse:
		return sortedBelow(t.Input)
	case *Explode:
		return sortedBelow(t.Input)
	case *Unnest:
		return sortedBelow(t.Input)
	case *Cache:
		return sortedBelow(t.Input)
	case *Join:
		return sortedBelow(t.Left)
	}
	return false
}

// markSwap returns j marked SwapAtRuntime, noting a change if it was not.
func markSwap(j *Join, changed *bool) *Join {
	if j.SwapAtRuntime {
		return j
	}
	c := *j
	c.SwapAtRuntime = true
	*changed = true
	return &c
}

// SwapJoin is the build_side rule's rewrite, Join(R, L) under a Project that
// restores j's output exactly, or nil when it cannot be exact. The physical join
// plans it when it exchanges a SwapAtRuntime join's inputs (step 162).
func SwapJoin(j *Join) Node { return swapJoin(j) }

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
	for _, t := range orig.KeyTypes {
		if t.IsFloat() {
			return nil
		}
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
	case *Cache:
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
