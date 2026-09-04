package plan

import (
	"strings"

	"ursus/dtype"
	"ursus/internal/expr"
	"ursus/internal/uerr"
)

// AsOfStrategy picks which neighbouring right row a left row matches.
type AsOfStrategy uint8

const (
	// AsOfBackward is the default: the LAST right key at or before the left key.
	// "Attach the most recent quote to each trade."
	AsOfBackward AsOfStrategy = iota
	// AsOfForward is the first right key at or after the left key.
	AsOfForward
	// AsOfNearest is whichever of those two is closer, ties going backward.
	AsOfNearest

	asOfCount
)

var asOfNames = [asOfCount]string{
	AsOfBackward: "backward", AsOfForward: "forward", AsOfNearest: "nearest",
}

func (s AsOfStrategy) String() string {
	// The `!= ""` half is load-bearing — see Interpolation.String, which records why
	// an unnamed constant rendering as the empty string is worse than "?".
	if int(s) < len(asOfNames) && asOfNames[s] != "" {
		return asOfNames[s]
	}
	return "?"
}

// AsOfJoin matches each left row to the NEAREST right row rather than to an equal
// one.
//
// # It is a LEFT join with a different match rule
//
// Every left row survives; the right columns are null when nothing is near enough.
// That is why Layout below delegates to a synthesised Join rather than deriving a
// second output schema: join_layout.go calls itself "the single authority on a join's
// output", and the collision, suffixing and key-merging algorithm is exactly what
// that sentence exists to stop being copied.
//
// # Why a separate node rather than another JoinKind
//
// Join has no slot for a strategy, a tolerance or an exact-match flag, and its
// predicates — Keyed, filtersLeft, leftCanBeNull — all read as statements about EQUI
// semantics. A kind that quietly meant "nearest" would make every one of them
// subtly wrong rather than obviously absent.
//
// # Both sides must be sorted on On, and it is CHECKED
//
// The nearest-key search is a binary search over a sorted run. ursus-api.md's example
// calls SetSorted, an unchecked assertion; step 14 already decided that question for
// GroupByDynamic and the answer does not change here. Pushdown's doc puts it best: a
// capability claim that is trusted but wrong returns wrong rows silently.
type AsOfJoin struct {
	Left, Right Node

	// LeftOn and RightOn are the ORDERING key — exactly one column per side, the one
	// the nearest-key search runs over.
	LeftOn, RightOn expr.Node

	// By are exact-match keys applied BEFORE the search, so a trade matches only
	// quotes for its own symbol. Empty means one global group.
	LeftBy, RightBy []expr.Node

	Strategy AsOfStrategy

	// Tolerance bounds how far the search may reach. Zero means unbounded.
	Tolerance dtype.Interval

	// AllowExactMatches lets a right key EQUAL to the left key match. Default true,
	// so the zero value of the option struct is the ordinary behaviour; the physical
	// planner carries the resolved value.
	AllowExactMatches bool

	Suffix string
}

func (a *AsOfJoin) planNode()        {}
func (a *AsOfJoin) Children() []Node { return []Node{a.Left, a.Right} }

func (a *AsOfJoin) WithChildren(kids []Node) Node {
	if len(kids) != 2 {
		panic("plan: AsOfJoin takes exactly two children")
	}
	c := *a
	c.Left, c.Right = kids[0], kids[1]
	return &c
}

// asJoin is the equi-join whose output shape an as-of join borrows.
//
// The ordering key and the by-keys are all key pairs for LAYOUT purposes — they are
// the columns that exist on both sides and should be merged rather than duplicated —
// even though only the by-keys are matched by equality. Coalesce is On because a
// matched row's two key values are equal by construction for the by-keys and are the
// left's by definition for the ordering key.
func (a *AsOfJoin) asJoin() *Join {
	left := append([]expr.Node{a.LeftOn}, a.LeftBy...)
	right := append([]expr.Node{a.RightOn}, a.RightBy...)
	return &Join{
		Left: a.Left, Right: a.Right,
		LeftOn: left, RightOn: right,
		Kind:     JoinLeft,
		Suffix:   a.Suffix,
		Coalesce: CoalesceOn,
	}
}

// Layout delegates, so there is exactly one implementation of a join's output shape.
func (a *AsOfJoin) Layout() (*JoinLayout, error) { return a.asJoin().Layout() }

