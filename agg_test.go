package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// sales is the fixture for the analytics tests. `amount` has nulls, and region
// "apac" has ONLY nulls — the case that separates SQL's sum-of-nothing-is-NULL
// from Polars' sum-of-nothing-is-zero.
func sales() *ursus.LazyFrame {
	return ursus.Frame(
		ursus.Values("region", []string{"eu", "us", "eu", "apac", "us", "eu", "apac"}),
		ursus.Values("rep", []string{"ann", "bob", "cat", "dan", "eve", "ann", "fay"}),
		ursus.ValuesNullable("amount",
			[]float64{10, 20, 30, 0, 40, 50, 0},
			[]bool{true, true, true, false, true, true, false}),
		ursus.Values("qty", []int32{1, 2, 3, 4, 5, 6, 7}),
	)
}

func TestGroupByAgg(t *testing.T) {
	df, err := sales().
		// MaintainOrder is declared because the assertion below depends on it. It
		// happens to hold without the flag — this input is one batch and never spills,
		// so nothing reorders — but "happens to hold" is what the flag exists to stop
		// a test from relying on, and since step 12 an unordered group-by really can
		// come out partition-major.
		GroupBy(ursus.Col("region")).MaintainOrder().
		Agg(
			ursus.Col("amount").Sum().Alias("total"),
			ursus.Col("amount").Mean().Alias("avg"),
			ursus.Col("amount").Count().Alias("n_amounts"),
			ursus.Len().Alias("n_rows"),
			ursus.Col("rep").NUnique().Alias("reps"),
		).
		Collect(t.Context())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if got, want := df.Height(), 3; got != want {
		t.Fatalf("height = %d, want %d\n%s", got, want, df)
	}

	// Groups come out in first-appearance order: eu, us, apac — which is what
	// MaintainOrder above guarantees.
	regions, err := df.Column[string]("region")
	if err != nil {
		t.Fatal(err)
	}
	if got := regions.Values(); !equal(got, []string{"eu", "us", "apac"}) {
		t.Fatalf("regions = %v", got)
	}

	total, _ := df.Column[float64]("total")
	// eu: 10+30+50 = 90. us: 20+40 = 60. apac: both amounts are null.
	if v, ok := total.Get(0); !ok || v != 90 {
		t.Errorf("eu total = %v (valid %v), want 90", v, ok)
	}
	if v, ok := total.Get(1); !ok || v != 60 {
		t.Errorf("us total = %v (valid %v), want 60", v, ok)
	}
	// THE decision: sum over a group with no non-null values is NULL, not 0.
	// Returning 0 would make "no sales recorded" indistinguishable from
	// "sales totalling zero", irreversibly.
	if _, ok := total.Get(2); ok {
		t.Error("apac total must be NULL (all values null), not 0")
	}

	avg, _ := df.Column[float64]("avg")
	if v, _ := avg.Get(0); v != 30 {
		t.Errorf("eu mean = %v, want 30", v)
	}
	// Mean divides by the COUNT of non-null values, not the group size. us has
	// two non-null amounts out of two rows so this case does not discriminate;
	// TestMeanDividesByCount does.
	if _, ok := avg.Get(2); ok {
		t.Error("apac mean must be NULL, not NaN")
	}

	// Count(col) counts non-nulls; Len() counts rows. They differ for apac.
	nAmounts, _ := df.Column[uint64]("n_amounts")
	nRows, _ := df.Column[uint64]("n_rows")
	if v, _ := nAmounts.Get(2); v != 0 {
		t.Errorf("apac count(amount) = %d, want 0", v)
	}
	if v, _ := nRows.Get(2); v != 2 {
		t.Errorf("apac len() = %d, want 2", v)
	}

	// eu has reps ann, cat, ann -> 2 distinct.
	reps, _ := df.Column[uint64]("reps")
	if v, _ := reps.Get(0); v != 2 {
		t.Errorf("eu n_unique(rep) = %d, want 2", v)
	}
}

