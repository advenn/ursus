package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/plan"
)

// TestPushdownSoundness is the ONLY mechanism that can catch a bad predicate push.
//
// The optimizer's Verify mode compares schemas before and after each rule, and a
// wrongly-moved filter does not change the schema — it changes which ROWS come
// back. So soundness is checked the only way it can be: run each query with the
// optimizer on and off, and require identical results.
func TestPushdownSoundness(t *testing.T) {
	queries := []struct {
		name string
		lf   func() *ursus.LazyFrame
	}{
		{"filter below sort", func() *ursus.LazyFrame {
			return sales().Sort(ursus.Asc(ursus.Col("qty"))).
				Filter(ursus.Col("qty").Gt(2))
		}},
		{"filter below select", func() *ursus.LazyFrame {
			return sales().Select(ursus.Col("region"), ursus.Col("qty")).
				Filter(ursus.Col("qty").Gt(3))
		}},
		{"filter on a renamed column", func() *ursus.LazyFrame {
			return sales().Select(ursus.Col("qty").Alias("n")).
				Filter(ursus.Col("n").Gt(3))
		}},
		{"filter on a COMPUTED column", func() *ursus.LazyFrame {
			// The substitution case: pushing `doubled > 6` verbatim would filter the
			// un-doubled column and return the wrong rows.
			return sales().WithColumns(ursus.Col("qty").Mul(2).Alias("doubled")).
				Filter(ursus.Col("doubled").Gt(6))
		}},
		{"filter on a SHADOWED column", func() *ursus.LazyFrame {
			// The nastiest case: the name means different things above and below.
			return sales().WithColumns(ursus.Col("qty").Mul(10).Alias("qty")).
				Filter(ursus.Col("qty").Gt(30))
		}},
		{"filter above a limit", func() *ursus.LazyFrame {
			return sales().Head(3).Filter(ursus.Col("qty").Gt(1))
		}},
		{"filter above an aggregate (HAVING)", func() *ursus.LazyFrame {
			return sales().GroupBy(ursus.Col("region")).
				Agg(ursus.Len().Alias("n")).
				Filter(ursus.Col("n").Gt(2))
		}},
		{"filter above a whole-row distinct", func() *ursus.LazyFrame {
			return sales().Unique().Filter(ursus.Col("qty").Gt(3))
		}},
		{"filter above a subset distinct, on a NON-subset column", func() *ursus.LazyFrame {
			// Pushing this would change which row is kept as the representative.
			return sales().Unique("region").Filter(ursus.Col("qty").Gt(3))
		}},
		{"filter above a subset distinct, on the subset", func() *ursus.LazyFrame {
			return sales().Unique("region").Filter(ursus.Col("region").Ne("us"))
		}},
		{"two filters and a sort", func() *ursus.LazyFrame {
			return sales().
				Filter(ursus.Col("qty").Gt(1)).
				Sort(ursus.Desc(ursus.Col("qty"))).
				Filter(ursus.Col("region").Ne("apac"))
		}},
		{"filter with a null predicate", func() *ursus.LazyFrame {
			return sales().Sort(ursus.Asc(ursus.Col("qty"))).
				Filter(ursus.Col("amount").Gt(15))
		}},
	}

	for _, q := range queries {
		t.Run(q.name, func(t *testing.T) {
			on, err := q.lf().Collect(t.Context(), ursus.WithVerify())
			if err != nil {
				t.Fatalf("optimized: %v", err)
			}
			off, err := q.lf().Collect(t.Context(), ursus.WithOptFlags(plan.NoFlags()))
			if err != nil {
				t.Fatalf("unoptimized: %v", err)
			}
			if on.String() != off.String() {
				optPlan, _ := q.lf().Explain(t.Context())
				t.Errorf("optimizer changed the result\n optimized:\n%s\n unoptimized:\n%s\n plan:\n%s",
					on, off, optPlan)
			}
		})
	}
}

// TestLimitIsAPushdownBarrier pins the classic bug with a limit that BINDS.
//
//	Filter(Limit(2, [1..5]), x > 3)  ==  []
//	Limit(2, Filter([1..5], x > 3))  ==  [4,5]
//
// Different cardinality and different rows. Every fixture where the limit does not
// bind passes either way, which is exactly why this needs a case built to bind.
func TestLimitIsAPushdownBarrier(t *testing.T) {
	lf := ursus.Frame(ursus.Values("x", []int64{1, 2, 3, 4, 5})).
		Head(2).
		Filter(ursus.Col("x").Gt(3))

	df, err := lf.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 0 {
		t.Errorf("got %d rows, want 0 — the filter was pushed below the limit\n%s",
			df.Height(), df)
	}

	// The plan must still show the Filter ABOVE the Limit.
	p, err := lf.Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	fi, li := strings.Index(p, "FILTER"), strings.Index(p, "LIMIT")
	if fi < 0 || li < 0 || fi > li {
		t.Errorf("FILTER must remain above LIMIT:\n%s", p)
	}
}

// TestPushdownMovesFilterBelowSort proves the rule does something, not merely
// nothing safely.
func TestPushdownMovesFilterBelowSort(t *testing.T) {
	lf := sales().
		Sort(ursus.Asc(ursus.Col("qty"))).
		Filter(ursus.Col("qty").Gt(2))

	p, err := lf.Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	si, fi := strings.Index(p, "SORT"), strings.Index(p, "FILTER")
	if si < 0 || fi < 0 || si > fi {
		t.Errorf("FILTER should have moved below SORT:\n%s", p)
	}
}

// TestPushdownSubstitutesComputedColumns checks that a predicate over a computed
// column is REWRITTEN as it descends, rather than moved verbatim.
func TestPushdownSubstitutesComputedColumns(t *testing.T) {
	lf := sales().
		WithColumns(ursus.Col("qty").Mul(10).Alias("qty")).
		Filter(ursus.Col("qty").Gt(30))

	p, err := lf.Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// The pushed-down predicate must mention the multiplication; a verbatim push
	// would read `col("qty") > 30` against the un-multiplied column.
	if !strings.Contains(p, `(col("qty") * lit(10)) > lit(30)`) {
		t.Errorf("predicate was not substituted on the way down:\n%s", p)
	}

	df, err := lf.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// qty*10 > 30 means qty > 3, i.e. rows with qty 4,5,6,7.
	if df.Height() != 4 {
		t.Errorf("got %d rows, want 4\n%s", df.Height(), df)
	}
}
