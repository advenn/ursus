package plan

import (
	"ursus/internal/uerr"
)

// Rule is one optimizer rewrite.
//
// A rule takes a resolved plan and returns a rewritten one plus whether it
// changed anything. It must be SOUND: the rewritten plan must produce the same
// rows, in the same order, with the same schema. The optimizer verifies the
// schema half of that automatically — see Optimizer.Run.
// Apply receives the Flags so a rule can gate an individual ARM, not just its own
// existence. Predicate pushdown needs that: moving a filter through a join is far
// riskier than moving one through a sort, and "turn off all predicate pushdown" is
// too blunt a tool to bisect with when the two live in one recursive sweep.
type Rule interface {
	Name() string
	Apply(Node, Flags) (Node, bool, error)
}

// RunMode says how often the driver applies a rule.
type RunMode uint8

const (
	// Once: the rule reaches its own fixed point internally, so a second pass
	// would be wasted work. Pushdowns are like this — one top-down sweep places
	// every projection.
	Once RunMode = iota
	// UntilStable: the rule makes local improvements that may enable each other,
	// so it is re-applied until nothing changes. Constant folding is like this.
	UntilStable
)

// Flags switches individual rules off.
//
// This exists for two reasons that both matter: benchmarking the value of a rule,
// and bisecting a wrong answer down to the rule that caused it. An optimizer you
// cannot turn off one piece at a time is an optimizer you cannot debug.
type Flags struct {
	ProjectionPushdown bool
	PredicatePushdown  bool
	LimitPushdown      bool
	SimplifyExprs      bool

	// JoinPredicatePushdown gates the Join ARM of predicate pushdown, separately
	// from the rest of the rule. It is the newest and by far the most dangerous
	// piece of the optimizer — the legality table has four barrier cells whose
	// violation returns different rows with no error — so it gets its own switch.
	JoinPredicatePushdown bool
}

// DefaultFlags enables every rule that exists.
func DefaultFlags() Flags {
	return Flags{
		ProjectionPushdown: true,
		PredicatePushdown:  true,
		LimitPushdown:      true,
		SimplifyExprs:      true,

		JoinPredicatePushdown: true,
	}
}

// NoFlags disables everything, for debugging.
func NoFlags() Flags { return Flags{} }

type registered struct {
	rule    Rule
	mode    RunMode
	enabled func(Flags) bool
}

// Optimizer applies rules to a resolved plan.
type Optimizer struct {
	rules []registered
	// folder is shared by pointer with the simplify rule, so SetConstEvaluator can
	// reach an evaluator that is otherwise sealed inside a Local adapter. A holder
	// rather than a field on the rule because registered.rule is an interface and
	// rules are values, not pointers.
	folder *constFolder
	// Verify runs the schema-preservation check after every rule. On in tests,
	// off in production, because it costs a full schema resolution per rule.
	Verify bool
}

// constFolder is the mutable cell SetConstEvaluator writes and simplify reads.
type constFolder struct{ e ConstEvaluator }

// NewOptimizer returns the standard rule set.
func NewOptimizer() *Optimizer {
	folder := &constFolder{}
	return &Optimizer{
		folder: folder,
		rules: []registered{
			// Predicate pushdown runs FIRST. Moving filters down before deciding
			// which columns are needed means projection pushdown sees the final
			// shape of the plan; the reverse order would compute a projection and
			// then invalidate it.
			{
				rule:    predicatePushdown{},
				mode:    Once,
				enabled: func(f Flags) bool { return f.PredicatePushdown },
			},
			{
				rule:    projectionPushdown{},
				mode:    Once,
				enabled: func(f Flags) bool { return f.ProjectionPushdown },
			},
			// Limit pushdown runs before simplification, not last. It only fires on a
			// Limit sitting directly above a Scan, and predicate pushdown is what
			// decides whether a Filter ends up between them — so running this first
			// would let a limit reach a scan that a later rule turns into an inexact
			// one.
			{
				rule:    limitPushdown{},
				mode:    Once,
				enabled: func(f Flags) bool { return f.LimitPushdown },
			},
			// Simplification runs LAST, because the pushdowns make work for it that
			// does not exist beforehand: an identity Project only becomes one once
			// the scan below it has been narrowed, and predicate pushdown rebuilds
			// predicates in a child's namespace as it moves them.
			//
			// The cost, recorded rather than fixed: a Filter that folds away to
			// nothing no longer prunes the work the pushdowns did above it. That is a
			// missed optimization, not a wrong answer, and closing it means
			// registering this rule a second time at the front rather than any new
			// machinery.
			//
			// # Once, and this was measured rather than assumed
			//
			// design/logical.md §8.1 offers constant folding as the example of a rule
			// that needs UntilStable, and it was registered that way first. It does
			// not need it: BOTH walks are bottom-up — TransformUp over plan nodes,
			// simplify.walk over expressions — so a child is always rewritten before
			// the parent that could exploit it, and every rewrite here reaches its
			// fixed point in a single sweep. Registering it UntilStable ran a second
			// pass that changed nothing on every query in the suite.
			//
			// Which is exactly what Once's own doc describes, so it gets Once. The
			// UntilStable machinery is now exercised directly instead — see
			// TestOscillatingRuleIsCaught, which reaches the maxIterations error path
			// that had never executed since step 2.
			{
				rule:    Local(simplify{fold: folder}),
				mode:    Once,
				enabled: func(f Flags) bool { return f.SimplifyExprs },
			},
		},
	}
}

// SetConstEvaluator attaches a constant folder to the simplification rule.
//
// Without one the rule still performs every structural rewrite it knows; only
// Lit⊕Lit folding is skipped. See ConstEvaluator for why this is injected rather
// than imported, and TestPlanPackageNeedsNoArrow for the invariant it protects.
func (o *Optimizer) SetConstEvaluator(e ConstEvaluator) { o.folder.e = e }

// maxIterations bounds UntilStable rules. A rule that oscillates between two
// forms would otherwise hang the query; failing loudly is better than hanging,
// and the limit is far above any legitimate fixed point.
const maxIterations = 16

// Run applies every enabled rule to n.
//
// After each rule it optionally re-derives the root schema and compares it to the
// original. A rewrite that changes the output schema is not an optimization, it is
// a bug, and this check catches an entire class of them for the cost of one schema
// resolution per rule.
func (o *Optimizer) Run(n Node, flags Flags) (Node, error) {
	var want string
	if o.Verify {
		s, err := n.Schema()
		if err != nil {
			return nil, err
		}
		want = s.String()
	}

	for _, r := range o.rules {
		if !r.enabled(flags) {
			continue
		}

		iterations := 1
		if r.mode == UntilStable {
			iterations = maxIterations
		}

		for i := range iterations {
			next, changed, err := r.rule.Apply(n, flags)
			if err != nil {
				return nil, uerr.Wrap(err, uerr.KindInternal, "optimize",
					"rule %q failed", r.rule.Name())
			}
			n = next
			if !changed {
				break
			}
			if r.mode == UntilStable && i == maxIterations-1 {
				return nil, uerr.Internalf(
					"optimizer rule %q did not reach a fixed point in %d iterations",
					r.rule.Name(), maxIterations)
			}
		}

		if o.Verify {
			s, err := n.Schema()
			if err != nil {
				return nil, uerr.Wrap(err, uerr.KindInternal, "optimize",
					"rule %q produced a plan whose schema does not resolve", r.rule.Name())
			}
			if got := s.String(); got != want {
				return nil, uerr.Internalf(
					"rule %q changed the output schema\n  before: %s\n  after:  %s",
					r.rule.Name(), want, got)
			}
		}
	}
	return n, nil
}
