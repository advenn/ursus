package ursus_test

// End-to-end simplification, with a constant folder attached.
//
// internal/plan's own tests run the structural rewrites with NO folder — that is
// what keeps that layer free of Arrow. These are the other half: the same rule
// with physical.KernelFolder injected by lazy.go, checked against real rows.

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/ursustest"
)

// noSimplify is DefaultFlags with just this step's rule off, so a comparison
// isolates simplification rather than turning the whole optimizer off.
func noSimplify() plan.Flags {
	f := plan.DefaultFlags()
	f.SimplifyExprs = false
	return f
}

// TestConstantFolding: the plan text collapses AND the rows do not move.
//
// Both halves are needed. A rule that silently did nothing would pass every
// result comparison in the suite, and a rule that folded wrongly would pass a
// plan-text check — the whole point is that the two agree.
func TestConstantFolding(t *testing.T) {
	for _, c := range []struct {
		name string
		expr ursus.Expr
		want string // what the folded literal must render as
	}{
		{"integer arithmetic", ursus.Lit(2).Mul(3), "lit(6)"},
		{"float arithmetic", ursus.Lit(1.5).Add(2.5), "lit(4)"},
		{"comparison", ursus.Lit(2).Gt(1), "lit(true)"},
		{"nested", ursus.Lit(2).Mul(3).Add(4), "lit(10)"},
		{"cast", ursus.Lit(7).Cast(ursus.Float64), "lit(7)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			q := func() *ursus.LazyFrame {
				return frame().Select(ursus.Col("id"), c.expr.Alias("k"))
			}

			txt, err := q().Explain(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(txt, c.want) {
				t.Errorf("plan does not contain %s:\n%s", c.want, txt)
			}

			on, err := q().Collect(t.Context(), ursus.WithVerify())
			if err != nil {
				t.Fatal(err)
			}
			off, err := q().Collect(t.Context(), ursus.WithOptFlags(noSimplify()))
			if err != nil {
				t.Fatal(err)
			}
			ursustest.AssertFrameEqual(t, on, off)
		})
	}
}

// TestFoldingUsesTheRealKernel is why the folder lives at L50.
//
// 0.1 + 0.2 is 0.30000000000000004, and a folder that reached for Go arithmetic
// on its own would have to reproduce every promotion, overflow and rounding rule
// the kernel already implements. It would agree here and diverge somewhere, and
// the divergence would show up as one query disagreeing with itself depending on
// whether a value happened to be foldable.
//
// So the assertion is not "0.1+0.2 is close to 0.3". It is that the folded plan
// and the unfolded plan produce the SAME BITS.
func TestFoldingUsesTheRealKernel(t *testing.T) {
	// `evaluated` reaches the same sum through a column, so it cannot be folded and
	// always goes through the kernel at run time. The two must agree exactly.
	lf := ursus.Frame(ursus.Values("zero", []float64{0})).
		Select(
			ursus.Lit(0.1).Add(0.2).Alias("folded"),
			ursus.Col("zero").Add(0.1).Add(0.2).Alias("evaluated"),
		)

	txt, err := lf.Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(txt, "lit(0.30000000000000004)") {
		t.Errorf("the fold did not go through the kernel:\n%s", txt)
	}

	df, err := lf.Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	folded, err := df.Column[float64]("folded")
	if err != nil {
		t.Fatal(err)
	}
	evaluated, err := df.Column[float64]("evaluated")
	if err != nil {
		t.Fatal(err)
	}
	if folded.Values()[0] != evaluated.Values()[0] {
		t.Errorf("folded %v != evaluated %v — the folder and the executor disagree",
			folded.Values()[0], evaluated.Values()[0])
	}
}

