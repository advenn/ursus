package ursus_test

// The last three projection-pushdown arms: AsOfJoin, MergeSorted and HStack.
//
// All three fell to the rule's conservative default and read EVERY column of their
// input, which that default's doc calls "correct-but-unoptimized the day they are
// added". It stayed that way for six steps.
//
// Every test here is a rule-on / rule-off DIFFERENTIAL, because the failure mode
// that matters is silent: a pushdown that errors is loud, one that changes rows is
// not. That is the discipline step 48 established for this rule.

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// scanOf returns the projection line of the optimized plan's i-th scan.
func scanLines(t *testing.T, lf *ursus.LazyFrame) []string {
	t.Helper()
	p, err := lf.Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for line := range strings.SplitSeq(p, "\n") {
		if strings.Contains(line, "projection:") {
			out = append(out, strings.TrimSpace(line))
		}
	}
	if len(out) == 0 {
		t.Fatalf("no projection line in:\n%s", p)
	}
	return out
}

// assertSameWithAndWithout is the differential every arm gets.
func assertSameWithAndWithout(t *testing.T, minRows int, q func() *ursus.LazyFrame) {
	t.Helper()
	on, err := q().Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatalf("optimized: %v", err)
	}
	off, err := q().Collect(t.Context(), ursus.WithOptFlags(noProjectionPushdown()))
	if err != nil {
		t.Fatalf("unoptimized: %v", err)
	}
	// Anti-vacuity: two empty frames agree whatever the rule did.
	if on.Height() < minRows {
		t.Fatalf("got %d rows, want at least %d — comparing empty frames proves "+
			"nothing", on.Height(), minRows)
	}
	ursustest.AssertFrameEqual(t, on, off, ursustest.CheckNullability())
}

func asofSides() (*ursus.LazyFrame, *ursus.LazyFrame) {
	l := ursus.Frame(
		ursus.Values("k", []int64{10, 20, 30}),
		ursus.Values("lv", []int64{1, 2, 3}),
		ursus.Values("lx", []int64{7, 8, 9}),
	)
	r := ursus.Frame(
		ursus.Values("k", []int64{8, 20, 25}),
		ursus.Values("rv", []int64{80, 90, 95}),
		ursus.Values("rx", []int64{5, 6, 7}),
	)
	return l, r
}

// TestAsOfJoinPrunesBothSides. The arm borrows the equi-join's analysis through
// asJoin(), so what needs its own proof is that the ORDERING KEY survives when
// nobody selects it, and that the node is still an as-of join afterwards.
func TestAsOfJoinPrunesBothSides(t *testing.T) {
	q := func() *ursus.LazyFrame {
		l, r := asofSides()
		return l.JoinAsOf(r, ursus.AsOfOn(ursus.Col("k"))).
			Select(ursus.Col("lv"), ursus.Col("rv"))
	}

	for i, got := range scanLines(t, q()) {
		if strings.Contains(got, "lx") || strings.Contains(got, "rx") {
			t.Errorf("scan %d still reads an unselected column: %s", i, got)
		}
		// The key is read even though the Select never names it.
		if !strings.Contains(got, "k") {
			t.Errorf("scan %d dropped the as-of key: %s", i, got)
		}
	}
	assertSameWithAndWithout(t, 3, q)
}

// TestAsOfJoinStaysAnAsOfJoin is the one an identical schema would hide.
//
// pushdownJoin ends in j.WithChildren, which returns a *Join. Reusing it wholesale
// would have swapped the AsOfJoin for an equi-join: the schema is the same, so only
// the VALUES differ — an equi-join finds no match for key 10 or 30, while backward
// as-of matches both.
func TestAsOfJoinStaysAnAsOfJoin(t *testing.T) {
	l, r := asofSides()
	df, err := l.JoinAsOf(r, ursus.AsOfOn(ursus.Col("k"))).
		Select(ursus.Col("k"), ursus.Col("rv")).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 3 {
		t.Fatalf("got %d rows, want 3 — an as-of join keeps every left row\n%s",
			df.Height(), df)
	}
	// Key 10 has no exact match; backward as-of takes 8's value. An equi-join
	// would leave it null.
	if v, ok, err := df.At[int64](0, "rv"); err != nil {
		t.Fatal(err)
	} else if !ok || v != 80 {
		t.Errorf("row 0 rv = %v (present %v), want 80 — nearest-match semantics "+
			"were lost, which an equi-join with the same schema would hide", v, ok)
	}
}

// sortedSides builds two frames with identical schemas, which is what
// MergeSorted requires, and an extra column nobody selects.
func sortedSides() (*ursus.LazyFrame, *ursus.LazyFrame) {
	a := ursus.Frame(
		ursus.Values("k", []int64{1, 3, 5}),
		ursus.Values("v", []int64{10, 30, 50}),
		ursus.Values("x", []int64{100, 300, 500}),
	)
	b := ursus.Frame(
		ursus.Values("k", []int64{2, 4, 6}),
		ursus.Values("v", []int64{20, 40, 60}),
		ursus.Values("x", []int64{200, 400, 600}),
	)
	return a, b
}

