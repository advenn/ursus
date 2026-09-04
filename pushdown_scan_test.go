package ursus_test

import (
	"strings"
	"testing"

	"ursus"
	"ursus/dtype"
	"ursus/internal/bitmap"
	"ursus/internal/data"
	"ursus/internal/expr"
	"ursus/internal/plan"
	"ursus/internal/source/memsrc"
	"ursus/internal/source/testsrc"
)

// The tests in this file cover the branch of predicatePushdown that decides what a
// SOURCE may be trusted to do. Until step 3 that branch was unreachable: memsrc
// declared no predicate support, so no test could execute it, and it was wrong.
//
// Everything here goes through internal/source/testsrc, which claims a capability
// and then behaves according to it — including badly, which is the point.

// scanFixture is two columns over three batches. Multiple batches matter: a source
// that filters per batch and an engine that filters per batch have to agree on what
// an empty batch means, and a single-batch fixture never asks the question.
func scanFixture(t *testing.T) *memsrc.Source {
	t.Helper()

	schema, err := dtype.NewSchema(
		dtype.Field{Name: "qty", Type: dtype.Int64},
		dtype.Field{Name: "region", Type: dtype.String},
	)
	if err != nil {
		t.Fatal(err)
	}

	var batches []*data.Batch
	for _, part := range [][]int64{{1, 2, 3}, {4, 5}, {6, 7}} {
		regions := make([]string, len(part))
		for i, v := range part {
			regions[i] = [...]string{"eu", "us", "apac"}[v%3]
		}
		b, err := data.NewBatch(schema, []*data.Column{
			data.NewFixed("qty", dtype.Int64, part, bitmap.AllSet(len(part))),
			data.NewString("region", regions, bitmap.AllSet(len(part))),
		})
		if err != nil {
			t.Fatal(err)
		}
		batches = append(batches, b)
	}

	src, err := memsrc.New(schema, batches...)
	if err != nil {
		t.Fatal(err)
	}
	return src
}

// qtyGt returns a keep function matching `qty > n`, for the Exact source. It has
// to agree with the predicate the query filters on; that agreement is what
// "Exact" means and stating it in the test is the honest way to express it.
func qtyGt(n int64) func(*data.Batch, int) bool {
	return func(b *data.Batch, row int) bool {
		c, ok := b.ByName("qty")
		if !ok {
			return false
		}
		s, err := data.TypedColumn[int64](c)
		if err != nil {
			return false
		}
		v, valid := s.Get(row)
		return valid && v > n
	}
}

// bothWays runs a query with the optimizer on and off and requires identical
// results. This is the only mechanism that catches a wrongly-moved filter: the
// optimizer's Verify mode compares schemas, and moving a filter does not change
// the schema — it changes which rows come back.
func bothWays(t *testing.T, lf func() *ursus.LazyFrame) string {
	t.Helper()

	on, err := lf().Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatalf("optimized: %v", err)
	}
	off, err := lf().Collect(t.Context(), ursus.WithOptFlags(plan.NoFlags()))
	if err != nil {
		t.Fatalf("unoptimized: %v", err)
	}
	if on.String() != off.String() {
		p, _ := lf().Explain(t.Context())
		t.Fatalf("the optimizer changed the result\n optimized:\n%s\n unoptimized:\n%s\n plan:\n%s",
			on, off, p)
	}
	return on.String()
}

// TestInexactScanKeepsTheFilter is the regression test for the bug this step
// exists to fix.
//
// An inexact source is one that can SKIP data using a predicate without being able
// to SATISFY it — Parquet row-group pruning is the real instance. The optimizer
// must push the conjunct into the scan AND leave the Filter above it. The version
// of pushIntoScan before step 3 returned a bare Scan, so every row the source
// declined to filter reached the user.
//
// testsrc.NewInexact filters nothing at all, which is the largest legal superset
// and therefore the strongest form of this test.
func TestInexactScanKeepsTheFilter(t *testing.T) {
	src := testsrc.NewInexact(scanFixture(t))
	q := func() *ursus.LazyFrame {
		return ursus.Scan(src).Filter(ursus.Col("qty").Gt(3))
	}

	got := bothWays(t, q)
	if strings.Count(got, "\n") == 0 {
		t.Fatalf("expected rows, got:\n%s", got)
	}

	df, err := q().Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 4 { // qty 4,5,6,7
		t.Errorf("got %d rows, want 4 — the Filter above the inexact scan was dropped\n%s",
			df.Height(), df)
	}

	// The plan must show BOTH halves: the conjunct offered to the source, and the
	// Filter that still enforces it.
	p, err := q().Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, "predicate: [") {
		t.Errorf("the conjunct did not reach the scan:\n%s", p)
	}
	if !strings.Contains(p, "FILTER") {
		t.Errorf("the Filter must remain above an INEXACT scan:\n%s", p)
	}
}

