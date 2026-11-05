package ursus_test

// Step 174: ReplaceTimeZone. Its answers are Polars 2.1.0's, recorded in the tzreplace
// group of testdata/oracle and spelled in oracle_tzreplace_test.go since step 498,
// which took this file's table. What stays asserts the refusals, and that a wall clock
// read twice or never refuses the query by default, as Polars' does.

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

func TestReplaceTimeZoneRefuses(t *testing.T) {
	one := []string{"2024-01-01T00:00:00"}
	runCalCases(t, []calCase{
		{name: "a Date", dt: dtype.Date, in: []string{"2024-01-01"},
			f:      func(d ursus.DtExpr) ursus.Expr { return d.ReplaceTimeZone("UTC") },
			refuse: "requires a Datetime"},
		{name: "an unknown zone", dt: naiveUs, in: one,
			f:      func(d ursus.DtExpr) ursus.Expr { return d.ReplaceTimeZone("Mars/Olympus_Mons") },
			refuse: "not known"},
		{name: "an ambiguous policy that is none", dt: naiveUs, in: one,
			f:      func(d ursus.DtExpr) ursus.Expr { return d.ReplaceTimeZone("UTC", ursus.Ambiguous("nearest")) },
			refuse: "ambiguous policy"},
		{name: "a non_existent policy Polars does not take", dt: naiveUs, in: one,
			f:      func(d ursus.DtExpr) ursus.Expr { return d.ReplaceTimeZone("UTC", ursus.NonExistent("earliest")) },
			refuse: "non_existent policy"},
	})
}

// TestReplaceTimeZoneRaisesByDefault: a wall clock read twice or never refuses the
// query, as Polars' default raises, naming the clock and how to answer it.
func TestReplaceTimeZoneRaisesByDefault(t *testing.T) {
	for _, c := range []struct {
		name, in string
		want     []string
	}{
		{"never", "2024-03-31T01:30:00", []string{"does not exist", "2024-03-31", "01:30", "NonExistentNull"}},
		{"twice", "2024-10-27T01:30:00", []string{"is ambiguous", "2024-10-27", "01:30", "AmbiguousEarliest"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			lf := calColumn(t, naiveUs, "2024-06-01T00:00:00", c.in).
				Select(ursus.Col("ts").Dt().ReplaceTimeZone("Europe/London"))
			_, err := lf.Collect(t.Context())
			if err == nil {
				t.Fatal("answered; Polars raises")
			}
			if errors.Is(err, uerr.ErrInternal) {
				t.Errorf("reported as an ursus bug: %v", err)
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("the error does not name %q: %v", w, err)
				}
			}
		})
	}
}
