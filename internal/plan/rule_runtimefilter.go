package plan

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
)

// runtimeFilters places, below an inner or semi join's probe side, a filter by the
// keys its build side will hold (step 167): v0.6 item 6.
//
// # Why
//
// A join builds before it probes (joinBreaker), so when its probe side starts its
// keys are known. A probe row whose key is not among them joins to nothing. Where
// the probe key comes unchanged from below other operators, dropping those rows
// there saves every operator between. PDS-H q2 joins part, filtered to about 750
// rows at SF=1, to a probe side that is partsupp's 800,000 rows joined to the
// supplier chain first; filtered by part's keys at the scan, partsupp is about 3,000.
//
// # Where it goes
//
// Down the probe side from the join, by the key's name, through what keeps a row's
// key as it is: a Filter, a Project that passes the column or renames it, a
// WithColumns that leaves it alone, another RuntimeFilter, and the side of a join
// that join does not null-extend, the side the column comes from. The join must
// carry no Validate, whose duplicate check a filter below could starve. The column
// must have the outer join's key type all the way down, so a key promoted at a join
// between (a Date met by a Datetime) stops it there. It never enters a Cache, which
// another consumer reads too, and it is placed only once it has passed a join: just
// below the join itself it would do the probe's own lookups again.
//
// # Which joins
//
// Inner and semi joins, whose probe rows without a match are dropped, on one key of
// an integer type the probe column already has, so the published keys and the
// filtered column compare as the same integers; outside NullsEqual, so a null key
// matches nothing either way. The join publishes its keys only from a partitioned
// build of integer keys (physical.joinBuildSink.Probe); until it does, and if it
// never does, the filter keeps every row. So a row is dropped only if it cannot join.
type runtimeFilters struct{}

func (runtimeFilters) Name() string { return "runtime_filters" }

func (runtimeFilters) Apply(n Node, _ Flags) (Node, bool, error) {
	ids := 0
	changed := false
	// Every site of a Cache must read the same subtree: the first site's rewrite
	// is every site's.
	caches := map[int]Node{}
	out, err := TransformUp(n, func(x Node) (Node, error) {
		if c, ok := x.(*Cache); ok {
			if first, seen := caches[c.ID]; seen {
				return first, nil
			}
			caches[c.ID] = c
			return c, nil
		}
		j, ok := x.(*Join)
		if !ok {
			return x, nil
		}
		key, kt, ok := filterableJoin(j)
		if !ok {
			return x, nil
		}
		slot := &RuntimeSlot{ID: ids + 1}
		left, placed := placeFilter(j.Left, key, kt, slot, false)
		if !placed {
			return x, nil
		}
		ids++
		changed = true
		c := *j
		c.Left, c.Publish = left, slot
		return &c, nil
	})
	return out, changed, err
}

// filterableJoin reports whether j may filter its probe side by its built keys, and
// the probe key's name and type.
func filterableJoin(j *Join) (string, dtype.DataType, bool) {
	if (j.Kind != JoinInner && j.Kind != JoinSemi) || len(j.LeftOn) != 1 || j.NullsEqual ||
		j.Validate != ValidateNone || j.Publish != nil {
		return "", dtype.DataType{}, false
	}
	col, ok := j.LeftOn[0].(*expr.Col)
	if !ok {
		return "", dtype.DataType{}, false
	}
	layout, err := j.Layout()
	if err != nil {
		return "", dtype.DataType{}, false
	}
	kt := layout.KeyTypes[0]
	if !intKeyType(kt) || !hasField(j.Left, col.Name, kt) {
		return "", dtype.DataType{}, false
	}
	return col.Name, kt, true
}

// intKeyType reports whether dt is stored as an integer of at most 64 bits, which is
// what a join keys in integer tables (kernel.IsIntKey).
func intKeyType(dt dtype.DataType) bool {
	switch dt.Physical().ID() {
	case dtype.TypeInt8, dtype.TypeInt16, dtype.TypeInt32, dtype.TypeInt64,
		dtype.TypeUint8, dtype.TypeUint16, dtype.TypeUint32, dtype.TypeUint64:
		return true
	}
	return false
}

// hasField reports whether n has a column name of type dt.
func hasField(n Node, name string, dt dtype.DataType) bool {
	s, err := n.Schema()
	if err != nil {
		return false
	}
	f, ok := s.ByName(name)
	return ok && f.Type == dt
}

// placeFilter places a RuntimeFilter on key, of type kt, as deep in n as the key
// flows unchanged, and reports whether it did. passed is whether a join is above n
// on the way down from the filtering join, without which nothing is placed.
func placeFilter(n Node, key string, kt dtype.DataType, slot *RuntimeSlot, passed bool) (Node, bool) {
	descend := func(i int, name string, passed bool) (Node, bool) {
		kids := n.Children()
		if !hasField(kids[i], name, kt) {
			return nil, false
		}
		in, ok := placeFilter(kids[i], name, kt, slot, passed)
		if !ok {
			return nil, false
		}
		next := append([]Node(nil), kids...)
		next[i] = in
		return n.WithChildren(next), true
	}
	switch t := n.(type) {
	case *Filter, *RuntimeFilter:
		if out, ok := descend(0, key, passed); ok {
			return out, true
		}
	case *Project:
		if src, ok := passedThrough(t.Exprs, key); ok {
			if out, ok := descend(0, src, passed); ok {
				return out, true
			}
		}
	case *WithColumns:
		if !defines(t.Exprs, key) {
			if out, ok := descend(0, key, passed); ok {
				return out, true
			}
		}
	case *Join:
		if side, name, ok := keySide(t, key); ok {
			if out, ok := descend(side, name, true); ok {
				return out, true
			}
		}
	}
	if !passed {
		return n, false
	}
	return &RuntimeFilter{Input: n, Key: key, Slot: slot}, true
}

// passedThrough is the input column a Project's output key is, as itself or renamed.
func passedThrough(exprs []expr.Node, key string) (string, bool) {
	for _, e := range exprs {
		if expr.OutputName(e) != key {
			continue
		}
		switch t := e.(type) {
		case *expr.Col:
			return t.Name, true
		case *expr.Alias:
			if c, ok := t.Child.(*expr.Col); ok {
				return c.Name, true
			}
		}
		return "", false
	}
	return "", false
}

// defines reports whether a WithColumns writes key.
func defines(exprs []expr.Node, key string) bool {
	for _, e := range exprs {
		if expr.OutputName(e) == key {
			return true
		}
	}
	return false
}

// keySide is the input of j a column of its output comes from, and its name there:
// only a side j keeps every row's key on, which is not one it null-extends, and not
// through a join that validates.
func keySide(j *Join, key string) (int, string, bool) {
	if j.Validate != ValidateNone {
		return 0, "", false
	}
	layout, err := j.Layout()
	if err != nil {
		return 0, "", false
	}
	i := layout.Schema.IndexOf(key)
	if i < 0 {
		return 0, "", false
	}
	ls, err := j.Left.Schema()
	if err != nil {
		return 0, "", false
	}
	rs, err := j.Right.Schema()
	if err != nil {
		return 0, "", false
	}
	jc := layout.Columns[i]
	leftKept := j.Kind == JoinInner || j.Kind == JoinLeft || j.Kind == JoinCross ||
		j.Kind.filtersLeft()
	rightKept := j.Kind == JoinInner || j.Kind == JoinRight || j.Kind == JoinCross
	switch {
	case jc.Side == FromLeft && leftKept:
		return 0, ls.Field(jc.Index).Name, true
	case jc.Side == FromRight && rightKept:
		return 1, rs.Field(jc.Index).Name, true
	}
	return 0, "", false
}