// TestFoldingRefusesOnError: an execution error must not become a plan-time one.
//
// Division is NOT the example, which is worth recording because it is the obvious
// guess. Float division by zero is +Inf and integer division by zero is a null —
// the kernel's documented choices, neither of them an error. The operation that
// actually fails is a strict cast that cannot represent its value, and folding it
// eagerly would move the failure from Collect to Explain.
func TestFoldingRefusesOnError(t *testing.T) {
	overflow := func() *ursus.LazyFrame {
		return frame().Select(ursus.Lit(300).Cast(ursus.Int8).Alias("boom"))
	}

	t.Run("the cast survives in the plan", func(t *testing.T) {
		txt, err := overflow().Explain(t.Context())
		if err != nil {
			t.Fatalf("folding turned an execution error into a PLAN-time one: %v", err)
		}
		if !strings.Contains(txt, "lit(300).cast(Int8)") {
			t.Errorf("the failing cast was folded away:\n%s", txt)
		}
	})

	t.Run("and still fails where it always did", func(t *testing.T) {
		_, on := overflow().Collect(t.Context())
		_, off := overflow().Collect(t.Context(), ursus.WithOptFlags(noSimplify()))
		if (on == nil) != (off == nil) {
			t.Fatalf("simplification changed whether the query fails: on=%v off=%v", on, off)
		}
		if on == nil {
			t.Fatal("a strict cast of 300 to Int8 should fail")
		}
	})

	t.Run("Explain still works on a doomed query", func(t *testing.T) {
		// This is the concrete cost of folding an error, and it is worth stating
		// plainly because the intuitive example is wrong: a Cond does NOT protect
		// its untaken arm here. evalCond computes both branches and selects between
		// them, so `When(c).Then(a).Otherwise(bad)` fails whether or not c is ever
		// false — with this rule on, with it off, and before this step existed.
		//
		// What folding would genuinely break is asking to SEE the plan. Explain
		// resolves and optimizes without evaluating anything, so a query that only
		// fails when run must still be explainable.
		lf := frame().Select(
			ursus.When(ursus.Col("price").Gt(0)).
				Then(ursus.Lit(1)).
				Otherwise(ursus.Lit(300).Cast(ursus.Int8)).
				Alias("safe"),
		)
		if _, err := lf.Explain(t.Context()); err != nil {
			t.Errorf("Explain must not run the query: %v", err)
		}
	})
}

// TestIntegerDivisionByZeroNowFolds is this test in its third life, and the change
// is the point of step 49.
//
// It used to assert the OPPOSITE — that the folder refuses `lit(1) // lit(0)` — and
// its own doc explained why: "Binary.Field says the result of dividing two non-null
// literals is non-null, so the folded literal would be nullable where the expression
// it replaced was not." The refusal was not a property of folding. It was the
// constant folder correctly declining to make a schema lie visible.
//
// Binary.Field now consults MayProduceNull, so the expression is declared nullable
// before anything is folded, the folded literal matches it, fieldEqual permits the
// rewrite, and the fold happens. **The lie was costing the optimizer a fold**, which
// is the concrete answer to "what does a schema-only defect actually break".
//
// local.go describes exactly this mechanism from the other side: the guard "does
// rather than a blanket refusal: when x is NOT nullable both sides are non-null, the
// fold is sound, and it is allowed."
func TestIntegerDivisionByZeroNowFolds(t *testing.T) {
	q := func() *ursus.LazyFrame {
		return frame().Select(ursus.Lit(1).FloorDiv(0).Alias("k"))
	}

	txt, err := q().Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(txt, "(lit(1) // lit(0))") {
		t.Errorf("the division is now declared nullable, so the fold is sound "+
			"and should happen:\n%s", txt)
	}
	if !strings.Contains(txt, "lit(null") {
		t.Errorf("it should have folded to a null literal:\n%s", txt)
	}

	on, err := q().Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	off, err := q().Collect(t.Context(), ursus.WithOptFlags(noSimplify()))
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, on, off)
}

