package ursus

import (
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/uerr"
)

// JoinKind is the shape of a join: which rows survive.
type JoinKind = plan.JoinKind

const (
	// JoinInner keeps only matching pairs. The default.
	JoinInner = plan.JoinInner
	// JoinLeft keeps every left row, with right columns null where unmatched.
	JoinLeft = plan.JoinLeft
	// JoinRight is the mirror of JoinLeft.
	JoinRight = plan.JoinRight
	// JoinFull keeps every row from both sides.
	JoinFull = plan.JoinFull
	// JoinSemi keeps left rows that have at least one match. A filter: no right
	// column is added.
	JoinSemi = plan.JoinSemi
	// JoinAnti keeps left rows with no match. The inverse of JoinSemi.
	JoinAnti = plan.JoinAnti
	// JoinCross is the cartesian product. No keys.
	JoinCross = plan.JoinCross
)

// JoinValidation asserts a cardinality and errors when the data violates it.
//
// The first term names the LEFT frame: ManyToOne on orders.Join(customers)
// asserts that the customers are unique.
type JoinValidation = plan.JoinValidation

const (
	ValidateNone = plan.ValidateNone
	// ValidateOneToOne requires unique keys on both sides.
	ValidateOneToOne = plan.ValidateOneToOne
	// ValidateOneToMany requires unique keys on the LEFT.
	ValidateOneToMany = plan.ValidateOneToMany
	// ValidateManyToOne requires unique keys on the RIGHT. This is the one that
	// catches accidental fan-out, which is the most common analytics bug there is.
	ValidateManyToOne = plan.ValidateManyToOne
	// ValidateManyToMany imposes no constraint.
	ValidateManyToMany = plan.ValidateManyToMany
)

// JoinOption configures a join.
type JoinOption func(*joinCfg)

type joinCfg struct {
	on, leftOn, rightOn      []Expr
	onSet, leftSet, rightSet bool
	kind                     JoinKind
	suffix                   string
	suffixSet                bool
	coalesce                 plan.CoalesceMode
	nullsEqual               bool
	validate                 JoinValidation
}

// JoinOn names key columns present under the same name on both sides.
func JoinOn(keys ...Expr) JoinOption {
	return func(c *joinCfg) { c.on, c.onSet = keys, true }
}

// JoinLeftOn and JoinRightOn name the key columns separately, for frames where
// they are called different things. They are used together and must agree in
// count; keys are paired positionally.
func JoinLeftOn(keys ...Expr) JoinOption {
	return func(c *joinCfg) { c.leftOn, c.leftSet = keys, true }
}

func JoinRightOn(keys ...Expr) JoinOption {
	return func(c *joinCfg) { c.rightOn, c.rightSet = keys, true }
}

// JoinHow sets the join kind. Default JoinInner.
func JoinHow(k JoinKind) JoinOption { return func(c *joinCfg) { c.kind = k } }

// JoinSuffix sets what is appended to a right-side column whose name collides
// with a left-side one. Default "_right".
func JoinSuffix(s string) JoinOption {
	return func(c *joinCfg) { c.suffix, c.suffixSet = s, true }
}

// JoinCoalesce forces key merging on or off.
//
// Unset is not the same as either: by default the key appears once for inner,
// left, right, semi and anti joins, and twice for a full join — which can leave
// the key null on either side, so there is no single side to take it from.
//
// The default only merges keys that are named the SAME on both sides. Merging
// JoinLeftOn(Col("cust_id")) with JoinRightOn(Col("id")) would silently delete a
// column the caller named explicitly, so it takes an explicit JoinCoalesce(true).
func JoinCoalesce(b bool) JoinOption {
	return func(c *joinCfg) {
		c.coalesce = plan.CoalesceOff
		if b {
			c.coalesce = plan.CoalesceOn
		}
	}
}

// JoinNullsEqual makes null keys match each other. Default false, which is SQL's
// rule and Polars': a null key matches nothing, including another null.
//
// Note this is deliberately the OPPOSITE of GroupBy, where null forms its own
// group. Grouping asks "are these the same value?" and joining asks "is this the
// same entity?", and a missing identifier is not evidence of sameness.
func JoinNullsEqual(b bool) JoinOption { return func(c *joinCfg) { c.nullsEqual = b } }

// JoinValidate asserts a key cardinality, failing the query when the data
// violates it.
//
//	orders.Join(customers, JoinOn(Col("customer_id")),
//	    JoinValidate(ursus.ValidateManyToOne))
//
// Worth reaching for: an unexpected duplicate on the right multiplies every
// matching left row, and the result looks entirely plausible.
func JoinValidate(v JoinValidation) JoinOption { return func(c *joinCfg) { c.validate = v } }

