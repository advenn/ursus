package ursus_test

// The asofoptions group (step 501, from asofoptions_test.go's hand answers): an as-of
// join's tolerance, its keys kept apart, and join_where with a suffix, each spelled as
// bench/scripts/oracle/asofoptions.py asks Polars.

import (
	"time"

	"github.com/advenn/ursus"
)

func init() {
	c := ursus.Col
	asOf := func(key string, opts ...ursus.AsOfOption) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame {
			return in.frame("left").JoinAsOf(in.frame("right"), append([]ursus.AsOfOption{ursus.AsOfOn(c(key))}, opts...)...).Select(c("w"))
		}
	}
	tol := ursus.AsOfTolerance[float64]
	oracleGroups["asofoptions"] = map[string]oracleQuery{
		"tolerance_1":          asOf("k", ursus.AsOfTolerance(1)),
		"tolerance_0_exact":    asOf("k", ursus.AsOfTolerance(0)),
		"forward_1":            asOf("k", ursus.AsOfTolerance(1), ursus.AsOfStrategyOpt(ursus.AsOfForward)),
		"nearest_2":            asOf("k", ursus.AsOfTolerance(2), ursus.AsOfStrategyOpt(ursus.AsOfNearest)),
		"tolerance_1_5":        asOf("k", tol(1.5)),
		"tolerance_int64_1":    asOf("k", ursus.AsOfTolerance(int64(1))),
		"float_0_1":            asOf("k", tol(0.1)),
		"float_1":              asOf("k", ursus.AsOfTolerance(1)),
		"zero_interval_met":    asOf("t", ursus.AsOfTolerance(time.Duration(0))),
		"zero_interval_missed": asOf("t", ursus.AsOfTolerance(time.Duration(0))),
		"zero_duration_missed": asOf("t", ursus.AsOfTolerance(ursus.FromDuration(0))),
		"no_tolerance_met":     asOf("t"),
		"coalesce_false": func(in oracleInputs) *ursus.LazyFrame {
			return in.frame("left").JoinAsOf(in.frame("right"), ursus.AsOfOn(c("k")), ursus.AsOfCoalesce(false))
		},
		"join_where_suffix": func(in oracleInputs) *ursus.LazyFrame {
			return in.frame("a").JoinWhereSuffix(in.frame("b"), "_r", c("x").Lt(c("x_r")))
		},
		"tolerance_gen": func(in oracleInputs) *ursus.LazyFrame {
			l, r, on := in.frame("left"), in.frame("right"), ursus.AsOfOn(c("k"))
			return l.JoinAsOf(r, on, ursus.AsOfTolerance(5)).Rename(map[string]string{"w": "back"}).
				JoinAsOf(r, on, ursus.AsOfTolerance(5), ursus.AsOfStrategyOpt(ursus.AsOfForward)).Rename(map[string]string{"w": "fwd"}).
				Select(c("back"), c("fwd"))
		},
		// Asked of Polars as the nearer of its backward and forward rows, the backward on
		// a tie (step 507): Polars 2.1's own nearest takes the forward.
		"nearest_tie_gen": asOf("k", ursus.AsOfTolerance(5), ursus.AsOfStrategyOpt(ursus.AsOfNearest)),
		"join_where_gen": func(in oracleInputs) *ursus.LazyFrame {
			return in.frame("a").JoinWhereSuffix(in.frame("b"), "_r", c("x").Lt(c("x_r")))
		},
	}
}