// TestFoldingFollowsTheKernelNotIntuition: +Inf is the right answer, so it folds.
//
// The mirror of the two tests above, and the reason they are not simply "refuse
// anything that looks risky". Float division by zero is a defined IEEE result,
// the kernel produces it, and folding reproduces it exactly — a folder that
// declined here out of caution would be wrong in a quieter way.
func TestFoldingFollowsTheKernelNotIntuition(t *testing.T) {
	txt, err := frame().Select(ursus.Lit(1.0).Div(0.0).Alias("k")).Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(txt, "lit(+Inf)") {
		t.Errorf("float division by zero should fold to +Inf:\n%s", txt)
	}
}

// TestSimplifyIsNotASemanticKnob: every shape must give the same rows with the
// rule on and off.
//
// The shape TestMemoryLimitIsNotASemanticKnob uses, and the reason is the same:
// an optimizer flag that changes an ANSWER is not an optimizer flag.
func TestSimplifyIsNotASemanticKnob(t *testing.T) {
	for _, c := range []struct {
		name string
		lf   func() *ursus.LazyFrame
	}{
		{"true predicate", func() *ursus.LazyFrame {
			return frame().Filter(ursus.Lit(true)).Select(ursus.Col("id"))
		}},
		{"false predicate", func() *ursus.LazyFrame {
			return frame().Filter(ursus.Lit(false)).Select(ursus.Col("id"))
		}},
		{"folded predicate", func() *ursus.LazyFrame {
			return frame().Filter(ursus.Col("price").Gt(ursus.Lit(2).Mul(2.5)))
		}},
		{"identity projection", func() *ursus.LazyFrame {
			return frame().Select(ursus.Col("id"), ursus.Col("price"))
		}},
		{"and with a true literal", func() *ursus.LazyFrame {
			return frame().Filter(ursus.Col("price").Gt(5).And(ursus.Lit(true)))
		}},
		{"and with a false literal", func() *ursus.LazyFrame {
			return frame().Filter(ursus.Col("price").Gt(5).And(ursus.Lit(false)))
		}},
		{"double negation", func() *ursus.LazyFrame {
			return frame().Filter(ursus.Col("price").Gt(5).Not().Not())
		}},
		{"aggregate over a folded key", func() *ursus.LazyFrame {
			return frame().GroupBy(ursus.Col("region")).
				Agg(ursus.Col("qty").Sum().Alias("q"))
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			on, err := c.lf().Collect(t.Context(), ursus.WithVerify())
			if err != nil {
				t.Fatal(err)
			}
			off, err := c.lf().Collect(t.Context(), ursus.WithOptFlags(noSimplify()))
			if err != nil {
				t.Fatal(err)
			}
			ursustest.AssertFrameEqual(t, on, off)
		})
	}
}

// TestDeadFilterReturnsNoRows is the end-to-end half of the null-predicate trap.
//
// internal/plan asserts the plan SHAPE — a Limit{N:0} rather than a removed node.
// This asserts the rows, because the shape is only interesting if it means what
// it is supposed to mean.
func TestDeadFilterReturnsNoRows(t *testing.T) {
	for _, c := range []struct {
		name string
		pred ursus.Expr
	}{
		{"false", ursus.Lit(false)},
		{"null", ursus.Null(ursus.Bool)},
	} {
		t.Run(c.name, func(t *testing.T) {
			df, err := frame().Filter(c.pred).Collect(t.Context(), ursus.WithVerify())
			if err != nil {
				t.Fatal(err)
			}
			if df.Height() != 0 {
				t.Errorf("height = %d, want 0 — a predicate that is never true keeps "+
					"no rows, and removing the node instead keeps every one of them",
					df.Height())
			}
		})
	}

	t.Run("a true predicate keeps every row", func(t *testing.T) {
		df, err := frame().Filter(ursus.Lit(true)).Collect(t.Context(), ursus.WithVerify())
		if err != nil {
			t.Fatal(err)
		}
		if df.Height() != 6 {
			t.Errorf("height = %d, want 6", df.Height())
		}
	})
}
