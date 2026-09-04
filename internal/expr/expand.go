package expr

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// Expand rewrites one written expression into the concrete expressions it denotes.
//
//	Col("a")                          → [col("a")]                       (unchanged)
//	Col("a","b").Mul(2)               → [(col("a") * 2), (col("b") * 2)]
//	ColDType(Float64).Round(2)        → one node per Float64 column
//	All().Exclude("id")               → one node per remaining column
//
// # Where this runs, and why there
//
// Expansion is a PLAN-TIME, SCHEMA-DRIVEN rewrite. It runs in the resolve phase:
// after a node's input schema is known, before type checking, and before any
// optimizer rule sees the plan.
//
// It cannot run earlier — `ColDType(Float64)` has no meaning without a schema.
// It must not run later — projection pushdown asks each expression which columns
// it reads, and an unexpanded matcher would answer "none", so the optimizer would
// happily prune away the very columns the expression is about to select. Running
// before type checking also means the type checker only ever sees single-output
// trees, which is what lets Field return one Field instead of a slice.
//
// # One matcher per expression
//
// A tree may contain at most one DISTINCT matcher. The same matcher repeated is
// fine and expands in lockstep — `All().Sub(All().Mean())` gives each column minus
// its own mean. Two different matchers are rejected, because the intended pairing
// is genuinely ambiguous: is it a zip, or a cross product?
func Expand(n Node, in *dtype.Schema) ([]Node, error) {
	if err := FirstErr(n); err != nil {
		return nil, err
	}

	matchers := distinctMatchers(n)
	switch len(matchers) {
	case 0:
		return []Node{n}, nil
	case 1:
		// fall through
	default:
		e := uerr.New(uerr.KindSchema, "",
			"expression contains %d different multi-column selections", len(matchers))
		for _, m := range matchers {
			e.Hint("selection: %s", m.String())
		}
		e.Hint("an expression may expand over at most one selection; " +
			"write the others explicitly or split into separate expressions")
		return nil, e
	}

	m := matchers[0]
	names, err := m.Match(in)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		// Matching nothing yields nothing. This is deliberate: `cs.Float()` over a
		// frame with no floats should be a no-op, not a failure, or every
		// schema-driven pipeline would need a guard.
		return nil, nil
	}

	out := make([]Node, len(names))
	for i, name := range names {
		out[i] = substitute(n, name)
	}

	if err := checkDistinctNames(out, m); err != nil {
		return nil, err
	}
	return out, nil
}

// ExpandAll expands a slice of expressions, concatenating the results in order.
func ExpandAll(ns []Node, in *dtype.Schema) ([]Node, error) {
	out := make([]Node, 0, len(ns))
	for _, n := range ns {
		xs, err := Expand(n, in)
		if err != nil {
			return nil, err
		}
		out = append(out, xs...)
	}
	return out, nil
}

// distinctMatchers collects the matchers in a tree, de-duplicated by their
// rendered form. Two matchers that render identically select identically, so
// treating them as one is safe and makes `All().Sub(All().Mean())` work.
func distinctMatchers(n Node) []Matcher {
	var out []Matcher
	seen := map[string]struct{}{}
	Walk(n, func(x Node) bool {
		if m, ok := x.(*Match); ok {
			k := m.M.String()
			if _, dup := seen[k]; !dup {
				seen[k] = struct{}{}
				out = append(out, m.M)
			}
		}
		return true
	})
	return out
}

// substitute returns a copy of n with every *Match replaced by col(name).
//
// The tree is rebuilt rather than mutated. Nodes are immutable and shared between
// LazyFrames, so mutating in place would corrupt every other expression holding a
// reference to the same subtree.
func substitute(n Node, name string) Node {
	if _, isMatch := n.(*Match); isMatch {
		return &Col{Name: name}
	}
	kids := n.Children()
	if len(kids) == 0 {
		return n
	}
	out := make([]Node, len(kids))
	for i, c := range kids {
		out[i] = substitute(c, name)
	}
	// Rebuild copies every non-child field, so a node type that grows a parameter
	// cannot silently lose it here — which is what happened twice in step 7 while
	// this function enumerated the types by hand.
	return Rebuild(n, out)
}

// checkDistinctNames rejects an expansion whose outputs collide.
//
// The classic case is an alias applied to a multi-column expression:
// `Col("a","b").Alias("x")` would produce two columns both named "x". Catching it
// here, with the offending alias named, is far better than letting NewSchema
// report an anonymous duplicate later.
func checkDistinctNames(ns []Node, m Matcher) error {
	seen := make(map[string]int, len(ns))
	for i, n := range ns {
		name := OutputName(n)
		if prev, dup := seen[name]; dup {
			e := uerr.New(uerr.KindSchema, "",
				"selection %s expands to %d columns that would all be named %q",
				m.String(), len(ns), name)
			e.Hint("expanded from: %s", n.String())
			if _, isAlias := n.(*Alias); isAlias {
				e.Hint("Alias sets one fixed name; use Name().Prefix(...) or " +
					"Name().Suffix(...) to rename an expansion")
			}
			e.Hint("collision between expansion %d and %d", prev, i)
			return e
		}
		seen[name] = i
	}
	return nil
}

// --- Rename ------------------------------------------------------------------

// Rename applies a function to the output name.
//
// This is the expansion-safe counterpart to Alias: where Alias imposes one fixed
// name (and so cannot be applied to a multi-column expression), Rename derives a
// name per output, which is what Name().Prefix / .Suffix / .Map need.
type Rename struct {
	Child Node
	Fn    func(string) string
	Label string // rendering only, e.g. `name.prefix("avg_")`
}

func (r *Rename) node()            {}
func (r *Rename) Children() []Node { return []Node{r.Child} }
func (r *Rename) String() string   { return r.Child.String() + "." + r.Label }

func (r *Rename) Field(in *dtype.Schema) (dtype.Field, error) {
	f, err := r.Child.Field(in)
	if err != nil {
		return dtype.Field{}, err
	}
	return f.Rename(r.Fn(f.Name)), nil
}
