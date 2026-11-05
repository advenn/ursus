package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestAggregationLists is step 331: a non-aggregate in an aggregation is each group's
// values as a list, a window in it computed a group at a time. The lists are Polars
// 2.1.0's since step 500, recorded in the agglists group of testdata/oracle and
// spelled in oracle_agglists_test.go, which took this file's table. What stays asserts
// the types beside each other: an aggregate stays one, an Int128 (which the recording
// casts to Int64, Polars' sum being one), and a literal a scalar, beside a list.
func TestAggregationLists(t *testing.T) {
	c := ursus.Col
	lf := ursus.Frame(ursus.Values("g", []int64{1, 1, 2, 2, 2}), i64s("x", 1, 2, 3, nil, 5),
		ursus.Values("y", []int64{5, 4, 3, 2, 1}))
	got, err := lf.GroupBy(c("g")).Agg(c("x").Sum(), c("y").Alias("ys"), ursus.Lit(int64(1)).Alias("one")).
		Sort(ursus.Asc(c("g"))).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if s := got.Schema().String(); !strings.Contains(s, "x: Int128") || !strings.Contains(s, "ys: List(Int64)") ||
		!strings.Contains(s, "one: Int64") {
		t.Errorf("an aggregate, a list and a literal: %s", s)
	}
}
