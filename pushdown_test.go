package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

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
