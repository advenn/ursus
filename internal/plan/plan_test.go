package plan_test

import (
	"context"
	"strings"
	"testing"

	"ursus/dtype"
	"ursus/internal/expr"
	"ursus/internal/plan"
)

// fakeSource proves the central claim: a plan can be built, resolved, optimized
// and explained with NO data and NO I/O. Everything in this file runs against a
// schema and nothing else.
type fakeSource struct {
	schema *dtype.Schema
	caps   plan.Caps
	calls  int // how many times Schema was asked; Resolve should ask once
}

func (f *fakeSource) Name() string     { return "memory" }
func (f *fakeSource) Describe() string { return "fake" }
func (f *fakeSource) Caps() plan.Caps  { return f.caps }

func (f *fakeSource) Schema(context.Context) (*dtype.Schema, error) {
	f.calls++
	return f.schema, nil
}

// wide is a 6-column source; pushdown tests assert we stop reading most of it.
func wide() *fakeSource {
	return &fakeSource{
		schema: dtype.MustSchema(
			dtype.NotNull("id", dtype.Int64),
			dtype.Of("ts", dtype.Datetime(dtype.Micro, "UTC")),
			dtype.Of("region", dtype.String),
			dtype.Of("qty", dtype.Int32),
			dtype.Of("price", dtype.Float64),
			dtype.Of("note", dtype.String),
		),
		caps: plan.Caps{Projection: true},
	}
}

func col(n string) expr.Node { return &expr.Col{Name: n} }
func lit(v any, d dtype.DataType) expr.Node {
	return &expr.Lit{Value: v, DT: d}
}

func resolve(t *testing.T, n plan.Node) plan.Node {
	t.Helper()
	got, err := plan.Resolve(t.Context(), n)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return got
}

func optimize(t *testing.T, n plan.Node) plan.Node {
	t.Helper()
	return optimizeWith(t, n, plan.DefaultFlags())
}

func optimizeWith(t *testing.T, n plan.Node, f plan.Flags) plan.Node {
	t.Helper()
	o := plan.NewOptimizer()
	o.Verify = true // schema preservation checked after every rule
	got, err := o.Run(n, f)
	if err != nil {
		t.Fatalf("Optimize: %v", err)
	}
	return got
}

func TestSchemaWithoutData(t *testing.T) {
	src := wide()
	p := &plan.Project{
		Input: &plan.Filter{
			Input: &plan.Scan{Src: src},
			Preds: []expr.Node{
				&expr.Binary{Op: expr.OpGt, L: col("price"), R: lit(5.0, dtype.Float64)},
			},
		},
		Exprs: []expr.Node{
			col("id"),
			&expr.Alias{
				Child: &expr.Binary{Op: expr.OpMul, L: col("qty"), R: col("price")},
				Name:  "total",
			},
		},
	}

	r := resolve(t, p)
	s, err := r.Schema()
	if err != nil {
		t.Fatal(err)
	}
	// qty is Int32 and price is Float64, so the product promotes to Float64, and
	// it is nullable because both inputs are.
	if got, want := s.String(), "{id: Int64!, total: Float64}"; got != want {
		t.Errorf("schema = %s, want %s", got, want)
	}
	if src.calls != 1 {
		t.Errorf("source schema fetched %d times, want exactly 1", src.calls)
	}
}

// TestProjectionPushdown is the test that justifies the lazy architecture.
func TestProjectionPushdown(t *testing.T) {
	src := wide()
	p := &plan.Project{
		Input: &plan.Filter{
			Input: &plan.Scan{Src: src},
			Preds: []expr.Node{
				&expr.Binary{Op: expr.OpGt, L: col("qty"), R: lit(int32(5), dtype.Int32)},
			},
		},
		Exprs: []expr.Node{col("id"), col("price")},
	}

	r := optimize(t, resolve(t, p))

	var scan *plan.Scan
	plan.Walk(r, func(n plan.Node) bool {
		if s, ok := n.(*plan.Scan); ok {
			scan = s
		}
		return true
	})
	if scan == nil {
		t.Fatal("no scan in the optimized plan")
	}

	// The filter needs qty, the projection needs id and price. Nothing needs ts,
	// region or note — 3 of 6 columns are never read.
	want := []string{"id", "qty", "price"}
	if len(scan.Projection) != len(want) {
		t.Fatalf("projection = %v, want %v", scan.Projection, want)
	}
	for i := range want {
		if scan.Projection[i] != want[i] {
			t.Fatalf("projection = %v, want %v (must be in SOURCE order)", scan.Projection, want)
		}
	}
}

