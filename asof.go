package ursus

import (
	"ursus/internal/expr"
	"ursus/internal/plan"
	"ursus/internal/uerr"
)

// AsOfStrategy picks which neighbouring right row a left row matches.
type AsOfStrategy = plan.AsOfStrategy

const (
	// AsOfBackward is the default: the last right key at or before the left key.
	// "Attach the most recent quote to each trade."
	AsOfBackward = plan.AsOfBackward
	// AsOfForward is the first right key at or after the left key.
	AsOfForward = plan.AsOfForward
	// AsOfNearest is whichever is closer, ties going backward.
	AsOfNearest = plan.AsOfNearest
)

// AsOfOption configures JoinAsOf.
type AsOfOption func(*asOfCfg)

type asOfCfg struct {
	on              Expr
	leftOn, rightOn Expr
	haveOn          bool
	haveLeft        bool
	haveRight       bool

	by              []Expr
	leftBy, rightBy []Expr
	haveBy          bool
	haveLeftBy      bool
	haveRightBy     bool

	strategy   AsOfStrategy
	tolerance  Interval
	allowExact bool
	suffix     string
}

// AsOfOn names the ordering key, present under the same name on both sides.
func AsOfOn(e Expr) AsOfOption {
	return func(c *asOfCfg) { c.on, c.haveOn = e, true }
}

// AsOfLeftOn and AsOfRightOn name differently-named ordering keys. Used together.
func AsOfLeftOn(e Expr) AsOfOption {
	return func(c *asOfCfg) { c.leftOn, c.haveLeft = e, true }
}
func AsOfRightOn(e Expr) AsOfOption {
	return func(c *asOfCfg) { c.rightOn, c.haveRight = e, true }
}

// AsOfBy adds EXACT-match keys applied before the nearest-key search, so a trade
// matches only quotes for its own symbol.
//
// Without it every left row searches one global run, which on any real fixture means
// matching the nearest row of the wrong instrument — a plausible number and the wrong
// one.
func AsOfBy(exprs ...Expr) AsOfOption {
	return func(c *asOfCfg) { c.by, c.haveBy = exprs, true }
}

// AsOfLeftBy and AsOfRightBy are AsOfBy for differently-named columns.
func AsOfLeftBy(exprs ...Expr) AsOfOption {
	return func(c *asOfCfg) { c.leftBy, c.haveLeftBy = exprs, true }
}
func AsOfRightBy(exprs ...Expr) AsOfOption {
	return func(c *asOfCfg) { c.rightBy, c.haveRightBy = exprs, true }
}

// AsOfStrategyOpt picks backward (the default), forward or nearest.
func AsOfStrategyOpt(s AsOfStrategy) AsOfOption {
	return func(c *asOfCfg) { c.strategy = s }
}

// AsOfTolerance bounds how far the search may reach. A candidate further than this
// from the left key does not match, and the left row comes back null-padded.
//
// The bound is recomputed per row for a CALENDAR interval, so Every("1mo") is a
// different distance in February than in March — which is the whole reason Interval
// is not a duration.
func AsOfTolerance(i Interval) AsOfOption {
	return func(c *asOfCfg) { c.tolerance = i }
}

// AsOfAllowExactMatches decides whether a right key EQUAL to the left key may match.
// Default true.
func AsOfAllowExactMatches(b bool) AsOfOption {
	return func(c *asOfCfg) { c.allowExact = b }
}

// AsOfSuffix renames a colliding right column. Defaults to Join's suffix.
func AsOfSuffix(s string) AsOfOption {
	return func(c *asOfCfg) { c.suffix = s }
}

