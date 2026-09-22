package ursus_test

// Two udfs sharing a name are one computation.
//
// Four maps in the planner deduplicate expressions by their RENDERED form. A Go
// closure has no rendered form, so UDF.String renders the name instead — and two
// different closures under one name render identically, merge, and both results
// come from whichever was seen first. No error, right row count, right type.
//
// udf.go's own doc states this and does not enforce it. udf_test.go proves only the
// POSITIVE direction, that differently-named udfs stay apart; change its two names
// to one string and it returns 20 and 20.
//
// # These tests assert the DEFECT
//
// Every assertion below is of the wrong answer, measured. That is deliberate and it
// is how this project lands evidence: a list of defects written down is evidence, a
// list described is a claim. The commit that refuses the collision turns each of
// these into an assertion that the query is refused.

import (
	"testing"

	"github.com/advenn/ursus"
)

func udfAt(t *testing.T, df *ursus.DataFrame, row int, col string) int64 {
	t.Helper()
	v, ok, err := df.At[int64](row, col)
	if err != nil {
		t.Fatalf("%s: %v", col, err)
	}
	if !ok {
		t.Fatalf("%s row %d is null", col, row)
	}
	return v
}

// double and triple are deliberately package-level and deliberately DIFFERENT, so
// no reading of the fixture can excuse the merge.
func double(v int64) (int64, error) { return v * 2, nil }
func triple(v int64) (int64, error) { return v * 3, nil }

// TestUDFCollisionInAnAggregate reaches internal/physical/agg.go:749, extractAggs'
// byKey, one map per plan.Aggregate.
func TestUDFCollisionInAnAggregate(t *testing.T) {
	df, err := udfFrame().GroupBy(ursus.Lit(1).Alias("k")).Agg(
		ursus.Col("id").MapElements("f", ursus.Int64, double).Max().Alias("a"),
		ursus.Col("id").MapElements("f", ursus.Int64, triple).Max().Alias("b"),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// id maxes at 4, so a should be 8 and b should be 12.
	a, b := udfAt(t, df, 0, "a"), udfAt(t, df, 0, "b")
	if a != 8 || b != 8 {
		t.Fatalf("a=%d b=%d; the defect is that BOTH are 8 — if this now says "+
			"8 and 12 the merge is gone and this test should assert the refusal", a, b)
	}
}

// TestUDFCollisionInATemporalGroup reaches internal/physical/tempgroup.go:499 — the
// same extractAggs, a different map instance, and the site every doc omits.
func TestUDFCollisionInATemporalGroup(t *testing.T) {
	df, err := hourly(t, "2024-01-01T00:10:00", "2024-01-01T01:05:00").
		GroupByDynamic(ursus.Col("ts"), ursus.DynamicOptions{Every: ursus.Every("1h")}).
		Agg(
			ursus.Col("v").MapElements("f", ursus.Int64, double).Max().Alias("a"),
			ursus.Col("v").MapElements("f", ursus.Int64, triple).Max().Alias("b"),
		).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// tsFrame numbers its rows, so the second window holds v=1: 2 and 3.
	a, b := udfAt(t, df, 1, "a"), udfAt(t, df, 1, "b")
	if a != 2 || b != 2 {
		t.Fatalf("a=%d b=%d; the defect is that BOTH are 2", a, b)
	}
}

// TestUDFCollisionInAWindowTemporary reaches internal/plan/resolve_window.go:76.
//
// This one is worth reading twice: on a merge, extractWindows returns a Col
// reference and never appends the losing subtree to specs, so the second udf is
// DELETED from the tree. Any check that runs after Resolve's walk sees one "f" and
// passes on exactly this query.
func TestUDFCollisionInAWindowTemporary(t *testing.T) {
	df, err := wf().Select(
		ursus.Col("v").MapElements("f", ursus.Int64, double).Max().Over(ursus.Col("g")).Alias("a"),
		ursus.Col("v").MapElements("f", ursus.Int64, triple).Max().Over(ursus.Col("g")).Alias("b"),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// Row 0 is in partition "a", whose values are 1, 3, 3: 6 and 9.
	a, b := udfAt(t, df, 0, "a"), udfAt(t, df, 0, "b")
	if a != 6 || b != 6 {
		t.Fatalf("a=%d b=%d; the defect is that BOTH are 6", a, b)
	}
}

// TestUDFCollisionInAPartitionKey reaches internal/physical/window.go:377, and it
// is the worst of the four: what is shared is not a temporary but a PARTITIONING,
// so every row of the second window is bucketed by the first udf's values.
//
// The two windows must render DIFFERENTLY overall, or resolve_window.go:76 merges
// them first and this never reaches the partition map — hence sum against max.
func TestUDFCollisionInAPartitionKey(t *testing.T) {
	mod2 := func(v int64) (int64, error) { return v % 2, nil }
	mod3 := func(v int64) (int64, error) { return v % 3, nil }
	df, err := wf().Select(
		ursus.Col("v").Sum().Over(
			ursus.Col("t").MapElements("bucket", ursus.Int64, mod2)).Alias("a"),
		ursus.Col("v").Max().Over(
			ursus.Col("t").MapElements("bucket", ursus.Int64, mod3)).Alias("b"),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// t is 0..5 and v is [1,10,3,20,3,null].
	//
	// Under t%3 — what "b" asks for — row 0 joins t=3, so max(1,20) = 20.
	// Under t%2 — what it GETS — row 0 joins t=2 and t=4, so max(1,3,3) = 3.
	if got := udfAt(t, df, 0, "b"); got != 3 {
		t.Fatalf("b row 0 = %d; the defect is 3, the t%%2 partitioning. 20 would "+
			"mean it is correctly partitioned by t%%3", got)
	}
}
