package ursus_test

import (
	"strings"
	"testing"

	"ursus"
	"ursus/internal/plan"
	"ursus/ursustest"
)

// The projection-pushdown arms, deferred in steps 11, 12, 13 and 14 — and the list
// grew at every one of them.
//
// The golden corpus was nearly blind to this work: before step 15 exactly ONE file
// moved when all nine arms landed. So the goldens below are the visible artefact
// rather than an afterthought, and the behavioural test beside them is what stops a
// wrong arm from merely changing the snapshot.

// TestGoldenProjectionArms pins the scan's projection line for each new arm.
func TestGoldenProjectionArms(t *testing.T) {
	// AGGREGATE: reads region and price, so a six-column source must decode two.
	ursustest.AssertPlan(t,
		frame().GroupBy(ursus.Col("region")).Agg(ursus.Col("price").Sum().Alias("s")),
		"testdata/plans/push_aggregate.txt")

	// WITH_COLUMNS: the running schema means the walk is backwards. `d` is defined
	// from price and then read, so price is required and d is not.
	ursustest.AssertPlan(t,
		frame().
			WithColumns(ursus.Col("price").Mul(2.0).Alias("d")).
			Select(ursus.Col("id"), ursus.Col("d")),
		"testdata/plans/push_withcolumns.txt")

	// DISTINCT with a subset: safe to prune everything outside it.
	ursustest.AssertPlan(t,
		frame().Unique("region").Select(ursus.Col("region")),
		"testdata/plans/push_distinct_subset.txt")

	// TAIL: a pure pass-through that used to require every column.
	ursustest.AssertPlan(t,
		frame().Tail(2).Select(ursus.Col("id")),
		"testdata/plans/push_tail.txt")

	// ROW INDEX: it invents a column and reads none.
	ursustest.AssertPlan(t,
		frame().WithRowIndex("n", 0).Select(ursus.Col("n"), ursus.Col("id")),
		"testdata/plans/push_rowindex.txt")

	// REVERSE, whose predicate barrier also went away in this step.
	ursustest.AssertPlan(t,
		frame().Reverse().Filter(ursus.Col("price").Gt(5)).Select(ursus.Col("id")),
		"testdata/plans/push_reverse.txt")
}

// TestProjectionArmsNarrowTheScan asserts the OUTCOME rather than the snapshot, so a
// wrong arm cannot pass by rewriting a golden.
func TestProjectionArmsNarrowTheScan(t *testing.T) {
	for _, c := range []struct {
		name string
		lf   *ursus.LazyFrame
		want string
	}{
		{"aggregate",
			frame().GroupBy(ursus.Col("region")).Agg(ursus.Col("price").Sum()),
			"[region, price] (2/6 cols)"},
		// The aggregate's OUTPUT is named after an unrelated input column. This is
		// the only shape in which "the required set is REPLACED, not extended" is
		// observable: extending would add `note` to what the scan reads, even though
		// the note it refers to is the aggregate's output and not the input's column.
		{"aggregate_output_shadows_an_input_column",
			frame().GroupBy(ursus.Col("region")).
				Agg(ursus.Col("price").Sum().Alias("note")),
			"[region, price] (2/6 cols)"},
		{"with_columns",
			frame().WithColumns(ursus.Col("price").Mul(2.0).Alias("d")).
				Select(ursus.Col("id"), ursus.Col("d")),
			"[id, price] (2/6 cols)"},
		{"distinct_subset",
			frame().Unique("region").Select(ursus.Col("region")),
			"[region] (1/6 cols)"},
		{"tail", frame().Tail(2).Select(ursus.Col("id")), "[id] (1/6 cols)"},
		{"reverse", frame().Reverse().Select(ursus.Col("id")), "[id] (1/6 cols)"},
		{"row_index",
			frame().WithRowIndex("n", 0).Select(ursus.Col("n"), ursus.Col("id")),
			"[id] (1/6 cols)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			txt, err := c.lf.Explain(t.Context(), ursus.Optimized(true))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(txt, c.want) {
				t.Errorf("scan does not read %s:\n%s", c.want, txt)
			}
		})
	}
}

// TestDistinctPrunesNothingWithoutASubset is the one arm whose careless version
// returns wrong ROWS rather than a failed plan.
//
// Whole-row distinct dedups on every column, so pruning one collapses rows that were
// distinct — fewer rows out, no schema change, nothing to fail resolution, and
// TestPushdownSoundness cannot catch it because Expressions(*Distinct) returns an
// empty slice in exactly this case.
func TestDistinctPrunesNothingWithoutASubset(t *testing.T) {
	// Two rows that agree on `a` and differ on `b`: whole-row distinct keeps both.
	f := ursus.Frame(
		ursus.Values("a", []int64{1, 1}),
		ursus.Values("b", []int64{1, 2}),
	)
	got, err := f.Unique().Select(ursus.Col("a")).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got.Height() != 2 {
		t.Fatalf("%d rows, want 2 — pruning b under a whole-row distinct collapsed "+
			"two distinct rows into one", got.Height())
	}
	// And the plan really does still read b, or the row count above is a coincidence.
	txt, err := f.Unique().Select(ursus.Col("a")).Explain(t.Context(), ursus.Optimized(true))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(txt, "(2/2 cols)") {
		t.Errorf("a subset-less distinct must keep every column:\n%s", txt)
	}
}

// TestReverseCommutesWithFilter: the predicate arm, checked by result equality with
// the optimizer off rather than by reading the plan.
//
// Reverse does no comparison, so it is strictly weaker than Sort — to which the same
// argument already applied. It was a barrier for no reason from step 1 to step 15.
func TestReverseCommutesWithFilter(t *testing.T) {
	q := func() *ursus.LazyFrame {
		return frame().Reverse().Filter(ursus.Col("price").Gt(5)).
			Select(ursus.Col("id"), ursus.Col("price"))
	}
	on, err := q().Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	off, err := q().Collect(t.Context(), ursus.WithOptFlags(plan.NoFlags()))
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, on, off)

	// And the filter really did move, or the equality above proves nothing.
	txt, err := q().Explain(t.Context(), ursus.Optimized(true))
	if err != nil {
		t.Fatal(err)
	}
	if i, j := strings.Index(txt, "REVERSE"), strings.Index(txt, "FILTER"); i >= 0 && j >= 0 && j < i {
		t.Errorf("the filter is still above the reverse:\n%s", txt)
	}
}

// TestSlicePositionalNodesStayBarriers: the four nodes that must NOT join Reverse in
// the predicate rule. Each depends on WHICH rows precede a row, so moving a filter
// below one changes the answer.
func TestSlicePositionalNodesStayBarriers(t *testing.T) {
	for _, c := range []struct {
		name string
		lf   *ursus.LazyFrame
	}{
		{"tail", frame().Tail(3).Filter(ursus.Col("price").Gt(5))},
		{"slice", frame().Slice(1, 3).Filter(ursus.Col("price").Gt(5))},
		{"row_index", frame().WithRowIndex("n", 0).Filter(ursus.Col("n").Lt(uint32(3)))},
	} {
		t.Run(c.name, func(t *testing.T) {
			on, err := c.lf.Collect(t.Context(), ursus.WithVerify())
			if err != nil {
				t.Fatal(err)
			}
			off, err := c.lf.Collect(t.Context(), ursus.WithOptFlags(plan.NoFlags()))
			if err != nil {
				t.Fatal(err)
			}
			ursustest.AssertFrameEqual(t, on, off)
		})
	}
}
