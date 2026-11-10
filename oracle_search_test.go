package ursus_test

// The search group (step 500, from search_test.go's tables): search_sorted and
// index_of, the peaks, unique_counts and value_counts, each spelled as
// bench/scripts/oracle/search.py asks Polars.

import (
	"fmt"
	"math"
	"strings"

	"github.com/advenn/ursus"
)

func init() {
	c := ursus.Col
	a, s, x, f, g := c("a"), c("s"), c("x"), c("f"), c("g")
	nan := math.NaN()
	anySide, left, right := ursus.SideAny, ursus.SideLeft, ursus.SideRight
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	byG := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().GroupBy(g).Agg(exprs...).Sort(ursus.Asc(g)) }
	}
	peaks := sel(x.PeakMax().Alias("max"), x.PeakMin().Alias("min"))
	nulls := sel(a.SearchSorted(nil, anySide, false).Alias("na"), a.SearchSorted(nil, left, false).Alias("nl"),
		a.SearchSorted(nil, right, false).Alias("nr"), a.SearchSorted(2, left, false).Alias("l"), a.SearchSorted(2, right, false).Alias("r"))
	// A value_counts struct unnested in a Select of its own (the zipper refuses a
	// reshape and its unnest together); a group's, exploded first.
	counts := func(e ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame {
			return in.df().Select(e.Alias("v")).Select(c("v").Struct().Field("*"))
		}
	}
	groupCounts := func(e ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame {
			return in.df().GroupBy(g).Agg(e).Explode("s").Select(g, s.Struct().Field("*"))
		}
	}
	var searchGen []ursus.Expr
	for _, v := range []int64{-50, -7, 0, 13, 50, 99} {
		for _, side := range []ursus.SearchSide{left, right} {
			name := strings.ReplaceAll(fmt.Sprintf("%s_%d", side, v), "-", "m")
			searchGen = append(searchGen, a.SearchSorted(v, side, false).Alias(name))
		}
	}
	searchGen = append(searchGen, a.SearchSorted(nil, left, false).Alias("null_left"), a.SearchSorted(nil, right, false).Alias("null_right"),
		a.IndexOf(int64(13)).Alias("idx13"), a.IndexOf(nil).Alias("idx_null"), a.IndexOf(int64(99)).Alias("idx99"))
	oracleGroups["search"] = map[string]oracleQuery{
		"search_sorted_any":      sel(a.SearchSorted(3, anySide, false)),
		"search_sorted_right":    sel(a.SearchSorted(3, right, false)),
		"search_sorted_by_group": byG(a.SearchSorted(3, left, false)),
		"search_sorted_descending": sel(a.SearchSorted(3, left, true).Alias("l"),
			a.SearchSorted(3, right, true).Alias("r")),
		"search_sorted_descending_more_above": sel(a.SearchSorted(4, left, true).Alias("l"),
			a.SearchSorted(7, right, true).Alias("r")),
		"search_sorted_descending_nulls_first": sel(a.SearchSorted(2, left, true).Alias("l"),
			a.SearchSorted(2, right, true).Alias("r")),
		"search_sorted_nan_last":      sel(a.SearchSorted(nan, left, false).Alias("nan"), a.SearchSorted(3.0, left, false).Alias("three")),
		"search_sorted_text":          sel(a.SearchSorted("d", left, false)),
		"search_sorted_no_rows":       sel(a.SearchSorted(3, left, false)),
		"search_sorted_all_null":      sel(a.SearchSorted(3, left, false)),
		"search_sorted_nulls_first_1": nulls,
		"search_sorted_nulls_first_2": nulls,
		"search_sorted_nulls_last_1":  nulls,
		"search_sorted_nulls_last_2":  nulls,
		"search_sorted_no_nulls":      nulls,
		"index_of":                    sel(a.IndexOf(3), s.IndexOf("x").Alias("s"), a.IndexOf(nil).Alias("n"), a.IndexOf(100).Alias("m")),
		"index_of_by_group":           byG(a.IndexOf(3)),
		"index_of_nan_and_zero":       sel(a.IndexOf(nan).Alias("nan"), a.IndexOf(0.0).Alias("zero")),
		"index_of_null_where_none":    sel(a.IndexOf(nil)),

		"peaks_polars":     peaks,
		"peaks_nan":        peaks,
		"peaks_negatives":  peaks,
		"peaks_falling":    peaks,
		"peaks_one":        peaks,
		"peaks_below_zero": peaks,
		"peaks_bools":      peaks,
		"peaks_dates":      peaks,
		"peak_max_over_g":  sel(x.PeakMax().Over(g)),

		"unique_counts":                sel(a.UniqueCounts()),
		"unique_counts_text":           sel(s.UniqueCounts()),
		"unique_counts_nan_zeros":      sel(f.UniqueCounts()),
		"unique_counts_by_group":       byG(s.UniqueCounts()),
		"value_counts":                 counts(s.ValueCounts(false, false)),
		"value_counts_sorted":          counts(s.ValueCounts(true, false)),
		"value_counts_ties":            counts(s.ValueCounts(true, false)),
		"value_counts_shares_named":    counts(s.ValueCounts(true, true, "n")),
		"value_counts_by_group_sorted": groupCounts(s.ValueCounts(true, false)),
		"value_counts_group_sorted":    groupCounts(s.ValueCounts(true, false)),
		"value_counts_group_shares":    groupCounts(s.ValueCounts(true, true)),

		"search_sorted_gen":         sel(searchGen...),
		"peaks_gen":                 peaks,
		"unique_counts_gen":         sel(s.UniqueCounts()),
		"value_counts_gen":          counts(a.ValueCounts(false, false)),
		"value_counts_by_group_gen": groupCounts(s.ValueCounts(true, false)),
	}
}