// TestPushdownSoundness is the property that keeps the conservative default
// honest as node types are added: every scan must supply every column that any
// expression above it reads.
//
// # Simplification is OFF here, and that is the point
//
// This is a property of PUSHDOWN, and it needs each case's shape intact to have a
// property to test. Case 0 is the demonstration: `Project([id], Scan(wide))` is
// not an identity projection as written, but projection pushdown narrows the scan
// to [id] and then it is — so with simplification on, the Project is removed, no
// expression remains anywhere in the tree, `needed` is empty and the loop below
// never executes. The test would pass by having nothing left to check.
//
// The counter is checked at the end for exactly that reason. A test that silently
// stops testing is worse than one that fails, because it goes on reporting
// success for years.
func TestPushdownSoundness(t *testing.T) {
	flags := plan.DefaultFlags()
	flags.SimplifyExprs = false

	plans := []plan.Node{
		&plan.Project{
			Input: &plan.Scan{Src: wide()},
			Exprs: []expr.Node{col("id")},
		},
		&plan.Filter{
			Input: &plan.Project{
				Input: &plan.Scan{Src: wide()},
				Exprs: []expr.Node{col("id"), col("price"), col("qty")},
			},
			Preds: []expr.Node{
				&expr.Binary{Op: expr.OpGt, L: col("price"), R: lit(1.0, dtype.Float64)},
			},
		},
		&plan.Limit{
			Input: &plan.Filter{
				Input: &plan.Scan{Src: wide()},
				Preds: []expr.Node{
					&expr.Binary{Op: expr.OpEq, L: col("region"), R: lit("eu", dtype.String)},
				},
			},
			N: 10,
		},
		// Nested projections: the inner one must still get everything the outer
		// one ends up needing.
		&plan.Project{
			Input: &plan.Project{
				Input: &plan.Scan{Src: wide()},
				Exprs: []expr.Node{col("id"), col("qty"), col("price"), col("note")},
			},
			Exprs: []expr.Node{
				&expr.Alias{
					Child: &expr.Binary{Op: expr.OpAdd, L: col("qty"), R: col("price")},
					Name:  "sum",
				},
			},
		},
	}

	for i, p := range plans {
		r := optimizeWith(t, resolve(t, p), flags)
		checked := 0

		// Collect every column read anywhere in the tree.
		needed := map[string]bool{}
		plan.Walk(r, func(n plan.Node) bool {
			for _, e := range plan.Expressions(n) {
				for _, name := range expr.RootNames(e) {
					needed[name] = true
				}
			}
			return true
		})

		plan.Walk(r, func(n plan.Node) bool {
			s, ok := n.(*plan.Scan)
			if !ok || s.Projection == nil {
				return true
			}
			have := map[string]bool{}
			for _, c := range s.Projection {
				have[c] = true
			}
			for name := range needed {
				// Only columns the source actually has are its responsibility.
				if !s.Full.Has(name) {
					continue
				}
				checked++
				if !have[name] {
					t.Errorf("plan %d: scan projects %v but %q is read above it",
						i, s.Projection, name)
				}
			}
			return true
		})

		// The vacuity guard, and it has to be PER PLAN. A total across all four
		// would stay comfortably above zero while case 0 quietly contributed
		// nothing — which is exactly the failure being guarded against, so a
		// threshold that cannot see one empty plan among four is no guard at all.
		if checked == 0 {
			t.Errorf("plan %d: no column assertions ran — the case has gone vacuous, "+
				"because some rewrite removed the expressions it was meant to check", i)
		}
	}
}

func TestOptimizerPreservesSchema(t *testing.T) {
	p := &plan.Project{
		Input: &plan.Filter{
			Input: &plan.Scan{Src: wide()},
			Preds: []expr.Node{
				&expr.Binary{Op: expr.OpGt, L: col("qty"), R: lit(int32(0), dtype.Int32)},
			},
		},
		Exprs: []expr.Node{col("price"), col("id")},
	}
	r := resolve(t, p)

	before, err := r.Schema()
	if err != nil {
		t.Fatal(err)
	}
	after, err := optimize(t, r).Schema()
	if err != nil {
		t.Fatal(err)
	}
	if !before.Equal(after) {
		t.Errorf("optimizer changed the schema:\n before %s\n after  %s", before, after)
	}
}

// TestImmutability: optimizing must not disturb the input plan, because two
// LazyFrames may share it.
func TestImmutability(t *testing.T) {
	r := resolve(t, &plan.Project{
		Input: &plan.Scan{Src: wide()},
		Exprs: []expr.Node{col("id")},
	})

	before, err := plan.Explain(r, plan.DefaultExplainOptions())
	if err != nil {
		t.Fatal(err)
	}
	optimize(t, r)
	after, err := plan.Explain(r, plan.DefaultExplainOptions())
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Errorf("optimizing mutated the input plan:\n%s\n---\n%s", before, after)
	}
}

