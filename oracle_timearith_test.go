package ursus_test

// The timearith group (step 498, from timearith_test.go): a Time minus a Time, a
// signed Duration, spelled as bench/scripts/oracle/timearith.py asks Polars. A Time
// plus a Duration Polars 2.1 refuses; the Go test holds it to Go's time package.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	oracleGroups["timearith"] = map[string]oracleQuery{
		"time_minus_time":     sel(c("a").Sub(c("b")).Alias("d")),
		"time_minus_time_gen": sel(c("a").Sub(c("b")).Alias("d"), c("b").Sub(c("a")).Alias("back")),
	}
}
