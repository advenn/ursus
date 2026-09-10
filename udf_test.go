package ursus_test

// UDFs are the escape hatch: a Go function the planner cannot see into. Everything
// the engine knows about one, the caller declared — so most of what is worth
// testing is what happens when a declaration is FALSE.

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/uerr"
	"github.com/advenn/ursus/ursustest"
)

func udfFrame() *ursus.LazyFrame {
	return ursus.Frame(
		ursus.Values("id", []int64{1, 2, 3, 4}),
		ursus.ValuesNullable("c", []float64{0, 100, -40, 0}, []bool{true, true, true, false}),
		ursus.Values("s", []string{"a", "bb", "ccc", "dddd"}),
	)
}

// TestMapElements is the feature: a Go function per value, with the type changing
// across the call.
func TestMapElements(t *testing.T) {
	df, err := udfFrame().Select(
		ursus.Col("id"),
		ursus.Col("c").MapElements("to_f", ursus.Float64,
			func(c float64) (float64, error) { return c*9/5 + 32, nil }).Alias("f"),
		ursus.Col("s").MapElements("width", ursus.Int64,
			func(s string) (int64, error) { return int64(len(s)), nil }).Alias("w"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}

	for i, want := range []float64{32, 212, -40} {
		got, ok, err := df.At[float64](i, "f")
		if err != nil {
			t.Fatal(err)
		}
		if !ok || got != want {
			t.Errorf("row %d: f = %v (present %v), want %v", i, got, ok, want)
		}
	}
	// A null input is a null output, and fn was never called for it.
	if _, ok, err := df.At[float64](3, "f"); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Error("a null input row must produce a null output row")
	}
	for i, want := range []int64{1, 2, 3, 4} {
		if got, _, err := df.At[int64](i, "w"); err != nil {
			t.Fatal(err)
		} else if got != want {
			t.Errorf("row %d: w = %d, want %d", i, got, want)
		}
	}
}

// TestMapElementsNeverSeesANull. The signature has no way to say "null", so fn must
// not be called for one — otherwise it receives a zero value indistinguishable from
// a real one.
func TestMapElementsNeverSeesANull(t *testing.T) {
	var calls int
	var mu sync.Mutex
	df, err := udfFrame().Select(
		ursus.Col("c").MapElements("count", ursus.Float64,
			func(c float64) (float64, error) {
				mu.Lock()
				calls++
				mu.Unlock()
				return c, nil
			}).Alias("out"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Errorf("fn called %d times over 4 rows with 1 null, want 3", calls)
	}
	if df.Height() != 4 {
		t.Errorf("got %d rows, want 4 — a skipped null is still a row", df.Height())
	}
}

// TestMapBatches: the vectorized form, which is the one that can SEE and SET nulls.
func TestMapBatches(t *testing.T) {
	df, err := udfFrame().Select(
		ursus.Col("c").MapBatches("clamp", ursus.Float64,
			func(vals []float64, ok []bool) ([]float64, []bool, error) {
				out := make([]float64, len(vals))
				mask := make([]bool, len(vals))
				for i, v := range vals {
					// A negative value becomes a null — something MapElements
					// cannot express at all.
					out[i], mask[i] = v, ok[i] && v >= 0
				}
				return out, mask, nil
			}).Alias("out"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		row     int
		want    float64
		present bool
	}{{0, 0, true}, {1, 100, true}, {2, 0, false}, {3, 0, false}} {
		got, ok, err := df.At[float64](tc.row, "out")
		if err != nil {
			t.Fatal(err)
		}
		if ok != tc.present {
			t.Errorf("row %d: present = %v, want %v", tc.row, ok, tc.present)
		}
		if ok && got != tc.want {
			t.Errorf("row %d: %v, want %v", tc.row, got, tc.want)
		}
	}
}

// TestMapBatchesNilMaskMeansAllValid documents the shortcut, which is the common
// case for a total function.
func TestMapBatchesNilMaskMeansAllValid(t *testing.T) {
	df, err := udfFrame().Select(
		ursus.Col("c").MapBatches("all_valid", ursus.Float64,
			func(vals []float64, _ []bool) ([]float64, []bool, error) {
				return vals, nil, nil
			}).Alias("out"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	// Row 3 was null on the way in and is NOT on the way out, because the udf said
	// every row is valid. That is the udf's call to make.
	if _, ok, err := df.At[float64](3, "out"); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Error("a nil mask declares every row valid, including one that was null")
	}
}

// TestUDFDeclaredTypeIsChecked is the evaluator contract, which a UDF is the first
// node able to violate: the user supplies both sides of
// `Eval(...).DType() == n.Field(...).Type`.
//
// int64 values declared as Float64 have the same width and different meaning, which
// is exactly the confusion data.Values' own doc calls "the worst kind of wrong".
func TestUDFDeclaredTypeIsChecked(t *testing.T) {
	_, err := udfFrame().Select(
		ursus.Col("id").MapElements("liar", ursus.Float64,
			func(v int64) (int64, error) { return v, nil }).Alias("out"),
	).Collect(t.Context())
	if err == nil {
		t.Fatal("a udf returning int64 while declaring Float64 must be refused")
	}
	for _, want := range []string{"liar", "Int64", "Float64"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should name %s: %v", want, err)
		}
	}
	if errors.Is(err, uerr.ErrInternal) {
		t.Errorf("a false declaration is a user error: %v", err)
	}
}

// TestUDFRowCountIsChecked. A MapBatches returning the wrong length must be a USER
// error naming the udf — evalColumn would report it as Internalf, and would
// silently BROADCAST a length-1 result, which for a udf is a bug papered over.
func TestUDFRowCountIsChecked(t *testing.T) {
	for _, tc := range []struct {
		name string
		n    int
	}{{"too few", 1}, {"too many", 9}} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := udfFrame().Select(
				ursus.Col("c").MapBatches("wrong_len", ursus.Float64,
					func(vals []float64, _ []bool) ([]float64, []bool, error) {
						return make([]float64, tc.n), nil, nil
					}).Alias("out"),
			).Collect(t.Context())
			if err == nil {
				t.Fatal("a udf returning the wrong number of rows must be refused")
			}
			if !strings.Contains(err.Error(), "wrong_len") {
				t.Errorf("the error should name the udf: %v", err)
			}
			if errors.Is(err, uerr.ErrInternal) {
				t.Errorf("a udf returning the wrong length is a user error: %v", err)
			}
		})
	}
}

