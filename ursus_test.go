package ursus_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/uerr"
)

// frame builds the fixture used across this file. Six columns, so projection
// pushdown has something to prune.
func frame() *ursus.LazyFrame {
	return ursus.Frame(
		ursus.Values("id", []int64{1, 2, 3, 4, 5, 6}),
		ursus.Values("region", []string{"eu", "us", "eu", "apac", "us", "eu"}),
		ursus.Values("qty", []int32{10, 3, 25, 7, 40, 1}),
		ursus.Values("price", []float64{9.5, 3.0, 12.25, 7.0, 1.5, 20.0}),
		ursus.Values("note", []string{"a", "b", "c", "d", "e", "f"}),
		ursus.ValuesNullable("discount", []float64{0.1, 0, 0.25, 0, 0.5, 0},
			[]bool{true, false, true, false, true, false}),
	)
}

// TestWalkingSkeleton is the target of step 1: a query that goes all the way
// through the real logical plan, the optimizer, the evaluator and the operator
// pipeline, and comes back with the right rows.
func TestWalkingSkeleton(t *testing.T) {
	df, err := frame().
		Filter(ursus.Col("price").Gt(5)).
		Select(ursus.Col("id"), ursus.Col("price")).
		Collect(t.Context())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	// The "!" marks a non-nullable column. An in-memory source holds all of its
	// data, so it can state truthfully that id and price contain no nulls; a
	// Parquet source will read the same fact from its schema. Only `discount`,
	// which really does have nulls, is nullable.
	if got, want := df.Schema().String(), "{id: Int64!, price: Float64!}"; got != want {
		t.Errorf("schema = %s, want %s", got, want)
	}
	if df.Height() != 4 {
		t.Fatalf("height = %d, want 4 (rows with price > 5)", df.Height())
	}

	ids, err := df.Column[int64]("id")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids.Values(), []int64{1, 3, 4, 6}; !equal(got, want) {
		t.Errorf("id = %v, want %v", got, want)
	}

	prices, err := df.Column[float64]("price")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := prices.Values(), []float64{9.5, 12.25, 7.0, 20.0}; !equal(got, want) {
		t.Errorf("price = %v, want %v", got, want)
	}
}

// TestProjectionReachesTheScan is the architectural claim. Without it the lazy
// plan is decoration: the query would read all six columns and discard four.
func TestProjectionReachesTheScan(t *testing.T) {
	plan, err := frame().
		Filter(ursus.Col("price").Gt(5)).
		Select(ursus.Col("id"), ursus.Col("price")).
		Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	// No PROJECT node: once the scan is narrowed to [id, price], selecting exactly
	// those two in that order is a no-op and the simplification rule removes it.
	// The architectural claim is the LAST line and is untouched — the projection
	// still reaches the scan, and the node that used to carry it is now redundant
	// precisely BECAUSE it did.
	want := `FILTER [(col("price") > lit(5))]
  MEMORY SCAN 6 rows
    projection: [id, price] (2/6 cols)
`
	if plan != want {
		t.Errorf("optimized plan:\n%s\nwant:\n%s", plan, want)
	}

	// And the unoptimized form, to show what the rule actually bought.
	raw, err := frame().
		Filter(ursus.Col("price").Gt(5)).
		Select(ursus.Col("id"), ursus.Col("price")).
		Explain(t.Context(), ursus.Optimized(false))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "projection: * (6 cols)") {
		t.Errorf("unoptimized plan should read every column:\n%s", raw)
	}
}

