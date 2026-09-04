package plan_test

// Everything in this file runs with NO constant folder attached, which is the
// point twice over: it is what design/logical.md §13's "resolved and optimized
// without an executor, without Arrow" means in practice, and it isolates the
// structural rewrites from the kernel so a failure here cannot be blamed on
// arithmetic. TestPlanPackageNeedsNoArrow asserts the layering the rest of the
// file relies on.

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/plan"
)

func and(l, r expr.Node) expr.Node { return &expr.Binary{Op: expr.OpAnd, L: l, R: r} }
func or(l, r expr.Node) expr.Node  { return &expr.Binary{Op: expr.OpOr, L: l, R: r} }
func not(c expr.Node) expr.Node    { return &expr.Unary{Op: expr.OpNot, Child: c} }

func boolLit(v bool) expr.Node { return &expr.Lit{Value: v, DT: dtype.Bool} }
func nullLit() expr.Node       { return &expr.Lit{Value: nil, DT: dtype.Bool} }

// flagged is wide() with a Boolean column of each nullability, because the whole
// guard story turns on that difference.
func flagged() *fakeSource {
	return &fakeSource{
		schema: dtype.MustSchema(
			dtype.NotNull("id", dtype.Int64),
			dtype.Of("maybe", dtype.Bool),     // nullable
			dtype.NotNull("sure", dtype.Bool), // not
			dtype.Of("price", dtype.Float64),
		),
		caps: plan.Caps{Projection: true},
	}
}

// explain renders an optimized plan, for tests that care about its shape.
func explain(t *testing.T, n plan.Node) string {
	t.Helper()
	s, err := plan.Explain(n, plan.DefaultExplainOptions())
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	return s
}

func filterOf(src *fakeSource, preds ...expr.Node) plan.Node {
	return &plan.Filter{Input: &plan.Scan{Src: src}, Preds: preds}
}

// TestBooleanIdentitiesUnderNulls covers the four AND/OR identities.
//
// Each is sound under Kleene three-valued logic, and the null case is the half
// worth pinning: `null AND true` is null, which is what the surviving operand
// already was, so the rewrite is a no-op on all three values rather than on two.
func TestBooleanIdentitiesUnderNulls(t *testing.T) {
	// want is the predicate that should SURVIVE, as an expression rather than as a
	// rendered string: comparing n.String() to n.String() cannot drift when the
	// operators change how they print, and a nil want means the whole Filter goes.
	cases := []struct {
		name string
		pred expr.Node
		want expr.Node
	}{
		{"x AND true", and(col("maybe"), boolLit(true)), col("maybe")},
		{"true AND x", and(boolLit(true), col("maybe")), col("maybe")},
		{"x OR false", or(col("maybe"), boolLit(false)), col("maybe")},
		{"false OR x", or(boolLit(false), col("maybe")), col("maybe")},

		// The absorbing elements, and the nullability split that decides them.
		// `sure` is non-null, so `sure AND false` is non-null too and collapsing it
		// to lit(false) preserves the field exactly — after which the Filter keeps
		// nothing and becomes a Limit{N:0}.
		{"non-null x AND false", and(col("sure"), boolLit(false)), nil},
		{"non-null x OR true", or(col("sure"), boolLit(true)), nil},

		// `maybe` IS nullable, so `maybe AND false` is a nullable Boolean while
		// lit(false) is not. Value-sound, schema-unsound, refused — see fieldEqual.
		// The predicate must come out exactly as it went in.
		{"nullable x AND false", and(col("maybe"), boolLit(false)),
			and(col("maybe"), boolLit(false))},
		{"nullable x OR true", or(col("maybe"), boolLit(true)),
			or(col("maybe"), boolLit(true))},

		// A NULL literal is not an identity element for either operator: the result
		// depends on x, so there is nothing to remove.
		{"x AND null", and(col("maybe"), nullLit()), and(col("maybe"), nullLit())},
		{"x OR null", or(col("maybe"), nullLit()), or(col("maybe"), nullLit())},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := explain(t, optimize(t, resolve(t, filterOf(flagged(), c.pred))))
			if c.want == nil {
				// `x OR true` keeps every row and `x AND false` keeps none, so the
				// two absorbing cases part company here rather than both vanishing.
				if strings.Contains(got, "FILTER") {
					t.Errorf("predicate should have collapsed to a constant:\n%s", got)
				}
				return
			}
			if want := "FILTER [" + c.want.String() + "]"; !strings.Contains(got, want) {
				t.Errorf("got:\n%s\nwant %s", got, want)
			}
		})
	}
}

