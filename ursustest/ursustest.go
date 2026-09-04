// Package ursustest provides assertions for testing code that uses ursus.
//
// It ships in v0.1 rather than later because every other package's tests need it,
// and because a dataframe library without a good AssertFrameEqual pushes each user
// into writing their own worse one.
package ursustest

import (
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ursus"
	"ursus/dtype"
	"ursus/internal/data"
)

// Option configures an assertion.
type Option func(*config)

type config struct {
	checkRowOrder    bool
	checkColumnOrder bool
	checkDTypes      bool
	checkNullability bool
	absTol, relTol   float64
}

func defaults() config {
	return config{
		checkRowOrder:    true,
		checkColumnOrder: true,
		checkDTypes:      true,
		// Nullability is a schema-level claim that often differs harmlessly
		// between a literal expectation and a computed result, so it is off by
		// default and available when it matters.
		checkNullability: false,
	}
}

// IgnoreRowOrder compares frames as multisets of rows.
func IgnoreRowOrder() Option { return func(c *config) { c.checkRowOrder = false } }

// IgnoreColumnOrder matches columns by name rather than by position.
func IgnoreColumnOrder() Option { return func(c *config) { c.checkColumnOrder = false } }

// IgnoreDTypes compares values without requiring identical types.
func IgnoreDTypes() Option { return func(c *config) { c.checkDTypes = false } }

// CheckNullability additionally requires the nullability flags to match.
func CheckNullability() Option { return func(c *config) { c.checkNullability = true } }

// WithTolerance compares floats approximately.
//
// Without it floats are compared BIT-EXACTLY, which is the right default for a
// library whose SIMD and scalar paths are required to agree bit for bit: an
// approximate default would hide exactly the bugs the differential tests exist to
// find. Reach for this only when comparing against a value computed a different way.
func WithTolerance(abs, rel float64) Option {
	return func(c *config) { c.absTol, c.relTol = abs, rel }
}

// AssertSchemaEqual fails if the schemas differ.
func AssertSchemaEqual(t testing.TB, got, want *ursus.Schema, opts ...Option) {
	t.Helper()
	cfg := apply(opts)

	if got.Len() != want.Len() {
		t.Fatalf("schema has %d columns, want %d\n got:  %s\n want: %s",
			got.Len(), want.Len(), got, want)
	}
	for i, wf := range want.All() {
		gf := got.Field(i)
		if cfg.checkColumnOrder && gf.Name != wf.Name {
			t.Fatalf("column %d is %q, want %q\n got:  %s\n want: %s",
				i, gf.Name, wf.Name, got, want)
		}
		if !cfg.checkColumnOrder {
			var ok bool
			if gf, ok = got.ByName(wf.Name); !ok {
				t.Fatalf("missing column %q\n got: %s", wf.Name, got)
			}
		}
		if cfg.checkDTypes && gf.Type != wf.Type {
			t.Fatalf("column %q is %s, want %s", wf.Name, gf.Type, wf.Type)
		}
		if cfg.checkNullability && gf.Nullable != wf.Nullable {
			t.Fatalf("column %q nullable = %v, want %v", wf.Name, gf.Nullable, wf.Nullable)
		}
	}
}

// AssertFrameEqual fails if the frames differ, reporting the first difference
// with enough context to diagnose it.
func AssertFrameEqual(t testing.TB, got, want *ursus.DataFrame, opts ...Option) {
	t.Helper()
	cfg := apply(opts)

	AssertSchemaEqual(t, got.Schema(), want.Schema(), opts...)

	if got.Height() != want.Height() {
		t.Fatalf("frame has %d rows, want %d\n got:\n%s\nwant:\n%s",
			got.Height(), want.Height(), got, want)
	}

	// A configured tolerance moves float columns OFF the exact-rendering path and
	// onto assertFloatsClose. Without that split WithTolerance cannot do anything
	// at all: the rendered rows are compared first, a one-ulp difference fails
	// there, and the numeric pass below is never reached. The option, its config
	// fields and assertFloatsClose were all written and had no caller until step
	// 17 needed one, which is exactly how the gap survived.
	approx := cfg.absTol > 0 || cfg.relTol > 0
	if approx && !cfg.checkRowOrder {
		// assertFloatsClose walks rows POSITIONALLY, so it cannot align frames whose
		// rows have been sorted into agreement by their rendered form — it would
		// compare unrelated rows and report enormous differences. Refusing beats
		// either silently skipping the numeric pass or reporting nonsense; sort the
		// query instead, which is what a caller wanting both actually means.
		t.Fatal("ursustest: IgnoreRowOrder and WithTolerance cannot be combined — " +
			"sort the frames on a key instead, so the rows line up")
	}

	gotRows := renderRows(got, approx)
	wantRows := renderRows(want, approx)

	if !cfg.checkRowOrder {
		sortStrings(gotRows)
		sortStrings(wantRows)
	}

	for i := range wantRows {
		if gotRows[i] != wantRows[i] {
			t.Fatalf("row %d differs\n got:  %s\n want: %s\n\nfull frames:\n%s\nwant:\n%s",
				i, gotRows[i], wantRows[i], got, want)
		}
	}

	if approx {
		assertFloatsClose(t, got, want, cfg)
	}
}

