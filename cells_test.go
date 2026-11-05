package ursus_test

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// The cell readers the root package's tests share, here since step 496 moved them
// out of the two files that held them (numodds_test.go, dtmore_test.go) when their
// hand tables became the oracle's.

// typedCells is the first column's values, a null nil, read as T.
func typedCells[T ursus.Literal](t *testing.T, q *ursus.LazyFrame) []any {
	t.Helper()
	df, err := q.Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	col, err := df.Column[T](df.Schema().Field(0).Name)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]any, df.Height())
	for i := range out {
		if v, ok := col.Get(i); ok {
			out[i] = v
		}
	}
	return out
}

// sameCells compares cells, a float by its bits, NaN with NaN.
func sameCells(got, want []any) bool {
	if len(got) != len(want) {
		return false
	}
	for i, w := range want {
		switch w := w.(type) {
		case float64:
			g, ok := got[i].(float64)
			if !ok || math.Float64bits(g) != math.Float64bits(w) && !(math.IsNaN(g) && math.IsNaN(w)) {
				return false
			}
		case float32:
			g, ok := got[i].(float32)
			if !ok || math.Float32bits(g) != math.Float32bits(w) && !(g != g && w != w) {
				return false
			}
		default:
			if got[i] != w {
				return false
			}
		}
	}
	return true
}

// floatsOf is a one-column answer's values, exactly, NaN for a null. The lines are
// split by hand: encoding/csv skips an empty line, which is a null here.
func floatsOf(t *testing.T, q *ursus.LazyFrame) []float64 {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(csvOf(t, q), "\n"), "\n")
	out := make([]float64, 0, len(lines)-1)
	for _, r := range lines[1:] {
		if r == "" {
			out = append(out, math.NaN())
			continue
		}
		f, err := strconv.ParseFloat(r, 64)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, f)
	}
	return out
}

func sameFloats(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] && !(math.IsNaN(a[i]) && math.IsNaN(b[i])) {
			return false
		}
	}
	return true
}