// TestNullSemantics pins the rules that are silently wrong when they are wrong.
func TestNullSemantics(t *testing.T) {
	t.Run("comparison with null is null, so Filter drops the row", func(t *testing.T) {
		// discount is null on rows 2, 4 and 6. `discount > 0.2` is therefore null
		// there, and a null predicate drops the row — it does not keep it and does
		// not treat it as false-but-present.
		df, err := frame().
			Filter(ursus.Col("discount").Gt(0.2)).
			Select(ursus.Col("id")).
			Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		ids, _ := df.Column[int64]("id")
		if got, want := ids.Values(), []int64{3, 5}; !equal(got, want) {
			t.Errorf("id = %v, want %v", got, want)
		}
	})

	t.Run("Filter(p) and Filter(Not(p)) do not partition", func(t *testing.T) {
		// Three rows have a null discount, so they are dropped by both. This is
		// SQL's behaviour and it surprises people, which is exactly why it is
		// pinned by a test.
		p := ursus.Col("discount").Gt(0.2)

		yes, err := frame().Filter(p).Count(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		no, err := frame().Filter(p.Not()).Count(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if yes+no != 3 {
			t.Errorf("p kept %d and !p kept %d, want 3 total (3 null rows dropped by both)", yes, no)
		}
	})

	t.Run("arithmetic propagates null", func(t *testing.T) {
		df, err := frame().
			Select(ursus.Col("price").Mul(ursus.Col("discount")).Alias("off")).
			Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		off, err := df.Column[float64]("off")
		if err != nil {
			t.Fatal(err)
		}
		wantValid := []bool{true, false, true, false, true, false}
		for i, w := range wantValid {
			if _, ok := off.Get(i); ok != w {
				t.Errorf("row %d valid = %v, want %v", i, ok, w)
			}
		}
	})

	t.Run("IsNull is never null; IsNan is", func(t *testing.T) {
		df, err := frame().
			Select(
				ursus.Col("discount").IsNull().Alias("missing"),
				ursus.Col("discount").IsNan().Alias("nan"),
			).
			Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}

		missing, _ := df.Column[bool]("missing")
		for i := range 6 {
			if _, ok := missing.Get(i); !ok {
				t.Errorf("IsNull row %d is null; it must always be valid", i)
			}
		}
		// null.IsNan() is NULL, not false: a missing value is not a float that
		// could be tested. This is the null-vs-NaN distinction most libraries
		// get wrong.
		nan, _ := df.Column[bool]("nan")
		if _, ok := nan.Get(1); ok {
			t.Error("null.IsNan() must be null, not false")
		}
	})

	t.Run("Kleene: false AND null is false", func(t *testing.T) {
		// price > 100 is false everywhere. AND-ing with a null predicate must
		// still be false-and-valid, not null — false is the absorbing element.
		df, err := frame().
			Select(
				ursus.Col("price").Gt(100).And(ursus.Col("discount").Gt(0.2)).Alias("k"),
			).
			Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		k, _ := df.Column[bool]("k")
		for i := range 6 {
			v, ok := k.Get(i)
			if !ok {
				t.Fatalf("row %d is null; false AND null must be false", i)
			}
			if v {
				t.Fatalf("row %d is true, want false", i)
			}
		}
	})

	t.Run("EqMissing treats null as a value", func(t *testing.T) {
		df, err := frame().
			Select(ursus.Col("discount").EqMissing(ursus.Col("discount")).Alias("same")).
			Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		same, _ := df.Column[bool]("same")
		for i := range 6 {
			v, ok := same.Get(i)
			if !ok || !v {
				t.Errorf("row %d: EqMissing with itself = (%v, valid=%v), want (true, true)", i, v, ok)
			}
		}
	})
}

// TestScalarLifting checks the ergonomic payoff of the Operand union: no Lit()
// wrapper at the call site, for every scalar width the constraint enumerates.
func TestScalarLifting(t *testing.T) {
	ctx := t.Context()

	cases := []struct {
		name string
		e    ursus.Expr
		want int64
	}{
		{"untyped int constant", ursus.Col("price").Gt(5), 4},
		{"untyped float constant", ursus.Col("price").Gt(5.0), 4},
		{"typed float64", ursus.Col("price").Gt(float64(5)), 4},
		{"typed int32 against an int32 column", ursus.Col("qty").Gt(int32(9)), 3},
		{"string", ursus.Col("region").Eq("eu"), 3},
		// price=[9.5,3,12.25,7,1.5,20] vs qty=[10,3,25,7,40,1]: only the last row
		// has price > qty. Note the Int32 column is promoted to Float64 first.
		{"expression operand", ursus.Col("price").Gt(ursus.Col("qty")), 1},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n, err := frame().Filter(c.e).Count(ctx)
			if err != nil {
				t.Fatalf("%s: %v", c.e, err)
			}
			if n != c.want {
				t.Errorf("%s matched %d rows, want %d", c.e, n, c.want)
			}
		})
	}
}