// AssertSeriesEqual fails if two typed columns differ.
func AssertSeriesEqual[T comparable](t testing.TB, got, want *data.Series[T]) {
	t.Helper()
	if got.Len() != want.Len() {
		t.Fatalf("series %q has %d values, want %d", got.Name(), got.Len(), want.Len())
	}
	for i := range want.Len() {
		gv, gok := got.Get(i)
		wv, wok := want.Get(i)
		if gok != wok {
			t.Fatalf("series %q row %d: valid = %v, want %v", got.Name(), i, gok, wok)
		}
		if gok && gv != wv {
			t.Fatalf("series %q row %d = %v, want %v", got.Name(), i, gv, wv)
		}
	}
}

// AssertPlan compares a frame's optimized plan against a golden file.
//
// Plan snapshots are how optimizer regressions get caught. A query that still
// returns the right answer while reading forty columns instead of two is a
// regression, and nothing else in a test suite notices.
//
// Run `go test -update` to rewrite the golden files.
func AssertPlan(t testing.TB, lf *ursus.LazyFrame, goldenPath string, opts ...ursus.ExplainOption) {
	t.Helper()

	got, err := lf.Explain(contextFor(t), opts...)
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}

	if *update {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("updated %s", goldenPath)
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("reading golden plan %s: %v\n\nrun with -update to create it. Plan was:\n%s",
			goldenPath, err, got)
	}
	if got != string(want) {
		t.Errorf("plan does not match %s\n got:\n%s\nwant:\n%s\n\nrun with -update if the change is intended",
			goldenPath, got, want)
	}
}

// --- internals ---------------------------------------------------------------

func apply(opts []Option) config {
	cfg := defaults()
	for _, o := range opts {
		o(&cfg)
	}
	return cfg
}

// renderRows renders one string per row.
//
// skipFloats leaves floating-point columns OUT of the rendering, for the
// tolerance path only — see AssertFrameEqual for why that is the difference
// between a working WithTolerance and a decorative one.
func renderRows(df *ursus.DataFrame, skipFloats bool) []string {
	b := df.Batch()
	out := make([]string, b.Rows())
	var sb strings.Builder
	for r := range b.Rows() {
		sb.Reset()
		first := true
		for c := range b.NumCols() {
			if skipFloats && isFloat(b.Column(c).DType()) {
				continue
			}
			if !first {
				sb.WriteString(" | ")
			}
			first = false
			sb.WriteString(cell(b.Column(c), r))
		}
		out[r] = sb.String()
	}
	return out
}

func isFloat(dt dtype.DataType) bool {
	return dt.ID() == dtype.TypeFloat32 || dt.ID() == dtype.TypeFloat64
}

func cell(c *data.Column, row int) string {
	if !c.IsValid(row) {
		return "null"
	}
	return renderValue(c, row)
}

func assertFloatsClose(t testing.TB, got, want *ursus.DataFrame, cfg config) {
	t.Helper()
	gb, wb := got.Batch(), want.Batch()
	for ci := range wb.NumCols() {
		gs, err1 := data.TypedColumn[float64](gb.Column(ci))
		ws, err2 := data.TypedColumn[float64](wb.Column(ci))
		if err1 != nil || err2 != nil {
			continue
		}
		for r := range wb.Rows() {
			gv, gok := gs.Get(r)
			wv, wok := ws.Get(r)
			if gok != wok {
				t.Errorf("column %q row %d: valid = %v, want %v", gs.Name(), r, gok, wok)
				continue
			}
			if !gok || close(gv, wv, cfg.absTol, cfg.relTol) {
				continue
			}
			t.Errorf("column %q row %d = %v, want %v (diff %v, tolerance abs=%v rel=%v)",
				gs.Name(), r, gv, wv, math.Abs(gv-wv), cfg.absTol, cfg.relTol)
		}
	}
}

func close(a, b, abs, rel float64) bool {
	if a == b {
		return true
	}
	if math.IsNaN(a) && math.IsNaN(b) {
		return true
	}
	d := math.Abs(a - b)
	if abs > 0 && d <= abs {
		return true
	}
	return rel > 0 && d <= rel*math.Max(math.Abs(a), math.Abs(b))
}

// sortStrings orders the rendered rows for an order-insensitive comparison.
//
// slices.Sort, not the hand-rolled insertion sort this used to be: IgnoreRowOrder
// is exactly what a spilled group-by's equivalence test needs, and O(n²) over a
// few thousand groups is minutes rather than milliseconds.
func sortStrings(xs []string) { slices.Sort(xs) }
