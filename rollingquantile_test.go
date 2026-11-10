package ursus_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/uerr"
)

// TestRollingQuantileRefusesOutsideUnit: rolling median and quantile by rows and by
// time (step 227) answer as Polars 2.1.0 does since step 500, recorded in the
// rollingquantile group of testdata/oracle and spelled in
// oracle_rollingquantile_test.go, which took this file's tables. What stays: a
// quantile outside [0, 1] is refused, naming the function.
func TestRollingQuantileRefusesOutsideUnit(t *testing.T) {
	frame := ursus.Frame(ursus.Values("v", []float64{3, 1, 4}))
	_, err := frame.Select(ursus.Col("v").RollingQuantile(1.5, ursus.InterpLinear, 3)).Collect(t.Context())
	if !errors.Is(err, uerr.ErrValue) || !strings.Contains(err.Error(), "rolling_quantile") {
		t.Errorf("a quantile of 1.5 should be refused, naming rolling_quantile: %v", err)
	}
}