// TestUDFErrorNamesTheRow. A udf that fails on one row of many is useless without
// knowing which — the contract Series.MapErr already set.
func TestUDFErrorNamesTheRow(t *testing.T) {
	boom := errors.New("boom")
	_, err := udfFrame().Select(
		ursus.Col("c").MapElements("fails", ursus.Float64,
			func(c float64) (float64, error) {
				if c < 0 {
					return 0, boom
				}
				return c, nil
			}).Alias("out"),
	).Collect(t.Context())
	if err == nil {
		t.Fatal("the udf's error must reach the caller")
	}
	if !errors.Is(err, boom) {
		t.Errorf("the user's own error must be unwrappable: %v", err)
	}
	for _, want := range []string{"fails", "row 2", `"c"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should mention %s: %v", want, err)
		}
	}
}

// TestUDFErrorIsTheSameRowAtEveryBatchSize.
//
// The row index a udf reports is its index WITHIN A BATCH, so a naive
// implementation names a different row depending on how the query happened to run.
// Batching is not something the user asked for, so it must not be visible.
func TestUDFErrorIsTheSameRowAtEveryBatchSize(t *testing.T) {
	boom := errors.New("boom")
	for _, size := range []int{1, 2, 3, 7, 8192} {
		for _, threads := range []int{1, 2, 4} {
			_, err := udfFrame().Select(
				ursus.Col("id").MapElements("fails", ursus.Int64,
					func(v int64) (int64, error) {
						if v == 3 {
							return 0, boom
						}
						return v, nil
					}).Alias("out"),
			).Collect(t.Context(),
				ursus.WithBatchSize(size), ursus.WithThreads(threads))
			if err == nil {
				t.Fatalf("size %d threads %d: expected the udf to fail", size, threads)
			}
			if !errors.Is(err, boom) {
				t.Errorf("size %d threads %d: %v", size, threads, err)
			}
		}
	}
}

// TestUDFIsBatchSizeInvariant. The results, unlike the error index, must be
// identical at every batch size and thread count — the standing rule for every
// operator that has one.
func TestUDFIsBatchSizeInvariant(t *testing.T) {
	q := func() *ursus.LazyFrame {
		return udfFrame().Select(
			ursus.Col("id"),
			ursus.Col("c").MapElements("to_f", ursus.Float64,
				func(c float64) (float64, error) { return c*9/5 + 32, nil }).Alias("f"),
		)
	}
	want, err := q().Collect(t.Context(), ursus.WithBatchSize(8192), ursus.WithThreads(1))
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{1, 2, 3, 7} {
		for _, threads := range []int{1, 2, 4} {
			got, err := q().Collect(t.Context(),
				ursus.WithBatchSize(size), ursus.WithThreads(threads), ursus.WithVerify())
			if err != nil {
				t.Fatalf("size %d threads %d: %v", size, threads, err)
			}
			ursustest.AssertFrameEqual(t, got, want, ursustest.CheckNullability())
		}
	}
}

// TestTwoUDFsWithDifferentNamesDoNotCollapse is the reason the name is required.
//
// Three parts of the engine deduplicate expressions by their RENDERED form, and a
// Go closure has no rendered form. Without a name in String(), these two render
// alike, become one computation, and both columns get the first one's answer —
// the shape of the unparameterised-Quantile bug that collapsed p50 and p99.
//
// The aggregate context is the one that bites, because extractAggs' byKey map is
// keyed on String().
func TestTwoUDFsWithDifferentNamesDoNotCollapse(t *testing.T) {
	df, err := udfFrame().GroupBy(ursus.Lit(1).Alias("k")).Agg(
		ursus.Col("id").MapElements("double", ursus.Int64,
			func(v int64) (int64, error) { return v * 2, nil }).
			Sum().Cast(ursus.Int64).Alias("doubled"),
		ursus.Col("id").MapElements("triple", ursus.Int64,
			func(v int64) (int64, error) { return v * 3, nil }).
			Sum().Cast(ursus.Int64).Alias("tripled"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}

	// 1+2+3+4 = 10, so doubled is 20 and tripled is 30. Collapsing gives 20 twice.
	d, _, err := df.At[int64](0, "doubled")
	if err != nil {
		t.Fatal(err)
	}
	tr, _, err := df.At[int64](0, "tripled")
	if err != nil {
		t.Fatal(err)
	}
	if d != 20 || tr != 30 {
		t.Errorf("doubled = %d, tripled = %d; want 20 and 30 — equal values mean "+
			"the two udfs collapsed into one computation\n%s", d, tr, df)
	}
}

// TestUDFRequiresAName refuses the empty name at construction, since the whole
// deduplication argument rests on it.
func TestUDFRequiresAName(t *testing.T) {
	e := ursus.Col("id").MapElements("", ursus.Int64,
		func(v int64) (int64, error) { return v, nil })
	if e.Err() == nil {
		t.Fatal("an unnamed udf must be refused")
	}
	if _, err := udfFrame().Select(e).Collect(t.Context()); err == nil {
		t.Error("the deferred error must surface at Collect")
	}
}

// TestUDFExpandsAcrossColumns reaches expr.Rebuild, which PANICS rather than
// erroring on a node type it does not know — so this is the arm whose absence is
// not a wrong answer but a crash.
func TestUDFExpandsAcrossColumns(t *testing.T) {
	df, err := ursus.Frame(
		ursus.Values("a", []int64{1, 2}),
		ursus.Values("b", []int64{10, 20}),
	).Select(
		ursus.Col("a", "b").MapElements("neg", ursus.Int64,
			func(v int64) (int64, error) { return -v, nil }),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(df.Schema().Names(), ","); got != "a,b" {
		t.Errorf("expansion should name both columns, got %s", got)
	}
	if v, _, err := df.At[int64](0, "b"); err != nil {
		t.Fatal(err)
	} else if v != -10 {
		t.Errorf("b[0] = %d, want -10", v)
	}
}

// TestUDFInsideAnAggregate reaches extractAggs, whose default arm is an internal
// error. Both nestings are covered: a udf under an aggregate, and one over it.
func TestUDFInsideAnAggregate(t *testing.T) {
	df, err := udfFrame().GroupBy(ursus.Lit(1).Alias("k")).Agg(
		ursus.Col("id").MapElements("dbl", ursus.Int64,
			func(v int64) (int64, error) { return v * 2, nil }).
			Sum().Cast(ursus.Int64).Alias("under"),
		// Sum widens Int64 to Int128, which no Literal type names, so the cast
		// is what makes the aggregate readable by a udf at all.
		ursus.Col("id").Sum().Cast(ursus.Int64).MapElements("neg", ursus.Int64,
			func(v int64) (int64, error) { return -v, nil }).Alias("over"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if v, _, err := df.At[int64](0, "under"); err != nil {
		t.Fatal(err)
	} else if v != 20 {
		t.Errorf("under = %d, want 20", v)
	}
	if v, _, err := df.At[int64](0, "over"); err != nil {
		t.Fatal(err)
	} else if v != -10 {
		t.Errorf("over = %d, want -10", v)
	}
}

// TestUDFIsOpaqueToTheOptimizer. Explain must not run the function — a folded udf
// would execute user code at plan time, before Collect, and the fold's result
// would be baked into the plan.
func TestUDFIsOpaqueToTheOptimizer(t *testing.T) {
	var ran bool
	lf := ursus.Frame(ursus.Values("x", []int64{1})).Select(
		ursus.Lit(int64(2)).MapElements("side_effect", ursus.Int64,
			func(v int64) (int64, error) { ran = true; return v, nil }).Alias("out"),
	)

	plan, err := lf.Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if ran {
		t.Error("Explain ran the udf; a udf over a literal must not be const-folded")
	}
	if !strings.Contains(plan, "side_effect") {
		t.Errorf("the plan should name the udf:\n%s", plan)
	}
}

// TestUDFOverAnUnknownColumn must be a schema error at plan time, which is what
// resolving the child inside UDF.Field buys.
func TestUDFOverAnUnknownColumn(t *testing.T) {
	lf := udfFrame().Select(
		ursus.Col("nope").MapElements("f", ursus.Int64,
			func(v int64) (int64, error) { return v, nil }),
	)
	if _, err := lf.CollectSchema(t.Context()); err == nil {
		t.Error("a udf over an unknown column must fail before any data is read")
	}
	_, err := lf.Collect(t.Context())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("the error should name the column: %v", err)
	}
	if errors.Is(err, uerr.ErrInternal) {
		t.Errorf("a typo is a user error: %v", err)
	}
}

// TestUDFReadsTheWrongGoType. `In` is checked against the column's physical layout
// at the first batch, not at compile time, so this has to be a clean error.
func TestUDFReadsTheWrongGoType(t *testing.T) {
	_, err := udfFrame().Select(
		ursus.Col("s").MapElements("mismatch", ursus.Int64,
			func(v int64) (int64, error) { return v, nil }).Alias("out"),
	).Collect(t.Context())
	if err == nil {
		t.Fatal("reading a String column as int64 must be refused")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("the error should name the udf: %v", err)
	}
}

// TestUDFDeclaringAnIncompatibleStorage. Physical layout, not Go type, is what
// makes a declared output type legal — Decimal is stored as Int128, so returning
// float64 values under it would reinterpret the bits.
func TestUDFDeclaringAnIncompatibleStorage(t *testing.T) {
	_, err := udfFrame().Select(
		ursus.Col("c").MapElements("as_decimal", ursus.Decimal(10, 2),
			func(v float64) (float64, error) { return v, nil }).Alias("out"),
	).Collect(t.Context())
	if err == nil {
		t.Fatal("float64 values cannot be returned as a Decimal")
	}
	if !strings.Contains(err.Error(), "Decimal") {
		t.Errorf("the error should name the declared type: %v", err)
	}
}

// TestUDFReinterpretsCompatibleStorage is the other side of that rule, and the
// reason it is physical-layout rather than exact-type: a udf may legitimately
// build a temporal column out of ticks.
func TestUDFReinterpretsCompatibleStorage(t *testing.T) {
	dt := ursus.Datetime(ursus.Second, "UTC")
	df, err := udfFrame().Select(
		ursus.Col("id").MapElements("as_ts", dt,
			func(v int64) (int64, error) { return v * 86400, nil }).Alias("ts"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if got := df.Schema().Field(0).Type; got != dt {
		t.Errorf("type = %s, want %s", got, dt)
	}
	if v, _, err := df.At[int64](1, "ts"); err != nil {
		t.Fatal(err)
	} else if v != 2*86400 {
		t.Errorf("ts[1] = %d, want %d", v, 2*86400)
	}
}

// TestUDFSurvivesAFilterAndAJoin puts a udf somewhere the optimizer will want to
// move things around it. The answer must not depend on any rule.
func TestUDFSurvivesAFilterAndAJoin(t *testing.T) {
	right := ursus.Frame(
		ursus.Values("id", []int64{2, 3}),
		ursus.Values("tag", []string{"x", "y"}),
	)
	q := func() *ursus.LazyFrame {
		return udfFrame().
			WithColumns(ursus.Col("id").MapElements("sq", ursus.Int64,
				func(v int64) (int64, error) { return v * v, nil }).Alias("sq")).
			Join(right, ursus.JoinOn(ursus.Col("id"))).
			Filter(ursus.Col("sq").Gt(int64(3))).
			Select(ursus.Col("id"), ursus.Col("sq"), ursus.Col("tag"))
	}
	got, err := q().Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	off, err := q().Collect(t.Context(), ursus.WithOptFlags(noProjectionPushdown()))
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, got, off, ursustest.CheckNullability())
	if got.Height() != 2 {
		t.Errorf("got %d rows, want 2\n%s", got.Height(), got)
	}
}

// TestUDFIsCalledConcurrently pins the documented requirement rather than a hope.
// If parallelise ever stops handing a Select to N workers this becomes vacuous
// rather than wrong, so it asserts only that the results are right under -race.
func TestUDFIsCalledConcurrently(t *testing.T) {
	n := 1000
	ids := make([]int64, n)
	for i := range ids {
		ids[i] = int64(i)
	}
	var mu sync.Mutex
	seen := map[int64]struct{}{}

	df, err := ursus.Frame(ursus.Values("id", ids)).Select(
		ursus.Col("id").MapElements("record", ursus.Int64,
			func(v int64) (int64, error) {
				mu.Lock()
				seen[v] = struct{}{}
				mu.Unlock()
				return v, nil
			}).Alias("out"),
	).Collect(t.Context(), ursus.WithBatchSize(16), ursus.WithThreads(4),
		ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != n {
		t.Fatalf("got %d rows, want %d", df.Height(), n)
	}
	if len(seen) != n {
		t.Errorf("the udf saw %d distinct values, want %d", len(seen), n)
	}
	// Output order is preserved even though invocation order is not.
	for i := range n {
		if v, _, err := df.At[int64](i, "out"); err != nil {
			t.Fatal(err)
		} else if v != int64(i) {
			t.Fatalf("row %d = %d; output order must not depend on scheduling", i, v)
		}
	}
}

// TestGoldenUDFPlan snapshots how a UDF renders.
//
// String() is not cosmetic here: three parts of the engine deduplicate expressions
// by it, so a change to this file is a change to which computations are shared.
// That is exactly the kind of thing a result-comparison test cannot see, and it is
// what a golden plan is for.
func TestGoldenUDFPlan(t *testing.T) {
	lf := udfFrame().
		WithColumns(
			ursus.Col("c").MapElements("to_f", ursus.Float64,
				func(c float64) (float64, error) { return c*9/5 + 32, nil }).Alias("f"),
			ursus.Col("s").MapBatches("widths", ursus.Int64,
				func(vals []string, ok []bool) ([]int64, []bool, error) {
					out := make([]int64, len(vals))
					for i, v := range vals {
						out[i] = int64(len(v))
					}
					return out, ok, nil
				}).Alias("w"),
		).
		Filter(ursus.Col("w").Gt(int64(1))).
		Select(ursus.Col("id"), ursus.Col("f"), ursus.Col("w"))

	ursustest.AssertPlan(t, lf, "testdata/plans/udf_optimized.txt")
	ursustest.AssertPlan(t, lf, "testdata/plans/udf_raw.txt", ursus.Optimized(false))
}

// TestUDFIsNotDuplicatedByPredicatePushdown.
//
// Pushing a filter below the node that defines its column works by SUBSTITUTING the
// definition into the predicate — which copies it. For an ordinary expression that
// is a good trade. For a udf it can only lose: the function then runs on every
// input row inside the filter, and the defining node still runs it on every
// survivor to produce the column. n + survivors calls, where not pushing costs n.
//
// The golden plan is what found this; no result comparison could, because the
// answer is identical either way. This test counts the calls.
func TestUDFIsNotDuplicatedByPredicatePushdown(t *testing.T) {
	var mu sync.Mutex
	calls := 0

	df, err := ursus.Frame(ursus.Values("id", []int64{1, 2, 3, 4})).
		WithColumns(ursus.Col("id").MapElements("sq", ursus.Int64,
			func(v int64) (int64, error) {
				mu.Lock()
				calls++
				mu.Unlock()
				return v * v, nil
			}).Alias("sq")).
		Filter(ursus.Col("sq").Gt(int64(4))).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 2 {
		t.Fatalf("got %d rows, want 2\n%s", df.Height(), df)
	}
	if calls != 4 {
		t.Errorf("the udf ran %d times over 4 rows; substituting it into the "+
			"predicate makes it run 4 + 2", calls)
	}
}
