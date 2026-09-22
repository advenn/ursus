package plan

import (
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// CheckUDFNames enforces the one thing a udf's name has to be: unique within the
// query. It is the enforcement of udf.go's own documented contract, which until now
// was stated and not checked.
//
// # Why a wrong name is a wrong ANSWER
//
// FOUR maps deduplicate expressions by their rendered form, over three code sites,
// because extractAggs is instantiated twice:
//
//	internal/plan/resolve_window.go   window temporaries, per extractWindows call
//	internal/physical/agg.go          extractAggs, per plan.Aggregate
//	internal/physical/tempgroup.go    the same extractAggs, per plan.TemporalGroup
//	internal/physical/window.go       partition-key sharing, per plan.Window
//
// A Go closure has no rendered form, so UDF.String renders the NAME instead. Two
// different closures sharing a name render identically, become one computation, and
// both results come from whichever was seen first — with no error. The last of the
// four is the worst: what it shares is a PARTITIONING, so every row of the second
// window is bucketed by another function's values.
//
// # Why it runs BEFORE Resolve's walk and not after
//
// One of those four merges happens INSIDE Resolve. extractWindows drops the losing
// window's whole subtree — it returns a Col reference and never appends to specs —
// so by the time TransformUp has finished, the second udf is no longer in the tree
// and a check there would pass on the exact query it exists to refuse.
//
// Running first means this sees the plan as the user wrote it, which is the only
// tree in which every udf the user built is still present. It does not double-count
// either: the surviving subtree is MOVED into the Window node, not copied.
//
// Running before expansion is fine, and is not an accident. Expansion replaces
// *Match with *Col inside an existing tree and mints no udf; its copies go through
// expr.Rebuild, which carries the id across. Nothing after plan construction mints
// one, so there is nothing later to catch.
//
// # Known gap
//
// expr.Node.Children deliberately excludes a nested scope body, and nothing
// implements one yet. A udf inside a future lambda — List().Eval() would be the
// first — is invisible to expr.Walk, hence to this check, and equally to
// expr.HasUDF, which guards predicate pushdown today.
func CheckUDFNames(n Node) error {
	type sighting struct {
		udf       *expr.UDF
		enclosing expr.Node
	}
	var seen map[string]sighting // allocated only if a udf is found
	var bad error

	Walk(n, func(x Node) bool {
		if bad != nil {
			return false
		}
		for _, e := range Expressions(x) {
			enclosing := e
			expr.Walk(e, func(y expr.Node) bool {
				if bad != nil {
					return false
				}
				u, ok := y.(*expr.UDF)
				if !ok {
					return true
				}
				if u.ID() == 0 {
					// Only reachable from a UDF built by a bare composite literal
					// instead of expr.NewUDF, which is an ursus bug. Loud, because
					// the alternative is this check quietly treating two
					// unidentified udfs as the same one.
					bad = uerr.Internalf(
						"plan: udf %q carries no identity; build it with expr.NewUDF",
						u.Name)
					return false
				}
				prev, dup := seen[u.Name]
				if !dup {
					if seen == nil {
						seen = make(map[string]sighting, 4)
					}
					seen[u.Name] = sighting{udf: u, enclosing: enclosing}
					return true
				}
				if prev.udf.ID() == u.ID() {
					// The same node, copied: multi-column expansion, expr.Rebuild,
					// extractAggs, or one Expr variable used twice. Every one of
					// those is a single udf and merging them is correct.
					return true
				}
				bad = uerr.New(uerr.KindValue, u.Kind,
					"two different udfs are both named %q", u.Name).
					Hint("first: %s", prev.enclosing.String()).
					Hint("second: %s", enclosing.String()).
					Hint("the planner deduplicates expressions by their rendered " +
						"form, and a Go closure has no rendered form — so two udfs " +
						"sharing a name would become ONE computation and both " +
						"results would come from whichever was seen first").
					Hint("give them different names; or, if they are meant to BE " +
						"the same function, build the Expr once and reuse that " +
						"variable rather than calling the helper twice")
				return false
			})
			if bad != nil {
				return false
			}
		}
		return true
	})
	return bad
}
