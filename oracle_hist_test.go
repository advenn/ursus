package ursus_test

// The hist group (step 500, from hist_test.go's tables): Polars' hist, its counts
// alone and with the breakpoints and categories, which both sides unnest, each spelled
// as bench/scripts/oracle/hist.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	x, g := c("x"), c("g")
	bins, count, bp, cat := ursus.HistBins, ursus.HistBinCount, ursus.HistBreakpoint, ursus.HistCategory
	counts := func(e ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(e) }
	}
	// The hist's struct unnested in a Select of its own: a reshape and its unnest
	// together are a selection of several columns, which the zipper refuses.
	parts := func(e ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(e).Select(x.Struct().Field("*")) }
	}
	labelBins := []float64{-1234567.891, -2, 1e-7, 0.000000123456, 0.1, 12.0000000001, 1234567, 1234567.891, 1e7, 1.5e300}
	oracleGroups["hist"] = map[string]oracleQuery{
		"counts_10":          counts(x.Hist()),
		"counts_3":           counts(x.Hist(count(3))),
		"counts_0":           counts(x.Hist(count(0))),
		"counts_edges":       counts(x.Hist(bins(0, 2, 5))),
		"counts_one_edge":    counts(x.Hist(bins(3))),
		"both_3":             parts(x.Hist(count(3), bp(), cat())),
		"both_4":             parts(x.Hist(count(4), bp(), cat())),
		"both_edges":         parts(x.Hist(bins(0, 2, 5), bp(), cat())),
		"breakpoint_edges":   parts(x.Hist(bins(0, 2, 5), bp())),
		"on_the_edges":       parts(x.Hist(bins(0, 2, 5), bp())),
		"integers":           parts(x.Hist(count(4), bp())),
		"alike":              parts(x.Hist(count(2), bp())),
		"no_rows":            parts(x.Hist(count(2), bp())),
		"one_bin":            parts(x.Hist(count(1), bp())),
		"last_edge_greatest": parts(x.Hist(count(5), bp())),
		"f32":                parts(x.Cast(ursus.Float32).Hist(count(2), bp())),
		"nan_alone":          parts(x.Hist(count(2), bp())),
		"nan_once":           parts(x.Hist(count(2), bp())),
		"nan_beside":         parts(x.Hist(count(2), bp())),
		"labels":             parts(x.Hist(cat(), bins(labelBins...))),
		"twenty_in_10":       parts(x.Hist(bp())),
		"by_group": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().GroupBy(g).Agg(x.Hist(count(2))).Sort(ursus.Asc(g))
		},
		"both_gen":   parts(x.Hist(count(7), bp(), cat())),
		"edges_gen":  parts(x.Hist(bins(-500, -100, -0.5, 0, 0.5, 100, 500), bp(), cat())),
		"counts_gen": counts(x.Hist()),
		"ints_gen":   parts(x.Hist(count(5), bp())),
	}
}
