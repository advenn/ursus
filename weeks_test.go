package ursus_test

// Step 184: a grid of weeks starts on a Monday, as Polars' does; "7d" counts from the
// epoch, a Thursday. Found by the Polars oracle: ursus read "1w" as seven days and
// started its weeks on Thursdays. The answers are Polars 2.1.0's, recorded in the weeks
// group of testdata/oracle and spelled in oracle_weeks_test.go since step 498, which
// took this file's table. What stays is Every's rendering, which keeps weeks and days
// apart.

import (
	"testing"

	"github.com/advenn/ursus"
)

func TestWeeksAndDaysRenderApart(t *testing.T) {
	for in, want := range map[string]string{"1w": "1w", "2w": "2w", "7d": "7d", "14d": "14d", "1w1d": "8d", "-1w": "-1w"} {
		if got := ursus.Every(in).String(); got != want {
			t.Errorf("Every(%q) renders %q, want %q", in, got, want)
		}
	}
}
