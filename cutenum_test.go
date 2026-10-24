package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestCutIsAnEnum is step 350: Cut answers an Enum of its bins' labels in their
// order, as Polars 1.44.1's cut does, where it answered a String. Every answer is
// Polars'.
func TestCutIsAnEnum(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	x := ursus.Frame(i64s("x", 1, 5, 9, nil))
	for _, tc := range []struct {
		name string
		e    ursus.Expr
		want string
	}{
		{"by intervals", c("x").Cut([]float64{3, 6}), "(-inf, 3] (3, 6] (6, inf] <nil> Enum(\"(-inf, 3]\", \"(3, 6]\", \"(6, inf]\")"},
		{"labelled", c("x").Cut([]float64{3, 6}, ursus.CutLabels("lo", "mid", "hi")), "lo mid hi <nil> Enum(lo, mid, hi)"},
		{"closed on the left", c("x").Cut([]float64{3, 6}, ursus.CutLeftClosed()), "[-inf, 3) [3, 6) [6, inf) <nil> Enum(\"[-inf, 3)\", \"[3, 6)\", \"[6, inf)\")"},
		// A break in decimal, as Polars writes it.
		{"small and large breaks", c("x").Cut([]float64{1e-7, 1e20}),
			"(0.0000001, 100000000000000000000] (0.0000001, 100000000000000000000] (0.0000001, 100000000000000000000] <nil> Enum(\"(-inf, 0.0000001]\", \"(0.0000001, 100000000000000000000]\", \"(100000000000000000000, inf]\")"},
		{"no break", c("x").Cut(nil), "(-inf, inf] (-inf, inf] (-inf, inf] <nil> Enum(\"(-inf, inf]\")"},
	} {
		if got := cells(t, x.Select(tc.e), "x"); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}

	// An Enum compares with its labels' text, and groups and sorts in its order.
	if got := cells(t, x.Filter(c("x").Cut([]float64{3, 6}).Eq("(3, 6]")), "x"); got != "5 Int64" {
		t.Errorf("== a label: %s", got)
	}
	g := ursus.Frame(i64s("x", 9, 1, 5)).GroupBy(c("x").Cut([]float64{3}).Alias("b")).Agg(ursus.Len()).Sort(ursus.Asc(c("b")))
	if got := cells(t, g, "b"); got != "(-inf, 3] (3, inf] Enum(\"(-inf, 3]\", \"(3, inf]\")" {
		t.Errorf("grouped: %s", got)
	}

	// Polars: "FrozenCategories must contain unique strings; found duplicate 'a'".
	if _, err := x.Select(c("x").Cut([]float64{3, 6}, ursus.CutLabels("a", "a", "b"))).Collect(ctx); err == nil ||
		!strings.Contains(err.Error(), `"a" names two bins`) {
		t.Errorf("a label twice: %v", err)
	}

	// QCut's labels come from the data, and are a Categorical, as Polars' are, since
	// step 352.
	if got := cells(t, x.Select(c("x").QCut([]float64{0.5})), "x"); !strings.HasSuffix(got, " Categorical") {
		t.Errorf("QCut: %s", got)
	}
}
