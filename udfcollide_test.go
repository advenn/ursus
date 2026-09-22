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
// # Each test was first written to assert the WRONG ANSWER
//
// The measured values are kept in the comments, because they are the evidence that
// the refusal below is refusing something real rather than something imagined. Each
// test now pairs the refusal with a CONTROL in the same shape and distinct names,
// whose two answers must differ — without that, "the planner refused" would be
// indistinguishable from "the query was broken for some other reason".

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/uerr"
)

// assertUDFCollision fails unless err is the name-collision refusal, and not some
// other error that happens to be non-nil — which is the whole risk with a test that
// asserts a failure.
func assertUDFCollision(t *testing.T, err error, name string) {
	t.Helper()
	if err == nil {
		t.Fatalf("two different udfs named %q must be refused", name)
	}
	var e *uerr.Error
	if !errors.As(err, &e) || !errors.Is(&uerr.Error{Kind: e.Kind}, uerr.ErrValue) {
		t.Fatalf("want a KindValue refusal, got %v", err)
	}
	want := fmt.Sprintf("two different udfs are both named %q", name)
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("refused for the wrong reason: %v", err)
	}
	if !strings.Contains(err.Error(), "reuse that variable") {
		t.Errorf("the refusal must name the workaround: %v", err)
	}
}

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
	agg := func(n1, n2 string) ([]int64, error) {
		df, err := udfFrame().GroupBy(ursus.Lit(1).Alias("k")).Agg(
			ursus.Col("id").MapElements(n1, ursus.Int64, double).Max().Alias("a"),
			ursus.Col("id").MapElements(n2, ursus.Int64, triple).Max().Alias("b"),
		).Collect(t.Context())
		if err != nil {
			return nil, err
		}
		return []int64{udfAt(t, df, 0, "a"), udfAt(t, df, 0, "b")}, nil
	}

	_, err := agg("f", "f")
	assertUDFCollision(t, err, "f")

	// The control. id maxes at 4, so these must be 8 and 12 — measured as 8 and 8
	// before the refusal landed.
	got, err := agg("f", "g")
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != 8 || got[1] != 12 {
		t.Errorf("a=%d b=%d, want 8 and 12", got[0], got[1])
	}
}

// TestUDFCollisionInATemporalGroup reaches internal/physical/tempgroup.go:499 — the
// same extractAggs, a different map instance, and the site every doc omits.
func TestUDFCollisionInATemporalGroup(t *testing.T) {
	dyn := func(n1, n2 string) ([]int64, error) {
		df, err := hourly(t, "2024-01-01T00:10:00", "2024-01-01T01:05:00").
			GroupByDynamic(ursus.Col("ts"), ursus.DynamicOptions{Every: ursus.Every("1h")}).
			Agg(
				ursus.Col("v").MapElements(n1, ursus.Int64, double).Max().Alias("a"),
				ursus.Col("v").MapElements(n2, ursus.Int64, triple).Max().Alias("b"),
			).Collect(t.Context())
		if err != nil {
			return nil, err
		}
		return []int64{udfAt(t, df, 1, "a"), udfAt(t, df, 1, "b")}, nil
	}

	_, err := dyn("f", "f")
	assertUDFCollision(t, err, "f")

	// The control. tsFrame numbers its rows, so the second window holds v=1:
	// 2 and 3, measured as 2 and 2 before the refusal landed.
	got, err := dyn("f", "g")
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != 2 || got[1] != 3 {
		t.Errorf("a=%d b=%d, want 2 and 3", got[0], got[1])
	}
}

// TestUDFCollisionInAWindowTemporary reaches internal/plan/resolve_window.go:76.
//
// This one is worth reading twice: on a merge, extractWindows returns a Col
// reference and never appends the losing subtree to specs, so the second udf is
// DELETED from the tree. Any check that runs after Resolve's walk sees one "f" and
// passes on exactly this query.
func TestUDFCollisionInAWindowTemporary(t *testing.T) {
	over := func(n1, n2 string) ([]int64, error) {
		df, err := wf().Select(
			ursus.Col("v").MapElements(n1, ursus.Int64, double).Max().Over(ursus.Col("g")).Alias("a"),
			ursus.Col("v").MapElements(n2, ursus.Int64, triple).Max().Over(ursus.Col("g")).Alias("b"),
		).Collect(t.Context())
		if err != nil {
			return nil, err
		}
		return []int64{udfAt(t, df, 0, "a"), udfAt(t, df, 0, "b")}, nil
	}

	_, err := over("f", "f")
	assertUDFCollision(t, err, "f")

	// The control. Row 0 is in partition "a", whose values are 1, 3, 3: 6 and 9,
	// measured as 6 and 6 before the refusal landed.
	got, err := over("f", "g")
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != 6 || got[1] != 9 {
		t.Errorf("a=%d b=%d, want 6 and 9", got[0], got[1])
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
	part := func(n1, n2 string) (int64, error) {
		df, err := wf().Select(
			ursus.Col("v").Sum().Over(
				ursus.Col("t").MapElements(n1, ursus.Int64, mod2)).Alias("a"),
			ursus.Col("v").Max().Over(
				ursus.Col("t").MapElements(n2, ursus.Int64, mod3)).Alias("b"),
		).Collect(t.Context())
		if err != nil {
			return 0, err
		}
		return udfAt(t, df, 0, "b"), nil
	}

	_, err := part("bucket", "bucket")
	assertUDFCollision(t, err, "bucket")

	// The control. t is 0..5 and v is [1,10,3,20,3,null]. Under t%3 — what "b"
	// asks for — row 0 joins t=3, so max(1,20) = 20. Under t%2, which it was
	// GETTING, row 0 joins t=2 and t=4, so max(1,3,3) = 3.
	got, err := part("bucket2", "bucket3")
	if err != nil {
		t.Fatal(err)
	}
	if got != 20 {
		t.Errorf("b row 0 = %d, want 20; 3 is the t%%2 partitioning", got)
	}
}

// TestOneUDFUsedTwiceIsLegal is the other half of the rule, and the case that makes
// the identity worth carrying at all.
//
// One Expr, held in a variable and used twice, is ONE udf. The same node appears in
// two places in the tree, so the check sees its name twice — and must not refuse,
// because there is only one closure and merging the two is exactly right.
//
// It is also the workaround the refusal recommends for a helper called twice, so if
// this stopped working the advice would be impossible, which is a defect this
// project has now fixed twice elsewhere.
func TestOneUDFUsedTwiceIsLegal(t *testing.T) {
	e := ursus.Col("id").MapElements("f", ursus.Int64, double)

	df, err := udfFrame().Select(
		e.Alias("a"),
		e.Gt(ursus.Lit(int64(4))).Alias("big"),
	).Collect(t.Context())
	if err != nil {
		t.Fatalf("one udf used twice must be legal: %v", err)
	}
	if got := udfAt(t, df, 3, "a"); got != 8 {
		t.Errorf("a row 3 = %d, want 8", got)
	}
	big, ok, err := df.At[bool](3, "big")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || !big {
		t.Errorf("big row 3 = %v (valid=%v), want true", big, ok)
	}
}