// Join combines this frame with another.
//
//	enriched := orders.Join(customers,
//	    ursus.JoinOn(ursus.Col("customer_id")),
//	    ursus.JoinHow(ursus.JoinLeft),
//	)
//
// Column names that appear on both sides get the right one suffixed; the join key
// appears once unless the kind is a full join. See JoinCoalesce and JoinSuffix.
func (lf *LazyFrame) Join(other *LazyFrame, opts ...JoinOption) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	if other == nil {
		return &LazyFrame{err: uerr.New(uerr.KindValue, "join",
			"the frame to join with is nil")}
	}
	if other.err != nil {
		return &LazyFrame{err: other.err}
	}

	cfg := joinCfg{}
	for _, o := range opts {
		o(&cfg)
	}
	if err := cfg.validateOpts(); err != nil {
		return &LazyFrame{err: err}
	}

	leftKeys, rightKeys := cfg.leftOn, cfg.rightOn
	if cfg.onSet {
		// Copy into two independent slices rather than aliasing one. It costs one
		// allocation at build time and removes a standing "did we alias?" question
		// from a package whose central invariant is that plans are immutable.
		leftKeys = append([]Expr(nil), cfg.on...)
		rightKeys = append([]Expr(nil), cfg.on...)
	}

	left, err := nodes(leftKeys)
	if err != nil {
		return &LazyFrame{err: uerr.Annotate(err, "join", "Join (left keys)")}
	}
	right, err := nodes(rightKeys)
	if err != nil {
		return &LazyFrame{err: uerr.Annotate(err, "join", "Join (right keys)")}
	}

	suffix := cfg.suffix
	if !cfg.suffixSet {
		suffix = ""
	}
	return lf.derive(&plan.Join{
		Left:       lf.node,
		Right:      other.node,
		LeftOn:     left,
		RightOn:    right,
		Kind:       cfg.kind,
		Suffix:     suffix,
		Coalesce:   cfg.coalesce,
		NullsEqual: cfg.nullsEqual,
		Validate:   cfg.validate,
	})
}

// JoinWhere joins every pair of rows that satisfies the predicates.
//
//	sessions.JoinWhere(events,
//	    ursus.Col("ts").Ge(ursus.Col("start")),
//	    ursus.Col("ts").Lt(ursus.Col("end")),
//	)
//
// This is the non-equi join: the predicates may be any boolean expression over both
// sides, and are AND-ed. Several predicates are the usual case, because an interval
// match takes two.
//
// # Which names the predicates use
//
// The join's OUTPUT names, which is to say left columns by their own name and a
// colliding right column with the suffix — `k` and `k_right` by default. There is no
// separate left/right namespace to qualify with, and the same rule applies here as
// to a Filter written after any other join. JoinSuffix changes the suffix.
//
// # What it costs
//
// A pair of rows for every pair that satisfies the predicates, which can be very
// large — that is the nature of the operation and not a defect. When at least one
// predicate is an EQUALITY between the two sides, the optimizer turns it into a hash
// join key and evaluates the rest per matching pair; without one, every pair is
// tested. Explain shows which happened.
//
// At least one predicate is required. A JoinWhere with none is a cartesian product
// written as though it were a filtered one, and the cardinality difference is |L| vs
// |L|x|R| — so it is an error rather than a silent cross join, for the same reason a
// keyless equi-join is.
func (lf *LazyFrame) JoinWhere(other *LazyFrame, preds ...Expr) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	if len(preds) == 0 {
		return &LazyFrame{err: uerr.New(uerr.KindValue, "join_where",
			"JoinWhere requires at least one predicate").
			Hint("use Join(other, JoinHow(ursus.JoinCross)) for an unfiltered cartesian product")}
	}
	// Definitionally a filtered cross product, and built as exactly that: the
	// optimizer's collapse_cross_join rule is what turns the equality predicates
	// into hash keys. Building the cross join and the filter separately is what
	// lets every other rule — predicate pushdown into the inputs, projection
	// pushdown, simplification — apply without knowing this method exists.
	return lf.Join(other, JoinHow(JoinCross)).Filter(preds...)
}

// WhereExists keeps the rows of this frame that have at least one row in other
// satisfying every predicate.
//
//	orders.WhereExists(lineitem,
//	    ursus.Col("o_orderkey").Eq(ursus.Col("l_orderkey")),
//	    ursus.Col("l_quantity").Gt(40.0),
//	)
//
// It is SQL's `EXISTS (SELECT ... WHERE ...)`, and the output is this frame: same
// columns, same order, fewer rows. No column of other appears in the result, which
// is why this is not a join you can write as one — see below.
//
// # Why it is not JoinWhere plus a filter
//
// JoinWhere pairs rows, so filtering its output filters pairs. This asks whether a
// satisfying partner EXISTS, which is a question about the left row, and a left row
// with three partners must come back once rather than three times. The predicate is
// therefore evaluated before the verdict, inside the join, and no combination of
// JoinWhere and Filter expresses that without a deduplication.
//
// # Which names the predicates use
//
// The names a JOIN of the two frames would produce: this frame's columns by their
// own name, and a colliding column of other with the suffix — `k` and `k_right`.
// The output has no such column, but the predicate is evaluated on pairs, so it can
// name both.
//
// # What it costs
//
// An equality between the two sides becomes a hash join key and the rest is tested
// per matching pair; with no equality among the predicates, every pair is tested.
// Explain shows which. A partner is found by scanning the candidates, so this stops
// at the first satisfying one — WhereNotExists cannot.
func (lf *LazyFrame) WhereExists(other *LazyFrame, preds ...Expr) *LazyFrame {
	return lf.whereExists(other, plan.JoinSemi, preds, "where_exists", "WhereExists")
}

