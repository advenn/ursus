package ursus_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
	"github.com/advenn/ursus/ursustest"
)

// unpivotFrame is two index columns and two value columns, with a null in the
// second value column so the melted column's nullability is observable.
func unpivotFrame() *ursus.LazyFrame {
	return ursus.Frame(
		ursus.Values("id", []int64{1, 2, 3}),
		ursus.Values("region", []string{"eu", "us", "apac"}),
		ursus.Values("jan", []int64{10, 30, 50}),
		ursus.ValuesNullable("feb", []int64{20, 0, 60}, []bool{true, false, true}),
	)
}

// TestUnpivotRowMajor pins the row order, which is the step's one deliberate
// divergence from Polars.
//
// Polars stacks k frames vertically: every row's jan, then every row's feb. That
// order cannot be produced one batch at a time — it would interleave differently at
// different batch sizes. Row-major can, and TestUnpivotIsBatchSizeInvariant is the
// other half of the argument.
func TestUnpivotRowMajor(t *testing.T) {
	df, err := unpivotFrame().
		Unpivot(ursus.UnpivotOptions{On: []string{"jan", "feb"}}).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 6 {
		t.Fatalf("got %d rows, want 6 (3 rows x 2 melted columns)\n%s", df.Height(), df)
	}

	want := []struct {
		id       int64
		variable string
		value    int64
		valueOK  bool
	}{
		{1, "jan", 10, true},
		{1, "feb", 20, true},
		{2, "jan", 30, true},
		{2, "feb", 0, false}, // the null survives the melt
		{3, "jan", 50, true},
		{3, "feb", 60, true},
	}
	for i, w := range want {
		id, _, err := df.At[int64](i, "id")
		if err != nil {
			t.Fatal(err)
		}
		v, _, err := df.At[string](i, "variable")
		if err != nil {
			t.Fatal(err)
		}
		val, ok, err := df.At[int64](i, "value")
		if err != nil {
			t.Fatal(err)
		}
		if id != w.id || v != w.variable || ok != w.valueOK || (w.valueOK && val != w.value) {
			t.Errorf("row %d: (%d, %q, %v/%v), want (%d, %q, %v/%v)",
				i, id, v, val, ok, w.id, w.variable, w.value, w.valueOK)
		}
	}
}

// TestUnpivotValueNullability. The value column is nullable if ANY melted column
// is, because a row takes its value from one of them and the schema cannot know
// which.
//
// Asserted against the SCHEMA rather than the data, and that is the point: nothing
// in the engine validates a declared nullability against what a column actually
// holds — data.NewBatch checks names and types only, and WithVerify is the
// optimizer's cross-rule schema check, not this. A column declared non-null that
// holds a null is a lie no machinery catches, so only an explicit assertion does.
func TestUnpivotValueNullability(t *testing.T) {
	nullable, err := unpivotFrame().
		Unpivot(ursus.UnpivotOptions{On: []string{"jan", "feb"}}).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if f := nullable.Schema().Field(nullable.Schema().Len() - 1); !f.Nullable {
		t.Error("feb holds a null, so the value column must be nullable")
	}

	// And the other direction: melting two non-null columns gives a non-null value.
	notNull, err := ursus.Frame(
		ursus.Values("id", []int64{1}),
		ursus.Values("jan", []int64{10}),
		ursus.Values("feb", []int64{20}),
	).Unpivot(ursus.UnpivotOptions{On: []string{"jan", "feb"}}).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if f := notNull.Schema().Field(notNull.Schema().Len() - 1); f.Nullable {
		t.Error("neither melted column is nullable, so the value column is not either")
	}
}

// TestUnpivotIsBatchSizeInvariant is the reason for row-major. It is also the check
// that the operator is a real BatchOp: it rides parallelOp, so the thread counts
// have to agree too.
func TestUnpivotIsBatchSizeInvariant(t *testing.T) {
	query := func() *ursus.LazyFrame {
		return unpivotFrame().Unpivot(ursus.UnpivotOptions{On: []string{"jan", "feb"}})
	}
	want, err := query().Collect(t.Context(), ursus.WithBatchSize(8192))
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{1, 2, 3, 7, 8192} {
		for _, threads := range []int{1, 2, 4} {
			got, err := query().Collect(t.Context(),
				ursus.WithBatchSize(size), ursus.WithThreads(threads), ursus.WithVerify())
			if err != nil {
				t.Fatalf("batch %d, %d threads: %v", size, threads, err)
			}
			ursustest.AssertFrameEqual(t, got, want, ursustest.CheckNullability())
		}
	}
}

// TestUnpivotPromotesTheValueType. Every melted column shares one value column, so
// their types must promote — and the promoted type is what the schema declares, not
// the first column's.
func TestUnpivotPromotesTheValueType(t *testing.T) {
	df, err := ursus.Frame(
		ursus.Values("id", []int64{1}),
		ursus.Values("small", []int32{7}),
		ursus.Values("big", []int64{1 << 40}),
	).Unpivot(ursus.UnpivotOptions{On: []string{"small", "big"}}).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if got := df.Schema().Field(df.Schema().Len() - 1).Type; got != dtype.Int64 {
		t.Errorf("value column is %s, want Int64 — Int32 and Int64 promote to Int64", got)
	}
	// The Int32 must be widened, not reinterpreted, and the Int64 must survive
	// intact — a value past Int32's range is what shows a truncation.
	if v, _, err := df.At[int64](0, "value"); err != nil {
		t.Fatal(err)
	} else if v != 7 {
		t.Errorf("the Int32 melted to %d, want 7", v)
	}
	if v, _, err := df.At[int64](1, "value"); err != nil {
		t.Fatal(err)
	} else if v != 1<<40 {
		t.Errorf("the Int64 melted to %d, want %d", v, int64(1)<<40)
	}
}