// TestMeanDividesByCount is the case that separates a correct mean from the
// classic bug of dividing by the group size. It is near-undetectable without a
// group whose null count differs from zero.
func TestMeanDividesByCount(t *testing.T) {
	lf := ursus.Frame(
		ursus.Values("g", []string{"a", "a", "a"}),
		ursus.ValuesNullable("v", []float64{1, 0, 3}, []bool{true, false, true}),
	)
	df, err := lf.GroupBy(ursus.Col("g")).
		Agg(ursus.Col("v").Mean().Alias("m")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	m, _ := df.Column[float64]("m")
	// (1+3)/2 = 2. Dividing by the group size would give (1+3)/3 = 1.333…
	if v, ok := m.Get(0); !ok || v != 2 {
		t.Errorf("mean = %v (valid %v), want 2 — dividing by len would give %v",
			v, ok, 4.0/3.0)
	}
}

// TestIntegerSumWidensToInt128 checks that an integer sum cannot silently
// overflow, which is the reason Int128 exists at all.
func TestIntegerSumWidensToInt128(t *testing.T) {
	const maxI64 = int64(9223372036854775807)
	lf := ursus.Frame(
		ursus.Values("g", []string{"a", "a", "a"}),
		ursus.Values("v", []int64{maxI64, maxI64, maxI64}),
	)
	df, err := lf.GroupBy(ursus.Col("g")).
		Agg(ursus.Col("v").Sum().Alias("total")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if got, want := df.Schema().String(), "{g: String!, total: Int128}"; got != want {
		t.Errorf("schema = %s, want %s", got, want)
	}

	total, err := df.Column[ursus.Int128Value]("total")
	if err != nil {
		t.Fatal(err)
	}
	v, _ := total.Get(0)
	// 3 * (2^63 - 1) = 27670116110564327421, which is well past int64 and would
	// have wrapped to a negative number in a 64-bit accumulator.
	if got, want := v.String(), "27670116110564327421"; got != want {
		t.Errorf("sum = %s, want %s", got, want)
	}
}

func TestGlobalAggregate(t *testing.T) {
	df, err := sales().
		GroupBy().
		Agg(
			ursus.Len().Alias("rows"),
			ursus.Col("amount").Sum().Alias("total"),
			ursus.Col("qty").Max().Alias("biggest"),
		).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 1 {
		t.Fatalf("global aggregate produced %d rows, want 1\n%s", df.Height(), df)
	}
	rows, _ := df.Column[uint64]("rows")
	if v, _ := rows.Get(0); v != 7 {
		t.Errorf("rows = %d, want 7", v)
	}
	biggest, _ := df.Column[int32]("biggest")
	if v, _ := biggest.Get(0); v != 7 {
		t.Errorf("max(qty) = %d, want 7", v)
	}
}

// TestGlobalAggregateOverEmptyInput is the corner a hash table gets wrong by
// default: no row ever creates a group, so the natural implementation emits
// nothing. SQL says `count(*)` of an empty table is 0, i.e. exactly one row.
func TestGlobalAggregateOverEmptyInput(t *testing.T) {
	df, err := sales().
		Filter(ursus.Col("qty").Gt(1000)). // matches nothing
		GroupBy().
		Agg(ursus.Len().Alias("rows"), ursus.Col("amount").Sum().Alias("total")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 1 {
		t.Fatalf("global aggregate over empty input produced %d rows, want 1", df.Height())
	}
	rows, _ := df.Column[uint64]("rows")
	if v, _ := rows.Get(0); v != 0 {
		t.Errorf("count over empty = %d, want 0", v)
	}
	total, _ := df.Column[float64]("total")
	if _, ok := total.Get(0); ok {
		t.Error("sum over empty must be NULL, not 0")
	}
}

// TestGroupByOverEmptyInput: with keys, an empty input produces no groups.
func TestGroupByOverEmptyInput(t *testing.T) {
	n, err := sales().
		Filter(ursus.Col("qty").Gt(1000)).
		GroupBy(ursus.Col("region")).
		Agg(ursus.Len().Alias("rows")).
		Count(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("group-by over empty input produced %d rows, want 0", n)
	}
}

// TestCompoundAggregate exercises the two-phase lowering: arithmetic OVER
// aggregates, evaluated by a Project on top of the sink.
func TestCompoundAggregate(t *testing.T) {
	df, err := sales().
		GroupBy(ursus.Col("region")).
		Agg(ursus.Col("amount").Sum().Div(ursus.Col("amount").Count()).Alias("per")).
		Collect(t.Context())
	if err != nil {
		t.Fatalf("compound aggregate: %v", err)
	}
	per, _ := df.Column[float64]("per")
	// eu: 90/3 = 30, which is also the mean — the point is that it was computed
	// from two separate aggregates rather than by the mean accumulator.
	if v, ok := per.Get(0); !ok || v != 30 {
		t.Errorf("eu sum/count = %v (valid %v), want 30", v, ok)
	}
}

func TestSort(t *testing.T) {
	df, err := sales().
		Sort(ursus.Desc(ursus.Col("qty"))).
		Select(ursus.Col("qty")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	qty, _ := df.Column[int32]("qty")
	if got := qty.Values(); !equal(got, []int32{7, 6, 5, 4, 3, 2, 1}) {
		t.Errorf("descending sort = %v", got)
	}
}

// TestSortNullPlacement pins the decision that null placement is independent of
// direction: NullsLast means last in the OUTPUT, ascending or descending.
func TestSortNullPlacement(t *testing.T) {
	cases := []struct {
		name string
		key  ursus.SortKey
		want []bool // per output row: is it null?
	}{
		{"asc, nulls first (default)", ursus.Asc(ursus.Col("amount")),
			[]bool{true, true, false, false, false, false, false}},
		{"asc, nulls last", ursus.Asc(ursus.Col("amount")).NullsLast(),
			[]bool{false, false, false, false, false, true, true}},
		{"desc, nulls first (default)", ursus.Desc(ursus.Col("amount")),
			[]bool{true, true, false, false, false, false, false}},
		{"desc, nulls last", ursus.Desc(ursus.Col("amount")).NullsLast(),
			[]bool{false, false, false, false, false, true, true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			df, err := sales().Sort(c.key).Select(ursus.Col("amount")).Collect(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			s, _ := df.Column[float64]("amount")
			for i, wantNull := range c.want {
				_, ok := s.Get(i)
				if ok == wantNull {
					t.Errorf("row %d: null=%v, want null=%v\n%s", i, !ok, wantNull, df)
				}
			}
		})
	}
}

func TestSortStableAndMultiKey(t *testing.T) {
	df, err := sales().
		Sort(ursus.Asc(ursus.Col("region")), ursus.Desc(ursus.Col("qty"))).
		Select(ursus.Col("region"), ursus.Col("qty")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	region, _ := df.Column[string]("region")
	qty, _ := df.Column[int32]("qty")
	if got := region.Values(); !equal(got, []string{"apac", "apac", "eu", "eu", "eu", "us", "us"}) {
		t.Errorf("regions = %v", got)
	}
	if got := qty.Values(); !equal(got, []int32{7, 4, 6, 3, 1, 5, 2}) {
		t.Errorf("qty within region = %v", got)
	}
}

// TestSortTopKMatchesFullSort: the Head-after-Sort shape must give the same rows
// whether or not a top-k substitution happens.
func TestSortTopKMatchesFullSort(t *testing.T) {
	full, err := sales().Sort(ursus.Desc(ursus.Col("qty"))).
		Select(ursus.Col("qty")).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	head, err := sales().Sort(ursus.Desc(ursus.Col("qty"))).Head(3).
		Select(ursus.Col("qty")).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	f, _ := full.Column[int32]("qty")
	h, _ := head.Column[int32]("qty")
	if !equal(h.Values(), f.Values()[:3]) {
		t.Errorf("head = %v, want %v", h.Values(), f.Values()[:3])
	}
}

func TestWithColumns(t *testing.T) {
	df, err := sales().
		WithColumns(
			ursus.Col("qty").Mul(2).Alias("double"),
			ursus.Col("double").Add(1).Alias("plus"), // references the column just added
			ursus.Col("qty").Mul(10),                 // replaces qty in place
		).
		Collect(t.Context())
	if err != nil {
		t.Fatalf("WithColumns: %v", err)
	}

	// qty was replaced IN PLACE, so it keeps its original position (index 3).
	if got, want := df.Schema().Names(),
		[]string{"region", "rep", "amount", "qty", "double", "plus"}; !equal(got, want) {
		t.Errorf("columns = %v, want %v", got, want)
	}

	// qty was Int32; multiplying by an untyped integer constant promotes to Int64,
	// so the replacement column has a different type from the one it replaced.
	// That is correct — WithColumns replaces by NAME, not by type.
	if got := df.Schema().Field(3).Type.String(); got != "Int64" {
		t.Errorf("replaced qty type = %s, want Int64", got)
	}
	qty, err := df.Column[int64]("qty")
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := qty.Get(0); v != 10 {
		t.Errorf("qty[0] = %d, want 10 (replaced)", v)
	}
	plus, err := df.Column[int64]("plus")
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := plus.Get(0); v != 3 { // double = 1*2 = 2, plus = 3
		t.Errorf("plus[0] = %d, want 3", v)
	}
}

func TestUnique(t *testing.T) {
	n, err := sales().Unique("region").Count(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("unique regions = %d, want 3", n)
	}

	// Whole-row distinct: every row here is distinct.
	n, err = sales().Unique().Count(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if n != 7 {
		t.Errorf("whole-row unique = %d, want 7", n)
	}
}

// TestUniqueKeepsFirstRow: subset-distinct must keep the FIRST row for each key,
// including the columns it did not compare, rather than an arbitrary one.
func TestUniqueKeepsFirstRow(t *testing.T) {
	df, err := sales().Unique("region").
		Select(ursus.Col("region"), ursus.Col("rep")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	rep, _ := df.Column[string]("rep")
	if got := rep.Values(); !equal(got, []string{"ann", "bob", "dan"}) {
		t.Errorf("reps = %v, want the first rep in each region", got)
	}
}

// TestAggregateRejectedInSelect is the fix for a defect that would have shipped:
// rejectAggregate was documented as guarding Select and was never called from it,
// so this query reached the evaluator and died as "a bug in ursus".
func TestAggregateRejectedInSelect(t *testing.T) {
	_, err := sales().Select(ursus.Col("qty").Sum()).Collect(t.Context())
	if err == nil {
		t.Fatal("Select over an aggregate must be rejected")
	}
	msg := err.Error()
	for _, want := range []string{"aggregate expression is not allowed", "GroupBy"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q:\n%s", want, msg)
		}
	}
}

func TestNestedAggregateRejected(t *testing.T) {
	_, err := sales().
		GroupBy(ursus.Col("region")).
		Agg(ursus.Col("qty").Sum().Mean()).
		Collect(t.Context())
	if err == nil {
		t.Fatal("an aggregate of an aggregate must be rejected")
	}
}

func TestNonAggregateInAggRejected(t *testing.T) {
	_, err := sales().
		GroupBy(ursus.Col("region")).
		Agg(ursus.Col("qty").Sum().Add(ursus.Col("qty"))).
		Collect(t.Context())
	if err == nil {
		t.Fatal("mixing an aggregate with a bare column must be rejected")
	}
	if !strings.Contains(err.Error(), "outside any aggregate") {
		t.Errorf("message should name the offending column:\n%s", err)
	}
}

// TestAggregateBatchSizeInvariance: batch size is a tuning knob, never a semantic
// one. This is the cheapest test that catches per-batch state leaks in a sink.
func TestAggregateBatchSizeInvariance(t *testing.T) {
	var ref string
	for _, size := range []int{1, 2, 3, 7, 64, 8192} {
		df, err := sales().
			GroupBy(ursus.Col("region")).
			Agg(
				ursus.Col("amount").Sum().Alias("total"),
				ursus.Col("amount").Mean().Alias("avg"),
				ursus.Col("qty").Min().Alias("lo"),
				ursus.Col("qty").Max().Alias("hi"),
				ursus.Col("rep").First().Alias("first_rep"),
				ursus.Col("rep").Last().Alias("last_rep"),
				ursus.Col("rep").NUnique().Alias("reps"),
				ursus.Len().Alias("n"),
			).
			Collect(t.Context(), ursus.WithBatchSize(size), ursus.WithVerify())
		if err != nil {
			t.Fatalf("batch size %d: %v", size, err)
		}
		got := df.String()
		if ref == "" {
			ref = got
			continue
		}
		if got != ref {
			t.Errorf("batch size %d changed the result:\n%s\nwant:\n%s", size, got, ref)
		}
	}
	t.Logf("aggregate result:\n%s", ref)
}

func TestSortBatchSizeInvariance(t *testing.T) {
	var ref string
	for _, size := range []int{1, 2, 3, 7, 8192} {
		df, err := sales().
			Sort(ursus.Asc(ursus.Col("region")), ursus.Desc(ursus.Col("qty"))).
			Collect(t.Context(), ursus.WithBatchSize(size))
		if err != nil {
			t.Fatalf("batch size %d: %v", size, err)
		}
		got := df.String()
		if ref == "" {
			ref = got
			continue
		}
		if got != ref {
			t.Errorf("batch size %d changed the sort:\n%s\nwant:\n%s", size, got, ref)
		}
	}
}

// TestMinMaxAgreeWithSort ties the extremum accumulator to the sort comparator.
// Both must use the TOTAL order; a Min that reached for the IEEE comparison
// kernels would disagree the moment a NaN appeared.
func TestMinMaxAgreeWithSort(t *testing.T) {
	agg, err := sales().
		GroupBy().
		Agg(ursus.Col("qty").Min().Alias("lo"), ursus.Col("qty").Max().Alias("hi")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	sorted, err := sales().
		Sort(ursus.Asc(ursus.Col("qty")).NullsLast()).
		Select(ursus.Col("qty")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	lo, _ := agg.Column[int32]("lo")
	hi, _ := agg.Column[int32]("hi")
	s, _ := sorted.Column[int32]("qty")
	vals := s.Values()

	if a, _ := lo.Get(0); a != vals[0] {
		t.Errorf("min = %d, but sorted first = %d", a, vals[0])
	}
	if a, _ := hi.Get(0); a != vals[len(vals)-1] {
		t.Errorf("max = %d, but sorted last = %d", a, vals[len(vals)-1])
	}
}