// TestImmutability: the most common lazy-frame idiom.
func TestImmutability(t *testing.T) {
	base := frame()

	a := base.Filter(ursus.Col("price").Gt(10))
	b := base.Filter(ursus.Col("price").Lt(5))

	na, err := a.Count(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	nb, err := b.Count(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	nbase, err := base.Count(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if na != 2 || nb != 2 || nbase != 6 {
		t.Errorf("derived frames interfered: a=%d b=%d base=%d, want 2/2/6", na, nb, nbase)
	}
}

// TestErrorQuality: a typo must produce a diagnosis, not a stack trace.
func TestErrorQuality(t *testing.T) {
	_, err := frame().Select(ursus.Col("pirce")).Collect(t.Context())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, uerr.ErrSchema) {
		t.Errorf("wrong error kind: %v", err)
	}
	msg := err.Error()
	for _, want := range []string{`unknown column "pirce"`, `did you mean: "price"?`, "available:"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message is missing %q:\n%s", want, msg)
		}
	}
}

func TestDeferredErrorSurvivesChaining(t *testing.T) {
	// A bad regex has nowhere to go at call time. It must ride the expression and
	// surface at Collect rather than panicking or being silently dropped.
	lf := frame().
		Select(ursus.ColRegex("(unclosed")).
		Filter(ursus.Col("id").Gt(0))

	if _, err := lf.Collect(t.Context()); err == nil {
		t.Fatal("a deferred regex error must surface at Collect")
	}
}

// TestBatchSizeIndependence: batch size is a tuning knob, never a semantic one.
func TestBatchSizeIndependence(t *testing.T) {
	var reference []int64
	for _, size := range []int{1, 2, 3, 5, 8192} {
		df, err := frame().
			Filter(ursus.Col("price").Gt(5)).
			Select(ursus.Col("id")).
			Collect(t.Context(), ursus.WithBatchSize(size), ursus.WithVerify())
		if err != nil {
			t.Fatalf("batch size %d: %v", size, err)
		}
		ids, _ := df.Column[int64]("id")
		got := ids.Values()
		if reference == nil {
			reference = got
			continue
		}
		if !equal(got, reference) {
			t.Errorf("batch size %d gave %v, want %v", size, got, reference)
		}
	}
}

func TestCollectBatches(t *testing.T) {
	total := 0
	for df, err := range frame().Filter(ursus.Col("qty").Gt(5)).CollectBatches(t.Context()) {
		if err != nil {
			t.Fatal(err)
		}
		total += df.Height()
	}
	if total != 4 {
		t.Errorf("streamed %d rows, want 4", total)
	}
}

func TestCollectInto(t *testing.T) {
	type Row struct {
		ID     int64   `ursus:"id"`
		Region string  `ursus:"region"`
		Price  float64 `ursus:"price"`
	}

	rows, err := frame().
		Filter(ursus.Col("region").Eq("eu")).
		Select(ursus.Col("id"), ursus.Col("region"), ursus.Col("price")).
		CollectInto[Row](t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	if rows[0] != (Row{ID: 1, Region: "eu", Price: 9.5}) {
		t.Errorf("row 0 = %+v", rows[0])
	}
}

func TestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := frame().Select(ursus.Col("id")).Collect(ctx); err == nil {
		t.Fatal("Collect must respect a cancelled context")
	}
}

func TestComputedColumns(t *testing.T) {
	df, err := frame().
		Select(
			ursus.Col("id"),
			ursus.Col("price").Mul(ursus.Col("qty")).Alias("revenue"),
			ursus.Col("price").Div(2).Alias("half"),
			ursus.Lit(1).Add(1).Alias("two"),
		).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	// qty is Int32 and price is Float64, so the product promotes to Float64.
	if got, want := df.Schema().String(),
		"{id: Int64!, revenue: Float64!, half: Float64!, two: Int64!}"; got != want {
		t.Errorf("schema = %s, want %s", got, want)
	}

	rev, _ := df.Column[float64]("revenue")
	if got, want := rev.Values()[0], 95.0; got != want {
		t.Errorf("revenue[0] = %v, want %v", got, want)
	}

	// A literal-only expression broadcasts to the frame height.
	two, _ := df.Column[int64]("two")
	if two.Len() != 6 {
		t.Errorf("literal column has %d rows, want 6", two.Len())
	}
}

func equal[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