// TestMergeSortedPrunesSymmetrically. Its Schema requires both sides Equal
// exactly, so the arm must prune them identically or not at all.
func TestMergeSortedPrunesSymmetrically(t *testing.T) {
	q := func() *ursus.LazyFrame {
		a, b := sortedSides()
		return a.MergeSorted(b, "k").Select(ursus.Col("k"), ursus.Col("v"))
	}

	lines := scanLines(t, q())
	if len(lines) != 2 {
		t.Fatalf("want two scans, got %d: %v", len(lines), lines)
	}
	if lines[0] != lines[1] {
		t.Errorf("the two sides pruned differently, which MergeSorted.Schema "+
			"refuses:\n left:  %s\n right: %s", lines[0], lines[1])
	}
	for _, got := range lines {
		if strings.Contains(got, "x") {
			t.Errorf("still reads the unselected column: %s", got)
		}
	}
	assertSameWithAndWithout(t, 6, q)
}

// TestMergeSortedKeepsItsKey. Schema() reads Key whether or not anyone selected
// it, so a set built from `required` alone fails with `unknown column`.
func TestMergeSortedKeepsItsKey(t *testing.T) {
	q := func() *ursus.LazyFrame {
		a, b := sortedSides()
		return a.MergeSorted(b, "k").Select(ursus.Col("v"))
	}
	if _, err := q().Explain(t.Context()); err != nil {
		t.Fatalf("the key must survive pruning even when unselected: %v", err)
	}
	assertSameWithAndWithout(t, 6, q)
}

// TestMergeSortedRefusesToDesynchronise is the arm's self-check.
//
// One side is a whole-row Distinct, whose own arm must require everything. Pruning
// the other side alone would leave [k,v] against [k,v,x] and MergeSorted.Schema
// would fail — AFTER resolution succeeded, which is defect P2's signature.
//
// The user would see a KindSchema "the two frames have different schemas", i.e. the
// optimizer desynchronising matching frames and then blaming the user. And the
// backstop is thinner than it looks: Optimizer.Verify is off in the golden
// inventory and in Explain, so a broken plan renders a clean golden file.
func TestMergeSortedRefusesToDesynchronise(t *testing.T) {
	q := func() *ursus.LazyFrame {
		a, b := sortedSides()
		return a.MergeSorted(b.Unique(), "k").Select(ursus.Col("k"), ursus.Col("v"))
	}

	if _, err := q().Explain(t.Context()); err != nil {
		t.Fatalf("the arm must fall back rather than desynchronise the sides: %v", err)
	}
	assertSameWithAndWithout(t, 6, q)
}

// TestHStackPrunesEachChildByName. The children own disjoint names, so the split
// is by membership in each child's schema.
func TestHStackPrunesEachChildByName(t *testing.T) {
	q := func() *ursus.LazyFrame {
		left := ursus.Frame(
			ursus.Values("k", []int64{1, 2}),
			ursus.Values("ka", []int64{3, 4}),
			ursus.Values("kb", []int64{5, 6}),
		)
		right := ursus.Frame(
			ursus.Values("hv", []string{"x", "y"}),
			ursus.Values("ha", []string{"p", "q"}),
			ursus.Values("hb", []string{"s", "t"}),
		)
		return left.HStack(right).Select(ursus.Col("k"), ursus.Col("hv"))
	}

	for i, got := range scanLines(t, q()) {
		for _, unwanted := range []string{"kb", "hb"} {
			if strings.Contains(got, unwanted) {
				t.Errorf("scan %d still reads %s: %s", i, unwanted, got)
			}
		}
	}
	assertSameWithAndWithout(t, 2, q)
}

// TestHStackReadsAnUnselectedChildForItsHeight.
//
// HStack pairs rows POSITIONALLY, and hstackOp derives the output height from its
// children, erroring when they disagree. So a child nobody selects from must still
// be read — the arm requires its first column rather than none.
//
// Without that the need set is empty, pushdownScan leaves the projection nil, and
// nil means "every column": the prune silently becomes a no-op that still reports
// changed. The rows are right either way, which is exactly why this needs the
// PLAN assertion and not just the differential.
func TestHStackReadsAnUnselectedChildForItsHeight(t *testing.T) {
	q := func() *ursus.LazyFrame {
		left := ursus.Frame(
			ursus.Values("k", []int64{1, 2}),
			ursus.Values("ka", []int64{3, 4}),
		)
		right := ursus.Frame(
			ursus.Values("hv", []string{"x", "y"}),
			ursus.Values("ha", []string{"p", "q"}),
			ursus.Values("hb", []string{"s", "t"}),
		)
		// Nothing from the right child is selected.
		return left.HStack(right).Select(ursus.Col("k"))
	}

	df, err := q().Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatalf("a child nobody selects from must still be read for its height: %v", err)
	}
	if df.Height() != 2 {
		t.Errorf("got %d rows, want 2 — the unselected child's height was lost\n%s",
			df.Height(), df)
	}

	lines := scanLines(t, q())
	if len(lines) != 2 {
		t.Fatalf("want two scans, got %d: %v", len(lines), lines)
	}
	// The right child reads exactly ONE column: enough for the height, no more.
	if strings.Contains(lines[1], "(3/3") || strings.Contains(lines[1], "*") {
		t.Errorf("the unselected child was not pruned at all — a nil projection "+
			"means every column, so this is a no-op reported as a change: %s",
			lines[1])
	}
	assertSameWithAndWithout(t, 2, q)
}