// WhereNotExists keeps the rows of this frame that have NO row in other satisfying
// every predicate.
//
//	// customers who never ordered anything large
//	customers.WhereNotExists(orders,
//	    ursus.Col("c_custkey").Eq(ursus.Col("o_custkey")),
//	    ursus.Col("o_totalprice").Gt(1000.0),
//	)
//
// SQL's `NOT EXISTS`, and the exact complement of WhereExists: for any frame and
// any predicates, the two partition this frame's rows.
//
// It is strictly more work than WhereExists on the same inputs. Finding one
// satisfying partner ends the search; proving there is none means examining every
// candidate.
func (lf *LazyFrame) WhereNotExists(other *LazyFrame, preds ...Expr) *LazyFrame {
	return lf.whereExists(other, plan.JoinAnti, preds, "where_not_exists", "WhereNotExists")
}

// whereExists builds the keyless semi/anti join both spellings reduce to.
//
// The predicates go in whole. Splitting off the equalities is collapse_cross_join's
// job, because deciding which conjunct straddles the two sides needs the join's
// layout, which the builder has no schema to compute. Left alone the join is still
// correct — one key group, every pair tested — so the rule is an optimisation
// rather than a requirement, which is what makes the with-rule/without-rule
// differential a real test.
func (lf *LazyFrame) whereExists(other *LazyFrame, kind plan.JoinKind, preds []Expr, op, name string) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	if other == nil {
		return &LazyFrame{err: uerr.New(uerr.KindValue, op,
			"the frame to test against is nil")}
	}
	if other.err != nil {
		return &LazyFrame{err: other.err}
	}
	if len(preds) == 0 {
		return &LazyFrame{err: uerr.New(uerr.KindValue, op,
			"%s requires at least one predicate", name).
			Hint("with no predicate every row of the other frame is a partner, so " +
				"the answer is either every row or none")}
	}
	ns, err := nodes(preds)
	if err != nil {
		return &LazyFrame{err: uerr.Annotate(err, op, name)}
	}
	return lf.derive(&plan.Join{
		Left:     lf.node,
		Right:    other.node,
		Kind:     kind,
		Residual: ns,
	})
}

// validateOpts checks the whole configuration once, so one call produces one good
// error rather than each option carrying an error slot.
func (c *joinCfg) validateOpts() error {
	switch {
	case c.onSet && (c.leftSet || c.rightSet):
		return uerr.New(uerr.KindValue, "join",
			"JoinOn sets both sides, so it cannot be combined with JoinLeftOn or JoinRightOn").
			Hint("use JoinOn when the key has the same name on both sides").
			Hint("use JoinLeftOn and JoinRightOn together when it does not")

	case c.leftSet != c.rightSet:
		missing, given := "JoinRightOn", "JoinLeftOn"
		if c.rightSet {
			missing, given = "JoinLeftOn", "JoinRightOn"
		}
		return uerr.New(uerr.KindValue, "join",
			"%s was given without %s", given, missing).
			Hint("keys are paired positionally, so both sides must be named")

	case c.onSet && len(c.on) == 0,
		c.leftSet && len(c.leftOn) == 0,
		c.rightSet && len(c.rightOn) == 0:
		return uerr.New(uerr.KindValue, "join", "a join key list must not be empty")

	case c.kind == JoinCross && (c.onSet || c.leftSet || c.rightSet):
		return uerr.New(uerr.KindValue, "join",
			"a cross join takes no keys").
			Hint("did you mean JoinHow(ursus.JoinInner)?")

	case c.kind != JoinCross && !c.onSet && !c.leftSet && !c.rightSet:
		return uerr.New(uerr.KindValue, "join",
			"a %s join requires keys", c.kind).
			Hint("use JoinOn(...), or JoinHow(ursus.JoinCross) for a cartesian product")

	case c.suffixSet && c.suffix == "":
		// "" on the node means "use the default", so an empty suffix could not be
		// distinguished from an unset one — and it would not disambiguate anything
		// anyway.
		return uerr.New(uerr.KindValue, "join", "the join suffix must not be empty")
	}
	return nil
}