// JoinAsOf matches each left row to the NEAREST right row rather than an equal one.
//
//	trades.JoinAsOf(quotes,
//	    ursus.AsOfOn(ursus.Col("ts")),
//	    ursus.AsOfBy(ursus.Col("symbol")),
//	    ursus.AsOfTolerance(ursus.Every("1m")),
//	)
//
// # It is a LEFT join
//
// Every left row survives; the right columns are null when nothing is near enough.
// So the output height always equals the left height, tolerance and strategy only
// decide which rows come back populated.
//
// # Both sides must be sorted on the as-of key, and it is CHECKED
//
// The search is a binary search over a sorted run. ursus verifies rather than
// trusting an assertion — an unchecked sortedness hint is a user claim that deletes a
// correctness check, and its failure mode is wrong matches with no error. Sort first
// if the check refuses.
func (lf *LazyFrame) JoinAsOf(other *LazyFrame, opts ...AsOfOption) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	if other == nil {
		return &LazyFrame{err: uerr.New(uerr.KindValue, "join_asof",
			"the frame to join against is nil")}
	}
	if other.err != nil {
		return &LazyFrame{err: other.err}
	}

	cfg := asOfCfg{allowExact: true}
	for _, o := range opts {
		o(&cfg)
	}
	if err := cfg.validate(); err != nil {
		return &LazyFrame{err: err}
	}

	leftOn, rightOn := cfg.leftOn, cfg.rightOn
	if cfg.haveOn {
		leftOn, rightOn = cfg.on, cfg.on
	}
	leftBy, rightBy := cfg.leftBy, cfg.rightBy
	if cfg.haveBy {
		// Copy into two independent slices rather than aliasing one, for the reason
		// JoinOn already records: it removes a standing "did we alias?" question from
		// a package whose central invariant is that plans are immutable.
		leftBy = append([]Expr(nil), cfg.by...)
		rightBy = append([]Expr(nil), cfg.by...)
	}

	return lf.derive(&plan.AsOfJoin{
		Left: lf.node, Right: other.node,
		LeftOn: leftOn.n, RightOn: rightOn.n,
		LeftBy: nodesOf(leftBy), RightBy: nodesOf(rightBy),
		Strategy:          cfg.strategy,
		Tolerance:         cfg.tolerance,
		AllowExactMatches: cfg.allowExact,
		Suffix:            cfg.suffix,
	})
}

func nodesOf(es []Expr) []expr.Node {
	if len(es) == 0 {
		return nil
	}
	out := make([]expr.Node, len(es))
	for i, e := range es {
		out[i] = e.n
	}
	return out
}

// validate rejects the option combinations that cannot mean anything, up front and
// with a message naming what was wrong — the shape joinCfg.validateOpts already uses.
func (c *asOfCfg) validate() error {
	switch {
	case c.haveOn && (c.haveLeft || c.haveRight):
		return asOfErr("AsOfOn cannot be combined with AsOfLeftOn or AsOfRightOn")
	case c.haveLeft != c.haveRight:
		return asOfErr("AsOfLeftOn and AsOfRightOn are used together")
	case !c.haveOn && !c.haveLeft:
		return asOfErr("an as-of join needs a key: pass AsOfOn or AsOfLeftOn/AsOfRightOn")
	case c.haveBy && (c.haveLeftBy || c.haveRightBy):
		return asOfErr("AsOfBy cannot be combined with AsOfLeftBy or AsOfRightBy")
	case c.haveLeftBy != c.haveRightBy:
		return asOfErr("AsOfLeftBy and AsOfRightBy are used together")
	case c.strategy > AsOfNearest:
		return asOfErr("unknown as-of strategy %d", uint8(c.strategy))
	}
	return nil
}

func asOfErr(format string, args ...any) error {
	return uerr.New(uerr.KindValue, "join_asof", format, args...)
}

// MergeSorted interleaves this frame with another that is sorted on the same key,
// keeping the result sorted.
//
// Not a concat: a concat appends, so the result is ordered only if the second frame
// begins after the first ends. Not a join either — nothing is matched and no row is
// dropped, so the output height is always the sum of the two.
//
// Both schemas must match EXACTLY and both inputs must be sorted on key, which is
// checked rather than assumed.
func (lf *LazyFrame) MergeSorted(other *LazyFrame, key string) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	if other == nil {
		return &LazyFrame{err: uerr.New(uerr.KindValue, "merge_sorted",
			"the frame to merge with is nil")}
	}
	if other.err != nil {
		return &LazyFrame{err: other.err}
	}
	if key == "" {
		return &LazyFrame{err: uerr.New(uerr.KindValue, "merge_sorted",
			"merge_sorted needs the name of the sorted column")}
	}
	return lf.derive(&plan.MergeSorted{Left: lf.node, Right: other.node, Key: key})
}