func TestExpansionInResolve(t *testing.T) {
	// ColDType(Float64) matches only `price` in the wide schema.
	p := &plan.Project{
		Input: &plan.Scan{Src: wide()},
		Exprs: []expr.Node{
			&expr.Rename{
				Child: &expr.Match{M: &expr.DTypeMatcher{Types: []dtype.DataType{dtype.Float64}}},
				Fn:    func(s string) string { return s + "_x2" },
				Label: `name.suffix("_x2")`,
			},
		},
	}
	r := resolve(t, p)

	s, err := r.Schema()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := s.String(), "{price_x2: Float64}"; got != want {
		t.Errorf("schema = %s, want %s", got, want)
	}

	// Crucially, pushdown must see through the expansion: an unexpanded matcher
	// would report reading no columns and the scan would project nothing.
	o := optimize(t, r)
	var scan *plan.Scan
	plan.Walk(o, func(n plan.Node) bool {
		if sc, ok := n.(*plan.Scan); ok {
			scan = sc
		}
		return true
	})
	if len(scan.Projection) != 1 || scan.Projection[0] != "price" {
		t.Errorf("projection after expansion = %v, want [price]", scan.Projection)
	}
}

func TestResolveErrors(t *testing.T) {
	cases := []struct {
		name string
		p    plan.Node
		want string
	}{
		{
			"non-boolean predicate",
			&plan.Filter{Input: &plan.Scan{Src: wide()}, Preds: []expr.Node{col("price")}},
			"predicate must be Boolean",
		},
		{
			"unknown column in select",
			&plan.Project{Input: &plan.Scan{Src: wide()}, Exprs: []expr.Node{col("nope")}},
			`unknown column "nope"`,
		},
		{
			"duplicate output names",
			&plan.Project{Input: &plan.Scan{Src: wide()}, Exprs: []expr.Node{
				&expr.Alias{Child: col("id"), Name: "x"},
				&expr.Alias{Child: col("qty"), Name: "x"},
			}},
			`both named "x"`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := plan.Resolve(t.Context(), c.p)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %v, want it to contain %q", err, c.want)
			}
		})
	}
}

func TestExplainGolden(t *testing.T) {
	p := &plan.Project{
		Input: &plan.Filter{
			Input: &plan.Scan{Src: wide()},
			Preds: []expr.Node{
				&expr.Binary{Op: expr.OpGt, L: col("qty"), R: lit(int32(5), dtype.Int32)},
			},
		},
		Exprs: []expr.Node{col("id"), col("price")},
	}

	r := resolve(t, p)

	unoptimized, err := plan.Explain(r, plan.DefaultExplainOptions())
	if err != nil {
		t.Fatal(err)
	}
	wantUnopt := `PROJECT [col("id"), col("price")]
  FILTER [(col("qty") > lit(5))]
    MEMORY SCAN fake
      projection: * (6 cols)
`
	if unoptimized != wantUnopt {
		t.Errorf("unoptimized plan:\n%s\nwant:\n%s", unoptimized, wantUnopt)
	}

	optimized, err := plan.Explain(optimize(t, r), plan.DefaultExplainOptions())
	if err != nil {
		t.Fatal(err)
	}
	wantOpt := `PROJECT [col("id"), col("price")]
  FILTER [(col("qty") > lit(5))]
    MEMORY SCAN fake
      projection: [id, qty, price] (3/6 cols)
`
	if optimized != wantOpt {
		t.Errorf("optimized plan:\n%s\nwant:\n%s", optimized, wantOpt)
	}
}

func TestResolveIsIdempotent(t *testing.T) {
	src := wide()
	p := &plan.Project{Input: &plan.Scan{Src: src}, Exprs: []expr.Node{col("id")}}

	r1 := resolve(t, p)
	r2 := resolve(t, r1)

	s1, _ := r1.Schema()
	s2, _ := r2.Schema()
	if !s1.Equal(s2) {
		t.Errorf("Resolve is not idempotent: %s vs %s", s1, s2)
	}
	if src.calls != 1 {
		t.Errorf("source schema fetched %d times across two Resolves, want 1", src.calls)
	}
}

func TestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := plan.Resolve(ctx, &plan.Project{
		Input: &plan.Scan{Src: wide()},
		Exprs: []expr.Node{col("id")},
	})
	if err == nil {
		t.Fatal("Resolve must respect a cancelled context")
	}
}