// TestExactScanRemovesTheFilter is the other half. A source that genuinely applies
// the predicate should not be double-filtered, and the plan should say so.
func TestExactScanRemovesTheFilter(t *testing.T) {
	src := testsrc.NewExact(scanFixture(t), qtyGt(3))
	q := func() *ursus.LazyFrame {
		return ursus.Scan(src).Filter(ursus.Col("qty").Gt(3))
	}

	bothWays(t, q)

	p, err := q().Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p, "FILTER") {
		t.Errorf("an EXACT scan should have absorbed the Filter:\n%s", p)
	}
	if !strings.Contains(p, "predicate: [") {
		t.Errorf("the conjunct did not reach the scan:\n%s", p)
	}
}

// TestExactScanReadsColumnsItDoesNotEmit is the read-set/emit-set case.
//
//	Filter(qty > 3).Select(region)   with qty > 3 EXACT
//
// leaves nothing in the plan mentioning qty, so projection pushdown narrows the
// scan to [region] — yet the source cannot evaluate its predicate without reading
// qty. ScanSpec.ReadSet is what makes that derivable rather than something each
// reader has to remember.
func TestExactScanReadsColumnsItDoesNotEmit(t *testing.T) {
	src := testsrc.NewExact(scanFixture(t), qtyGt(3))
	q := func() *ursus.LazyFrame {
		return ursus.Scan(src).
			Filter(ursus.Col("qty").Gt(3)).
			Select(ursus.Col("region"))
	}

	bothWays(t, q)

	df, err := q().Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if df.Width() != 1 {
		t.Errorf("scan emitted %d columns, want 1 — qty is read, not emitted\n%s",
			df.Width(), df)
	}
	if df.Height() != 4 {
		t.Errorf("got %d rows, want 4\n%s", df.Height(), df)
	}

	// The reader must have been asked for region only, and Explain must admit that
	// the scan nevertheless reads two columns.
	specs := src.Specs()
	if len(specs) == 0 {
		t.Fatal("source was never opened")
	}
	last := specs[len(specs)-1]
	if len(last.Projection) != 1 || last.Projection[0] != "region" {
		t.Errorf("projection = %v, want [region]", last.Projection)
	}
	if len(last.Predicate) != 1 {
		t.Errorf("predicate = %v, want one conjunct", last.Predicate)
	}

	p, err := q().Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, "reads: [qty, region]") {
		t.Errorf("Explain must show the read set when it differs from the projection:\n%s", p)
	}
}

// TestMixedConjunctsSplit: capability is per-conjunct, which is the reason
// Pushdown is not a bool. One conjunct moves into the scan, the other stays above.
func TestMixedConjunctsSplit(t *testing.T) {
	// Classify `qty > 3` Exact and anything else Unsupported.
	classify := func(preds []expr.Node) []plan.Pushdown {
		out := make([]plan.Pushdown, len(preds))
		for i, p := range preds {
			if strings.Contains(p.String(), `col("qty")`) {
				out[i] = plan.Exact
			}
		}
		return out
	}
	src := testsrc.New(scanFixture(t), classify, qtyGt(3))

	q := func() *ursus.LazyFrame {
		return ursus.Scan(src).
			Filter(ursus.Col("qty").Gt(3)).
			Filter(ursus.Col("region").Ne("eu"))
	}

	bothWays(t, q)

	p, err := q().Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, `predicate: [(col("qty") > lit(3))]`) {
		t.Errorf("the Exact conjunct should be the only one on the scan:\n%s", p)
	}
	if !strings.Contains(p, `FILTER [(col("region") != lit("eu"))]`) {
		t.Errorf("the Unsupported conjunct must stay above:\n%s", p)
	}
}

