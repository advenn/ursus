package ursus_test

import (
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
)

// G3 of v0.6.6-scope.md (step 265): Polars' pl.date, pl.datetime, pl.time,
// pl.duration, pl.from_epoch and business_day_count. Their answers are Polars 2.1.0's,
// recorded in the components group of testdata/oracle and spelled in
// oracle_components_test.go since step 498, which took this file's tables. What stays
// asserts the refusals.

// TestValuesFromComponentsRefuse: components that name no date or time, as Polars
// refuses them, a float component, and instants where business days take dates.
func TestValuesFromComponentsRefuse(t *testing.T) {
	c := ursus.Col
	lf := ursus.Frame(ursus.Values("y", []int64{2023}), ursus.Values("t", []time.Time{time.Now()}))
	for name, tc := range map[string]struct {
		e    ursus.Expr
		want string
	}{
		"February 29th, 2023": {ursus.MakeDate(c("y"), 2, 29), "date's components (2023, 2, 29) in row 0 name no date"},
		"a month 13":          {ursus.MakeDate(2024, 13, 1), "name no date"},
		"an hour 25":          {ursus.MakeDatetime(2024, 1, 1, 25, 0, 0, 0), "name no time"},
		"a time of 25 hours":  {ursus.MakeTime(25, 0, 0, 0), "name no time"},
		"a float component":   {ursus.MakeDate(2024.5, 1, 1), "integer components"},
		"a string part":       {ursus.MakeDuration(ursus.DurationParts{Days: "x"}), "takes numbers"},
		"instants":            {ursus.BusinessDayCount(c("t"), c("t")), "between Dates"},
		"a day unit":          {ursus.FromEpoch(c("y"), ursus.TimeUnit(99)), "from_epoch counts in"},
	} {
		if _, err := lf.Select(tc.e).Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}
