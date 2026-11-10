package ursus_test

import (
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
)

// generateFixture is the frame of the generate group's cases (oracle_generate_test.go):
// int_range, linear_space, repeat, ones and zeros, a column of their own length, and
// their per-row forms, each Polars 2.1.0's answer (G1 of v0.6.6-scope.md, step 263).
func generateFixture() *ursus.LazyFrame {
	return ursus.Frame(ursus.Values("a", []int64{1, 2, 3}),
		ursus.ValuesNullable("s", []int64{0, 5, 0}, []bool{true, true, false}),
		ursus.Values("e", []int64{3, 2, 4}), ursus.Values("st", []int64{1, -1, 2}),
		ursus.Values("g", []int64{1, 1, 2}))
}

// TestGeneratorsRefuse: a step of zero (Polars panics on int_ranges'), a negative
// count, a float where integers are taken (Polars casts it), ends of two kinds, a
// generator of another length beside a column, and one in .Over.
func TestGeneratorsRefuse(t *testing.T) {
	c := ursus.Col
	lf := generateFixture()
	for name, tc := range map[string]struct {
		q    *ursus.LazyFrame
		want string
	}{
		"a step of zero":     {lf.Select(ursus.IntRange(0, 5, 0)), "step is zero"},
		"a negative repeat":  {lf.Select(ursus.Repeat(1, -1)), "-1 times"},
		"a negative count":   {lf.Select(c("a").RepeatBy(c("st"))), "-1 times in row 1"},
		"a float start":      {lf.Select(ursus.IntRange(0.5, 3, 1)), "takes integers"},
		"ends of two kinds":  {lf.Select(ursus.LinearSpace(ursus.Lit(time.Now()), 1, 3)), "two values of one temporal type"},
		"another length":     {lf.Select(c("a"), ursus.IntRange(0, 2, 1)), "columns side by side are of one length"},
		"over its partition": {lf.Select(ursus.IntRange(0, ursus.Len(), 1).Over(c("g"))), "CumCount(false)"},
	} {
		if _, err := tc.q.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}
