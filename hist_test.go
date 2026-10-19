package ursus_test

import (
	"math"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestHist is tier 2 of v0.6.6-scope.md (step 286): Polars' hist. Every answer is
// Polars 1.44.1's, the edges by their bits.
func TestHist(t *testing.T) {
	c := ursus.Col
	x := c("x")
	nan := math.NaN()
	h := ursus.Frame(ursus.ValuesNullable("x", []float64{1, 2, 2.5, 4, 8, 0, nan},
		[]bool{true, true, true, true, true, false, true}))
	u := func(v ...int) []any {
		out := make([]any, len(v))
		for i, n := range v {
			out[i] = uint32(n)
		}
		return out
	}
	// parts is a hist's breakpoints, categories and counts, a field each.
	parts := func(q *ursus.LazyFrame, bp, cat bool) (b, k, n []any) {
		t.Helper()
		f := func(name string) *ursus.LazyFrame { return q.Select(x.Struct().Field(name).Alias("x")) }
		if bp {
			b = typedCells[float64](t, f("breakpoint"))
		}
		if cat {
			k = typedCells[string](t, f("category"))
		}
		return b, k, typedCells[uint32](t, f("count"))
	}
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want []any
	}{
		{"10 bins", h.Select(x.Hist()), u(1, 1, 1, 0, 1, 0, 0, 0, 0, 1)},
		{"3 bins", h.Select(x.Hist(ursus.HistBinCount(3))), u(3, 1, 1)},
		{"no bin", h.Select(x.Hist(ursus.HistBinCount(0))), u()},
		{"edges", h.Select(x.Hist(ursus.HistBins(0, 2, 5))), u(2, 2)},
		{"one edge, no bin", h.Select(x.Hist(ursus.HistBins(3))), u()},
	} {
		if got := typedCells[uint32](t, tc.q); !sameCells(got, tc.want) {
			t.Errorf("%s:\ngot  %v\nwant %v", tc.name, got, tc.want)
		}
	}
	for _, tc := range []struct {
		name         string
		q            *ursus.LazyFrame
		bp, cat      bool
		wantB, wantK []any
		wantN        []any
	}{
		{"3 bins, both", h.Select(x.Hist(ursus.HistBinCount(3), ursus.HistBreakpoint(), ursus.HistCategory())), true, true,
			[]any{3.3333333333333335, 5.666666666666667, 8.0},
			[]any{"[1.0, 3.333333]", "(3.333333, 5.666667]", "(5.666667, 8.0]"}, u(3, 1, 1)},
		{"4 bins, both", h.Select(x.Hist(ursus.HistBinCount(4), ursus.HistBreakpoint(), ursus.HistCategory())), true, true,
			[]any{2.75, 4.5, 6.25, 8.0}, []any{"[1.0, 2.75]", "(2.75, 4.5]", "(4.5, 6.25]", "(6.25, 8.0]"}, u(3, 1, 0, 1)},
		{"edges, both", h.Select(x.Hist(ursus.HistBins(0, 2, 5), ursus.HistBreakpoint(), ursus.HistCategory())), true, true,
			[]any{2.0, 5.0}, []any{"[0.0, 2.0]", "(2.0, 5.0]"}, u(2, 2)},
		{"edges, a breakpoint", h.Select(x.Hist(ursus.HistBins(0, 2, 5), ursus.HistBreakpoint())), true, false,
			[]any{2.0, 5.0}, nil, u(2, 2)},
		{"on the edges", ursus.Frame(ursus.Values("x", []float64{0, 2, 5, 5.0001, -1})).
			Select(x.Hist(ursus.HistBins(0, 2, 5), ursus.HistBreakpoint())), true, false, []any{2.0, 5.0}, nil, u(2, 1)},
		{"integers", ursus.Frame(ursus.Values("x", []int64{1, 2, 2, 3, 10})).Select(x.Hist(ursus.HistBinCount(4), ursus.HistBreakpoint())),
			true, false, []any{3.25, 5.5, 7.75, 10.0}, nil, u(4, 0, 0, 1)},
		{"alike", ursus.Frame(ursus.Values("x", []float64{5, 5})).Select(x.Hist(ursus.HistBinCount(2), ursus.HistBreakpoint())),
			true, false, []any{5.0, 5.5}, nil, u(2, 0)},
		{"no rows", ursus.Frame(ursus.Values("x", []float64{})).Select(x.Hist(ursus.HistBinCount(2), ursus.HistBreakpoint())),
			true, false, []any{0.5, 1.0}, nil, u(0, 0)},
		{"one bin", h.Select(x.Hist(ursus.HistBinCount(1), ursus.HistBreakpoint())), true, false, []any{8.0}, nil, u(5)},
		// The last edge is the greatest value itself, where 5 widths from -5.7 would
		// be 2.999999999999999.
		{"the last edge the greatest", ursus.Frame(ursus.Values("x", []float64{-5.7, -1, 3})).
			Select(x.Hist(ursus.HistBinCount(5), ursus.HistBreakpoint())), true, false,
			[]any{-3.9600000000000004, -2.2200000000000006, -0.4800000000000013, 1.259999999999999, 3.0}, nil, u(1, 0, 1, 0, 1)},
		{"a Float32", h.Select(x.Cast(ursus.Float32).Hist(ursus.HistBinCount(2), ursus.HistBreakpoint())),
			true, false, []any{4.5, 8.0}, nil, u(4, 1)},
		{"NaN alone", ursus.Frame(ursus.Values("x", []float64{nan, nan})).Select(x.Hist(ursus.HistBinCount(2), ursus.HistBreakpoint())),
			true, false, []any{nan, nan}, nil, u(0, 0)},
		{"NaN once", ursus.Frame(ursus.Values("x", []float64{nan})).Select(x.Hist(ursus.HistBinCount(2), ursus.HistBreakpoint())),
			true, false, []any{nan, nan}, nil, u(0, 0)},
		{"NaN beside", ursus.Frame(ursus.Values("x", []float64{nan, 1, 3})).Select(x.Hist(ursus.HistBinCount(2), ursus.HistBreakpoint())),
			true, false, []any{2.0, 3.0}, nil, u(1, 1)},
		// Polars' float display: one decimal for a whole number, six or four in
		// scientific notation for a long one.
		{"the labels", ursus.Frame(ursus.Values("x", []float64{0.5})).Select(x.Hist(ursus.HistCategory(),
			ursus.HistBins(-1234567.891, -2, 1e-7, 0.000000123456, 0.1, 12.0000000001, 1234567, 1234567.891, 1e7, 1.5e300))), false, true,
			nil, []any{"[-1.2346e6, -2.0]", "(-2.0, 0.0000001]", "(0.0000001, 1.2346e-7]", "(1.2346e-7, 0.1]", "(0.1, 12.0]",
				"(12.0, 1.234567e6]", "(1.234567e6, 1.2346e6]", "(1.2346e6, 1e7]", "(1e7, 1.5000e300]"}, u(0, 0, 0, 0, 1, 0, 0, 0, 0)},
	} {
		b, k, n := parts(tc.q, tc.bp, tc.cat)
		if !sameCells(b, tc.wantB) || !sameCells(k, tc.wantK) || !sameCells(n, tc.wantN) {
			t.Errorf("%s:\ngot  %v %v %v\nwant %v %v %v", tc.name, b, k, n, tc.wantB, tc.wantK, tc.wantN)
		}
	}
	twenty := make([]float64, 20)
	for i := range twenty {
		twenty[i] = float64(i)
	}
	b, _, n := parts(ursus.Frame(ursus.Values("x", twenty)).Select(x.Hist(ursus.HistBreakpoint())), true, false)
	if want := []any{1.9, 3.8, 5.699999999999999, 7.6, 9.5, 11.399999999999999, 13.299999999999999, 15.2, 17.099999999999998, 19.0}; !sameCells(b, want) ||
		!sameCells(n, u(2, 2, 2, 2, 2, 2, 2, 2, 2, 2)) {
		t.Errorf("0 to 19 in 10 bins:\ngot  %v %v\nwant %v", b, n, want)
	}
	// By group, a list a group.
	g := ursus.Frame(ursus.Values("x", []float64{1, 2, 3, 4}), ursus.Values("g", []int64{1, 1, 2, 2})).
		GroupBy(c("g")).Agg(x.Hist(ursus.HistBinCount(2)).List().Eval(ursus.Element().Cast(ursus.String)).List().Join("|")).
		Sort(ursus.Asc(c("g"))).Select(x)
	if got := csvOf(t, g); got != "x\n1|1\n1|1\n" {
		t.Errorf("by group: %q", got)
	}
	if s, err := h.Select(x.Hist()).CollectSchema(t.Context()); err != nil || s.Field(0).Name != "x" || s.Field(0).Type != ursus.Uint32 {
		t.Errorf("hist's name and type: %v, %v", s, err)
	}
	for name, tc := range map[string]struct {
		q    *ursus.LazyFrame
		want string
	}{
		// Polars: "bins must increase monotonically".
		"edges falling": {h.Select(x.Hist(ursus.HistBins(5, 0, 2))), "must increase"},
		// Polars: "can only provide one of `bin_count` or `bins`".
		"both": {h.Select(x.Hist(ursus.HistBins(0, 1), ursus.HistBinCount(2))), "not both"},
		// Polars: "'hist' is only supported for numeric data".
		"text":   {ursus.Frame(ursus.Values("x", []string{"a"})).Select(x.Hist()), "only defined for numbers"},
		"a date": {h.Select(x.Cast(ursus.Int32).Cast(ursus.Date).Hist()), "only defined for numbers"},
	} {
		if _, err := tc.q.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}
