package plan

import (
	"ursus/dtype"
	"ursus/internal/expr"
)

// LocalRule is the easy case: a pure node-to-node replacement, applied bottom-up
// with children already rewritten.
//
// design/logical.md §8.1 specified this alongside Rule and RunMode; the two
// pushdowns shipped without it because each needs to carry state DOWN the tree
// (the required-column set, the predicates being moved) and so had to own its own
// traversal. A rule that only ever looks at one node and its already-rewritten
// children needs none of that, and writing the sweep again per rule is how a walk
// acquires four subtly different copies — see Expressions' own doc for the case
// where that already happened.
//
// # It is thin because TransformUp exists, and that is not what earns it a file
//
// The traversal below is four lines. What earns this adapter its place is the
// GUARD it wraps every Match in: a local rewrite is accepted only when the
// replacement resolves to the same schema as what it replaced. Optimizer.Run
// verifies that at the ROOT of the plan, which is exactly where a rewrite buried
// under a Filter or a Join is invisible — the root schema of
// `Filter(bad) -> Scan` is the scan's schema no matter what `bad` became.
type LocalRule interface {
	Name() string

	// Match returns a replacement for n, or (n, false, nil) to leave it alone.
	// Children are already rewritten when it is called.
	//
	// It may return a node with a DIFFERENT schema; Local declines the rewrite in
	// that case rather than trusting the rule. That is deliberate: the alternative
	// is every rule re-deriving the check, and the one that forgets returns wrong
	// columns with no error anywhere.
	Match(n Node, f Flags) (Node, bool, error)
}

// Local adapts a LocalRule to Rule.
func Local(r LocalRule) Rule { return local{r} }

type local struct{ r LocalRule }

func (l local) Name() string { return l.r.Name() }

func (l local) Apply(n Node, f Flags) (Node, bool, error) {
	changed := false
	out, err := TransformUp(n, func(x Node) (Node, error) {
		got, ok, err := l.r.Match(x, f)
		if err != nil || !ok {
			return x, err
		}
		same, err := sameSchema(x, got)
		if err != nil || !same {
			// A schema-changing "optimization" is a bug, but it is the rule's bug
			// and not the query's: declining it leaves a correct plan behind, and
			// the tests are where the rule is supposed to be held to account.
			return x, err
		}
		changed = true
		return got, nil
	})
	if err != nil {
		return nil, false, err
	}
	return out, changed, nil
}

// sameSchema reports whether two plan nodes resolve to the same output schema.
//
// Schema.String carries names, types AND nullability, which is the whole point:
// the trap this catches is a rewrite that keeps every column and every type and
// quietly turns a nullable one non-null. See fieldEqual for the expression-level
// mirror of this and the concrete case that motivated it.
func sameSchema(a, b Node) (bool, error) {
	sa, err := a.Schema()
	if err != nil {
		return false, err
	}
	sb, err := b.Schema()
	if err != nil {
		// The replacement does not even resolve. That is a rule bug rather than a
		// query error — the input plan resolved — so decline rather than fail the
		// query the user actually asked for.
		return false, nil
	}
	return sa.String() == sb.String(), nil
}

// fieldEqual reports whether two expressions resolve to the same field against in.
//
// # This is the guard, and it is load-bearing three times over
//
// Name, Type and Nullable all have to match, and each of the three has a concrete
// rewrite behind it that looks obviously sound and is not:
//
//	Nullable   `x AND lit(false)` -> `lit(false)`. Value-sound under Kleene logic —
//	           null AND false really is false — but Binary.Field computes
//	           `nullable := lf.Nullable || rf.Nullable` with no absorbing-element
//	           case, so the rewrite turns a nullable Boolean into a non-null one.
//	           Optimizer.Verify then fails the query. Note what this guard does
//	           rather than a blanket refusal: when x is NOT nullable both sides are
//	           non-null, the fold is sound, and it is allowed.
//
//	Name       `Not(Not(Col("a").Alias("b")))` -> `Col("a").Alias("b")`. OutputName
//	           consults naming nodes only at the ROOT, so the doubled negation is
//	           named "a" (leftmost column wins) and the bare child is named "b".
//	           The rewrite silently renames an output column.
//
//	Type       any fold whose literal round-trip does not land back on the same
//	           dtype — a Decimal's scale, a Datetime's unit.
//
// A false return means "leave it alone", never an error: declining an optimization
// is always safe, and a rule that cannot prove its rewrite has no business making
// it.
func fieldEqual(a, b expr.Node, in *dtype.Schema) bool {
	fa, err := a.Field(in)
	if err != nil {
		return false
	}
	fb, err := b.Field(in)
	if err != nil {
		return false
	}
	return fa == fb
}

// sameValue is fieldEqual without the name — the guard for an INTERIOR rewrite.
//
// An interior node's name is not its own output name; it feeds OutputName's
// leftmost-column-wins search, and whether that search's answer changed is a
// question about the root. So the two are checked at the two different levels
// they actually matter at: value-shape at every node, name once at the top.
//
// Checking names here instead would decline `x AND true` -> `x` inside a Filter
// predicate, where the name is read by nothing at all.
func sameValue(a, b expr.Node, in *dtype.Schema) bool {
	fa, err := a.Field(in)
	if err != nil {
		return false
	}
	fb, err := b.Field(in)
	if err != nil {
		return false
	}
	return fa.Type == fb.Type && fa.Nullable == fb.Nullable
}