// TestDoubleNegationRespectsNames is the naming trap.
//
// Not(Not(x)) is value-identical to x on all three Kleene values, but OutputName
// consults naming nodes only at the ROOT: the doubled negation falls through to
// leftmost-column-wins and is named after the column, while the bare child
// carries its Alias. Rewriting the first into the second renames an output
// column, which no schema check below the root would see.
func TestDoubleNegationRespectsNames(t *testing.T) {
	t.Run("plain column is simplified", func(t *testing.T) {
		p := &plan.Project{
			Input: &plan.Scan{Src: flagged()},
			Exprs: []expr.Node{not(not(col("maybe")))},
		}
		got := explain(t, optimize(t, resolve(t, p)))
		if strings.Contains(got, "not()") {
			t.Errorf("double negation survived:\n%s", got)
		}
	})

	t.Run("aliased child is refused", func(t *testing.T) {
		p := &plan.Project{
			Input: &plan.Scan{Src: flagged()},
			Exprs: []expr.Node{not(not(&expr.Alias{Child: col("maybe"), Name: "renamed"}))},
		}
		r := optimize(t, resolve(t, p))

		// The rewrite would have named this column "renamed"; the original names it
		// "maybe". Either the rule declines, or the schema changes — and Optimizer
		// .Verify only sees the root, which this IS, so the assertion is on the name.
		s, err := r.Schema()
		if err != nil {
			t.Fatal(err)
		}
		if got, want := s.String(), "{maybe: Bool}"; got != want {
			t.Errorf("schema = %s, want %s — the rewrite renamed an output column", got, want)
		}
	})
}

// TestDeadFilterKeepsNoRows is the one silent wrong answer available here.
//
// A predicate has three outcomes and only `true` keeps a row. Removing the Filter
// node when the predicate is constant is right for lit(true) and catastrophic for
// the other two: it returns EVERY row where none belong, with an identical schema
// and no error anywhere.
func TestDeadFilterKeepsNoRows(t *testing.T) {
	for _, c := range []struct {
		name string
		pred expr.Node
	}{
		{"false", boolLit(false)},
		{"null", nullLit()},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := optimize(t, resolve(t, filterOf(flagged(), c.pred)))

			l, ok := r.(*plan.Limit)
			if !ok {
				t.Fatalf("want a Limit that keeps nothing, got %T:\n%s", r, explain(t, r))
			}
			if l.N != 0 {
				t.Errorf("Limit N = %d, want 0", l.N)
			}
			if strings.Contains(explain(t, r), "FILTER") {
				t.Errorf("the dead filter survived:\n%s", explain(t, r))
			}
		})
	}

	t.Run("true removes the node and keeps every row", func(t *testing.T) {
		r := optimize(t, resolve(t, filterOf(flagged(), boolLit(true))))
		got := explain(t, r)
		if strings.Contains(got, "FILTER") {
			t.Errorf("a true predicate constrains nothing and should go:\n%s", got)
		}
		if strings.Contains(got, "LIMIT") {
			t.Errorf("a true predicate must NOT truncate:\n%s", got)
		}
	})

	t.Run("one dead conjunct kills the whole filter", func(t *testing.T) {
		// `price > 5 AND false` keeps nothing, however selective the first half is.
		r := optimize(t, resolve(t, filterOf(flagged(),
			&expr.Binary{Op: expr.OpGt, L: col("price"), R: lit(5.0, dtype.Float64)},
			boolLit(false))))
		if l, ok := r.(*plan.Limit); !ok || l.N != 0 {
			t.Errorf("want Limit{N:0}, got:\n%s", explain(t, r))
		}
	})
}

// TestIdentityProjectRemoved is design/logical.md:1557's worked example.
func TestIdentityProjectRemoved(t *testing.T) {
	t.Run("exact match in order is removed", func(t *testing.T) {
		p := &plan.Project{
			Input: &plan.Scan{Src: flagged()},
			Exprs: []expr.Node{col("id"), col("maybe"), col("sure"), col("price")},
		}
		if got := explain(t, optimize(t, resolve(t, p))); strings.Contains(got, "PROJECT") {
			t.Errorf("an identity projection should be removed:\n%s", got)
		}
	})

	t.Run("reordering is NOT an identity", func(t *testing.T) {
		// The same field SET as the input, and emphatically not a no-op — which is
		// why the check is structural and positional rather than schema equality.
		p := &plan.Project{
			Input: &plan.Scan{Src: flagged()},
			Exprs: []expr.Node{col("maybe"), col("id"), col("sure"), col("price")},
		}
		r := optimize(t, resolve(t, p))
		if !strings.Contains(explain(t, r), "PROJECT") {
			t.Errorf("a reordering projection must survive:\n%s", explain(t, r))
		}
		s, err := r.Schema()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(s.String(), "{maybe:") {
			t.Errorf("schema = %s, want maybe first", s.String())
		}
	})

	t.Run("a subset is NOT an identity", func(t *testing.T) {
		p := &plan.Project{
			Input: &plan.Scan{Src: flagged()},
			Exprs: []expr.Node{col("id")},
		}
		// Projection pushdown narrows the scan to [id], which is what MAKES this an
		// identity; the node then goes. What must survive is the narrowing itself.
		r := optimize(t, resolve(t, p))
		s, err := r.Schema()
		if err != nil {
			t.Fatal(err)
		}
		if got, want := s.String(), "{id: Int64!}"; got != want {
			t.Errorf("schema = %s, want %s", got, want)
		}
	})
}