func (a *AsOfJoin) Schema() (*dtype.Schema, error) {
	l, err := a.Layout()
	if err != nil {
		return nil, err
	}
	return l.Schema, nil
}

func (a *AsOfJoin) Label() string {
	var b strings.Builder
	b.WriteString("ASOF JOIN ")
	b.WriteString(a.Strategy.String())
	b.WriteString(" on ")
	b.WriteString(a.LeftOn.String())
	if len(a.LeftBy) > 0 {
		b.WriteString(" by [")
		b.WriteString(expr.StringAll(a.LeftBy))
		b.WriteString("]")
	}
	if !a.Tolerance.IsZero() {
		b.WriteString(" tolerance=")
		b.WriteString(a.Tolerance.String())
	}
	if !a.AllowExactMatches {
		b.WriteString(" strict")
	}
	return b.String()
}

func (a *AsOfJoin) childLabels() []string { return []string{"left", "right"} }

// resolveAsOfJoin expands and type-checks an as-of join.
func resolveAsOfJoin(a *AsOfJoin) (Node, error) {
	ls, err := a.Left.Schema()
	if err != nil {
		return nil, err
	}
	rs, err := a.Right.Schema()
	if err != nil {
		return nil, err
	}
	if err := a.Tolerance.Err(); err != nil {
		return nil, uerr.Annotate(err, "join_asof", "tolerance")
	}
	if a.Tolerance.Negative() {
		return nil, uerr.New(uerr.KindValue, "join_asof",
			"tolerance must not be negative, got %s", a.Tolerance).
			Hint("tolerance is a distance; the strategy decides the direction")
	}

	c := *a
	one := func(n expr.Node, s *dtype.Schema, side string) (expr.Node, error) {
		out, err := expr.ExpandAll([]expr.Node{n}, s)
		if err != nil {
			return nil, uerr.Annotate(err, "join_asof", "AsOfJoin")
		}
		if len(out) != 1 {
			return nil, uerr.New(uerr.KindSchema, "join_asof",
				"the %s as-of key must be exactly one column, got %d", side, len(out)).
				Hint("a selector that expands to several columns cannot be an as-of key")
		}
		return out[0], nil
	}
	if c.LeftOn, err = one(a.LeftOn, ls, "left"); err != nil {
		return nil, err
	}
	if c.RightOn, err = one(a.RightOn, rs, "right"); err != nil {
		return nil, err
	}
	if c.LeftBy, err = expr.ExpandAll(a.LeftBy, ls); err != nil {
		return nil, uerr.Annotate(err, "join_asof", "by")
	}
	if c.RightBy, err = expr.ExpandAll(a.RightBy, rs); err != nil {
		return nil, uerr.Annotate(err, "join_asof", "by")
	}
	if len(c.LeftBy) != len(c.RightBy) {
		return nil, uerr.New(uerr.KindValue, "join_asof",
			"by has %d left columns and %d right ones", len(c.LeftBy), len(c.RightBy))
	}

	// The ordering key must be ORDERED, not merely hashable: the search compares with
	// < rather than ==. Every hashable type ursus has is also ordered, but stating it
	// here is what makes the requirement visible when a non-ordered one arrives.
	lf, err := expr.Resolve(c.LeftOn, ls)
	if err != nil {
		return nil, uerr.Annotate(err, "join_asof", "AsOfJoin")
	}
	if !lf.Type.IsNumeric() && !lf.Type.IsTemporal() {
		return nil, uerr.New(uerr.KindType, "join_asof",
			"cannot run an as-of join on a %s key", lf.Type).
			Hint("the as-of key is compared with <, so it must be numeric or temporal")
	}
	// Tolerance is a calendar-aware Interval, which only means something against an
	// instant. Refusing beats reinterpreting nanoseconds as a count of whatever the
	// key happens to be.
	if !c.Tolerance.IsZero() && !lf.Type.IsTemporal() {
		return nil, uerr.New(uerr.KindUnsupported, "join_asof",
			"tolerance is not supported on a %s key", lf.Type).
			Hint("an Interval measures time; on a numeric key there is no unit to " +
				"measure in").
			Hint("drop the tolerance, or cast the key to a temporal type")
	}

	// Computed and thrown away, exactly as resolveJoin does: it is where the key
	// types are promoted and the output collisions are checked, and the message is
	// far more useful here than from inside the physical planner.
	if _, err := c.Layout(); err != nil {
		return nil, err
	}
	return &c, nil
}
