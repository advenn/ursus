package ursus_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/uerr"
	"github.com/advenn/ursus/ursustest"
)

// unpivotFrame is two index columns and two value columns, with a null in the
// second value column so the melted column's nullability is observable. Its rows,
// in ursus's row-major order (each row's columns together, then the next row's,
// where Polars gives every row's first column, then every row's second: the step's
// one deliberate divergence, feature-gaps.md §9), are the unpivot group's
// (oracle_unpivot_test.go). Polars' order cannot be produced one batch at a time;
// row-major can, and TestUnpivotIsBatchSizeInvariant is the other half of the
// argument.
func unpivotFrame() *ursus.LazyFrame {
	return ursus.Frame(
		ursus.Values("id", []int64{1, 2, 3}),
		ursus.Values("region", []string{"eu", "us", "apac"}),
		ursus.Values("jan", []int64{10, 30, 50}),
		ursus.ValuesNullable("feb", []int64{20, 0, 60}, []bool{true, false, true}),
	)
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
	for _, run := range []struct{ size, threads int }{{1, 1}, {3, 2}, {8192, 4}} {
		size, threads := run.size, run.threads
		got, err := query().Collect(t.Context(),
			ursus.WithBatchSize(size), ursus.WithThreads(threads), ursus.WithVerify())
		if err != nil {
			t.Fatalf("batch %d, %d threads: %v", size, threads, err)
		}
		ursustest.AssertFrameEqual(t, got, want, ursustest.CheckNullability())
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

// TestUnpivotKeepsItsColumnsAliveThroughPushdown is the Expressions() arm.
//
// On and Index are column references in string form, invisible to a liveness rule
// that only walks expressions — so without the arm, projection pushdown prunes the
// very columns unpivot reads. Distinct.Subset has the arm for exactly this reason.
func TestUnpivotKeepsItsColumnsAliveThroughPushdown(t *testing.T) {
	melted := unpivotFrame().Unpivot(ursus.UnpivotOptions{On: []string{"jan", "feb"}, Index: []string{"id"}})
	lf := melted.Select(ursus.Col("value"))

	df, err := lf.Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatalf("selecting only the value column must not prune the melted ones: %v", err)
	}
	if df.Height() != 6 {
		t.Fatalf("got %d rows, want 6\n%s", df.Height(), df)
	}
	// The value column alone is the whole melt's value column.
	want, err := melted.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want, err = want.Select(t.Context(), ursus.Col("value"))
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, df, want)

	// Projection pushdown prunes `region`: Unpivot has its arm, pushdownUnpivot,
	// which keeps On and an explicit Index whole and nothing else. This asserted the
	// opposite once, as a log, until step 304 found it logging.
	s, err := lf.Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s, "region") {
		t.Errorf("projection pushdown no longer prunes through Unpivot:\n%s", s)
	}
}

// TestUnpivotWithoutOn is P2 of v0.6.7-scope.md (step 304): On left empty melts
// every column not in Index, as Polars' on=None; with Index empty too, every column;
// and with Index naming every column, nothing, no rows and a value column of the
// Null type. The rows are the unpivot group's; here stay the plan and the schemas.
func TestUnpivotWithoutOn(t *testing.T) {
	ctx := t.Context()
	lf := ursus.Frame(ursus.Values("id", []int64{1, 2}), ursus.Values("a", []int64{3, 4}), ursus.Values("b", []int64{5, 6}))

	// The plan names what it melts, written out when Unpivot was called.
	plan, err := lf.Unpivot(ursus.UnpivotOptions{Index: []string{"id"}}).Explain(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan, "UNPIVOT a, b index id") {
		t.Errorf("the plan:\n%s", plan)
	}

	// Index naming every column melts nothing, and so does a frame of no columns.
	for name, q := range map[string]*ursus.LazyFrame{
		"every column an index": lf.Unpivot(ursus.UnpivotOptions{Index: []string{"id", "a", "b"}}),
		"no columns":            ursus.Frame().Unpivot(ursus.UnpivotOptions{}),
		"eager":                 nil,
	} {
		var got *ursus.DataFrame
		if q == nil {
			df, err := lf.Collect(ctx)
			if err != nil {
				t.Fatal(err)
			}
			got, err = df.Unpivot(ctx, ursus.UnpivotOptions{Index: []string{"id", "a", "b"}})
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		} else if got, err = q.Collect(ctx); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var fields []string
		for _, f := range got.Schema().All() {
			fields = append(fields, f.Name+":"+f.Type.String())
		}
		want := "id:Int64 a:Int64 b:Int64 variable:String value:Null"
		if name == "no columns" {
			want = "variable:String value:Null"
		}
		if g := strings.Join(fields, " "); g != want || got.Height() != 0 {
			t.Errorf("%s: %s, %d rows; want %s and none", name, g, got.Height(), want)
		}
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
		// Nothing named melts every column since step 304, and region is text
		// beside numbers, which Polars casts to String (feature-gaps.md §9).
		{"every column, text among them",
			ursus.UnpivotOptions{},
			"no common type for Int64 and String"},
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