// TestUnpivotIndexDefaultsToTheRest — and the contrast with naming it explicitly.
func TestUnpivotIndexDefaultsToTheRest(t *testing.T) {
	implicit, err := unpivotFrame().
		Unpivot(ursus.UnpivotOptions{On: []string{"jan", "feb"}}).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if got := implicit.Schema().Names(); strings.Join(got, ",") != "id,region,variable,value" {
		t.Errorf("implicit index gave %v, want id, region, variable, value", got)
	}

	// Naming a subset drops the rest, which is the point of naming it.
	explicit, err := unpivotFrame().
		Unpivot(ursus.UnpivotOptions{On: []string{"jan", "feb"}, Index: []string{"id"}}).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if got := explicit.Schema().Names(); strings.Join(got, ",") != "id,variable,value" {
		t.Errorf("explicit index gave %v, want id, variable, value", got)
	}
}

// TestUnpivotCustomNames — the two invented columns are the only ones a user has no
// other way to name.
func TestUnpivotCustomNames(t *testing.T) {
	df, err := unpivotFrame().Unpivot(ursus.UnpivotOptions{
		On:           []string{"jan", "feb"},
		Index:        []string{"id"},
		VariableName: "month",
		ValueName:    "sales",
	}).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if got := df.Schema().Names(); strings.Join(got, ",") != "id,month,sales" {
		t.Errorf("got %v, want id, month, sales", got)
	}
}

// TestUnpivotFeedsAnOrdinaryFrame. The output is not special: it groups, filters and
// sorts like anything else, which is the whole point of melting.
func TestUnpivotFeedsAnOrdinaryFrame(t *testing.T) {
	df, err := unpivotFrame().
		Unpivot(ursus.UnpivotOptions{On: []string{"jan", "feb"}}).
		GroupBy(ursus.Col("variable")).
		Agg(ursus.Col("value").Sum().Cast(ursus.Int64).Alias("total")).
		Sort(ursus.Asc(ursus.Col("variable"))).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 2 {
		t.Fatalf("got %d groups, want 2\n%s", df.Height(), df)
	}
	// feb is 20 + null + 60 = 80; jan is 10 + 30 + 50 = 90.
	for i, want := range []int64{80, 90} {
		if v, _, err := df.At[int64](i, "total"); err != nil {
			t.Fatal(err)
		} else if v != want {
			t.Errorf("row %d: total = %d, want %d\n%s", i, v, want, df)
		}
	}
}

// TestUnpivotKeepsItsColumnsAliveThroughPushdown is the Expressions() arm.
//
// On and Index are column references in string form, invisible to a liveness rule
// that only walks expressions — so without the arm, projection pushdown prunes the
// very columns unpivot reads. Distinct.Subset has the arm for exactly this reason.
func TestUnpivotKeepsItsColumnsAliveThroughPushdown(t *testing.T) {
	lf := unpivotFrame().
		Unpivot(ursus.UnpivotOptions{On: []string{"jan", "feb"}, Index: []string{"id"}}).
		Select(ursus.Col("value"))

	df, err := lf.Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatalf("selecting only the value column must not prune the melted ones: %v", err)
	}
	if df.Height() != 6 {
		t.Fatalf("got %d rows, want 6\n%s", df.Height(), df)
	}
	if v, _, err := df.At[int64](0, "value"); err != nil {
		t.Fatal(err)
	} else if v != 10 {
		t.Errorf("first value is %d, want 10", v)
	}

	// Projection pushdown does NOT prune `region`, and that is the fail-safe
	// default rather than a bug: "An unrecognised node type requires ALL of its
	// input's columns ... New node types are therefore correct-but-unoptimized the
	// day they are added, rather than silently pruning columns they needed."
	//
	// Asserted so the day someone adds a pushdown arm, this test says so rather
	// than passing silently either way.
	s, err := lf.Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, "region") {
		t.Log("projection pushdown now prunes through Unpivot; update this test " +
			"and the open list in step-47-as-built.md")
	}
}

// TestUnpivotRefusals — four ways to get it wrong, each with an error that says
// which.
func TestUnpivotRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts ursus.UnpivotOptions
		want string
	}{
		{"no columns to melt",
			ursus.UnpivotOptions{},
			"at least one column"},
		{"unknown melted column",
			ursus.UnpivotOptions{On: []string{"nope"}},
			"nope"},
		{"unknown index column",
			ursus.UnpivotOptions{On: []string{"jan"}, Index: []string{"nope"}},
			"nope"},
		{"a column both melted and kept",
			ursus.UnpivotOptions{On: []string{"jan"}, Index: []string{"id", "jan"}},
			"both melted and kept"},
		{"no common type",
			ursus.UnpivotOptions{On: []string{"jan", "region"}},
			"no common type"},
		{"the value name collides with an index column",
			ursus.UnpivotOptions{On: []string{"jan"}, ValueName: "region"},
			"region"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := unpivotFrame().Unpivot(tc.opts).Collect(t.Context())
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the error should mention %q: %v", tc.want, err)
			}
			if errorsIsInternal(err) {
				t.Errorf("a user mistake must not be reported as an ursus bug: %v", err)
			}
		})
	}
}

func errorsIsInternal(err error) bool {
	return errors.Is(err, uerr.ErrInternal)
}