// TestSimplifyReachesItsFixedPointInOnePass is the property that justifies
// registering the rule Once.
//
// The rule IS registered Once, so a full collapse here is a proof rather than an
// illustration: if any of these needed a second sweep, the leftover would still
// be in the plan text. It holds because the expression walk rewrites children
// first and then applies the rewrites to the REBUILT parent, so a chain unwinds
// from the inside out in a single descent.
//
// This is the opposite of what design/logical.md §8.1 predicted — it offers
// folding as the UntilStable example — and the correction is recorded here rather
// than in a comment nobody would check.
func TestSimplifyReachesItsFixedPointInOnePass(t *testing.T) {
	deep := col("maybe")
	for range 6 {
		deep = not(deep)
	}

	for _, c := range []struct {
		name string
		e    expr.Node
		bad  string
	}{
		{"four negations", not(not(not(not(col("maybe"))))), "not()"},
		{"six negations", deep, "not()"},
		{"chained AND identities",
			and(and(col("maybe"), boolLit(true)), boolLit(true)), "lit(true)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := &plan.Project{
				Input: &plan.Scan{Src: flagged()},
				Exprs: []expr.Node{c.e},
			}
			got := explain(t, optimize(t, resolve(t, p)))
			if strings.Contains(got, c.bad) {
				t.Errorf("%q survives, so one pass was not enough:\n%s", c.bad, got)
			}
		})
	}
}

// TestRefusalIsPerExpressionNotPerNode is what fieldEqual buys over the
// node-level sameSchema check in Local.
//
// Both catch the naming trap. The difference is BLAST RADIUS: sameSchema can only
// decline the whole Match, so one unsound rewrite inside a Project would throw
// away every sound rewrite beside it. fieldEqual declines just the expression
// that would have renamed a column, and the rest of the list still simplifies.
//
// Without it this test does not fail loudly — it quietly stops optimizing.
func TestRefusalIsPerExpressionNotPerNode(t *testing.T) {
	p := &plan.Project{
		Input: &plan.Scan{Src: flagged()},
		Exprs: []expr.Node{
			// Refused: the bare child is named "renamed" and the doubled negation
			// is named "maybe".
			not(not(&expr.Alias{Child: col("maybe"), Name: "renamed"})),
			// Sound, and must survive the refusal above.
			&expr.Alias{Child: and(col("sure"), boolLit(true)), Name: "k"},
		},
	}
	got := explain(t, optimize(t, resolve(t, p)))

	if strings.Contains(got, "lit(true)") {
		t.Errorf("the sound rewrite was discarded along with the refused one:\n%s", got)
	}
	if !strings.Contains(got, `col("sure").alias("k")`) {
		t.Errorf(`want col("sure").alias("k"):`+"\n%s", got)
	}
}

// TestPlanPackageNeedsNoArrow pins design/logical.md §13.
//
// The claim is that a plan can be built, resolved, optimized and explained with
// no executor, no Arrow and no Parquet file — which is why constant folding is
// injected through plan.ConstEvaluator rather than imported from internal/kernel.
// Stated in prose it survives exactly until someone adds an import for a good
// reason; stated here it does not.
func TestPlanPackageNeedsNoArrow(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	// Not simd/archsimd: under GOEXPERIMENT=simd the standard library pulls it in
	// at every level — internal/uerr at L0 has it too — so its presence says
	// nothing about this package's own imports. What matters is the columnar
	// stack, which is what folding would have dragged in.
	for _, dep := range strings.Split(string(out), "\n") {
		switch {
		case strings.Contains(dep, "arrow"),
			strings.Contains(dep, "github.com/advenn/ursus/internal/kernel"),
			strings.Contains(dep, "github.com/advenn/ursus/internal/data"),
			strings.Contains(dep, "github.com/advenn/ursus/internal/arrowx"),
			strings.Contains(dep, "github.com/advenn/ursus/internal/bitmap"):
			t.Errorf("internal/plan depends on %q; folding is injected through "+
				"ConstEvaluator precisely so it does not", dep)
		}
	}
}
