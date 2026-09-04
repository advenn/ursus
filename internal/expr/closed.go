package expr

// Closed says which endpoints of an interval belong to it.
//
// It lives here, at the level of the expression IR, for the same reason
// Interpolation does: the plan node, the physical operator and the root package all
// need it and none of them may import another's caller.
//
// # Why four values rather than a pair of bools
//
// Two bools would admit the same four states and read worse at every call site —
// `Closed: ClosedBoth` against `LeftClosed: true, RightClosed: true`.
//
// # And why the zero value is none of them
//
// The three callers want three different defaults, and every one of them is the
// obviously right answer for its own question:
//
//	IsBetween         both  — "between lo and hi" includes the endpoints
//	GroupByDynamic    left  — the only convention under which a grid TILES
//	Rolling           right — a row's window ends at its own timestamp, so closing
//	                          the left end would drop the row from its own window
//
// A zero value that meant one of them would silently give the wrong default to the
// other two. So the zero means "ask the operator", each caller resolves it with Or,
// and no call site has to know which default it inherited.
type Closed uint8

const (
	// ClosedDefault defers to whatever the operation's own convention is. It is the
	// zero value precisely so that leaving the field alone cannot mean the wrong
	// thing somewhere else.
	ClosedDefault Closed = iota

	// ClosedLeft is [start, end) — the only value under which a grid of windows
	// partitions its input exactly.
	ClosedLeft

	// ClosedRight is (start, end].
	ClosedRight

	// ClosedBoth is [start, end]. A row landing exactly on a shared boundary is in
	// BOTH neighbouring windows, so the windows no longer partition and a count over
	// them exceeds the row count. That is the documented meaning, not an accident.
	ClosedBoth

	// ClosedNone is (start, end). The mirror: a row on a shared boundary is in
	// neither window and is dropped from the output entirely.
	ClosedNone

	closedCount
)

var closedNames = [closedCount]string{
	ClosedDefault: "default", ClosedLeft: "left", ClosedRight: "right",
	ClosedBoth: "both", ClosedNone: "none",
}

func (c Closed) String() string {
	// The `!= ""` half is the load-bearing one — see Interpolation.String, which
	// records why an unnamed constant rendering as the empty string is worse than
	// rendering as "?".
	if int(c) < len(closedNames) && closedNames[c] != "" {
		return closedNames[c]
	}
	return "?"
}

// Or resolves ClosedDefault to the caller's own convention and leaves anything else
// alone. Every consumer calls this exactly once, at the edge, so the rest of the code
// never has to wonder whether it is holding a resolved value.
func (c Closed) Or(def Closed) Closed {
	if c == ClosedDefault {
		return def
	}
	return c
}

// LowerOpen and UpperClosed are the two questions every range test asks, phrased so
// the caller never re-derives the four cases. Both treat ClosedDefault as ClosedLeft,
// which is only reachable if a caller forgot to call Or.
//
//	contains(x)  ==  (LowerOpen() ? x > lo : x >= lo) && (UpperClosed() ? x <= hi : x < hi)
func (c Closed) LowerOpen() bool   { return c == ClosedRight || c == ClosedNone }
func (c Closed) UpperClosed() bool { return c == ClosedRight || c == ClosedBoth }