// TestClassifyPredicatesFailsClosed covers the contract violations.
//
// Unsupported is the zero value so that every accident — a short slice, a value
// from an enum this build does not know — costs a slower plan rather than a wrong
// one. That property is only real if something checks it, because the natural way
// to write the helper (trust the slice, index it) has neither guard.
func TestClassifyPredicatesFailsClosed(t *testing.T) {
	cases := []struct {
		name     string
		classify func([]expr.Node) []plan.Pushdown
	}{
		{"nil slice", func([]expr.Node) []plan.Pushdown { return nil }},
		{"short slice", func(p []expr.Node) []plan.Pushdown {
			// One entry short, and every entry claiming Exact — the shape that
			// tempts an implementation to index what it was given and trust it.
			out := make([]plan.Pushdown, max(0, len(p)-1))
			for i := range out {
				out[i] = plan.Exact
			}
			return out
		}},
		{"long slice", func(p []expr.Node) []plan.Pushdown {
			out := make([]plan.Pushdown, len(p)+1)
			for i := range out {
				out[i] = plan.Exact
			}
			return out
		}},
		{"value outside the enum", func(p []expr.Node) []plan.Pushdown {
			out := make([]plan.Pushdown, len(p))
			for i := range out {
				out[i] = plan.Pushdown(200)
			}
			return out
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// keep is deliberately "drop everything": if the engine wrongly trusted
			// the claim and removed its Filter, the query would return no rows.
			src := testsrc.New(scanFixture(t), c.classify,
				func(*data.Batch, int) bool { return false })

			q := func() *ursus.LazyFrame {
				return ursus.Scan(src).Filter(ursus.Col("qty").Gt(3))
			}
			bothWays(t, q)

			df, err := q().Collect(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if df.Height() != 4 {
				t.Errorf("got %d rows, want 4 — a malformed classification was trusted\n%s",
					df.Height(), df)
			}

			p, err := q().Explain(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(p, "predicate: [") {
				t.Errorf("nothing should have been pushed into the scan:\n%s", p)
			}
			if !strings.Contains(p, "FILTER") {
				t.Errorf("the Filter must remain:\n%s", p)
			}
		})
	}
}

// TestLimitReachesTheScan proves limit pushdown does something. Head(2) on a large
// file is the first thing anyone types, and without this the reader still reads
// the whole thing.
func TestLimitReachesTheScan(t *testing.T) {
	src := testsrc.New(scanFixture(t), nil, nil)
	lf := ursus.Scan(src).Head(2)

	df, err := lf.Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 2 {
		t.Fatalf("got %d rows, want 2\n%s", df.Height(), df)
	}

	specs := src.Specs()
	if len(specs) == 0 {
		t.Fatal("source was never opened")
	}
	if got := specs[len(specs)-1].MaxRows; got != 2 {
		t.Errorf("ScanSpec.MaxRows = %d, want 2 — the limit never reached the reader", got)
	}

	p, err := lf.Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, "max rows: 2") {
		t.Errorf("Explain should show the pushed limit:\n%s", p)
	}
}

// TestLimitIsNotPushedThroughAnInexactFilter is the guard that makes limit
// pushdown safe, and it is the case a rule written without this test would get
// wrong.
//
//	Limit(2, Filter(qty > 3, Scan{INEXACT}))
//
// If the limit reached the scan, the source would return 2 rows of its SUPERSET
// (qty 1 and 2), the Filter would reject both, and Head(2) would return NOTHING
// instead of qty 4 and 5. Requiring the Limit's child to be a Scan rules this out
// structurally — the residual Filter of an inexact pushdown is what sits between
// them.
func TestLimitIsNotPushedThroughAnInexactFilter(t *testing.T) {
	src := testsrc.NewInexact(scanFixture(t))
	lf := ursus.Scan(src).Filter(ursus.Col("qty").Gt(3)).Head(2)

	df, err := lf.Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 2 {
		t.Errorf("got %d rows, want 2 (qty 4,5)\n%s", df.Height(), df)
	}

	for _, s := range src.Specs() {
		if s.MaxRows != 0 {
			t.Errorf("MaxRows = %d reached an inexact scan through a residual Filter; "+
				"the source would return a truncated superset and Head would under-deliver",
				s.MaxRows)
		}
	}
}
