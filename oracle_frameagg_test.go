package ursus_test

// The frameagg group (step 501, from frameagg_test.go's hand answers): a frame's
// aggregates over every type, each spelled as bench/scripts/oracle/frameagg.py asks
// Polars. An integer or Boolean sum is an Int128 here, a product a Float64 and a count
// a Uint64, where Polars' are an Int64, a UInt32 and an Int64 (feature-gaps.md §9);
// an empty sum or product is null here, 0 or 1 in Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	q := func(f func(lf *ursus.LazyFrame) *ursus.LazyFrame) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return f(in.df()) }
	}
	oracleGroups["frameagg"] = map[string]oracleQuery{
		"sum":              q((*ursus.LazyFrame).Sum),
		"mean":             q((*ursus.LazyFrame).Mean),
		"median":           q((*ursus.LazyFrame).Median),
		"min":              q((*ursus.LazyFrame).Min),
		"max":              q((*ursus.LazyFrame).Max),
		"std":              q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Std(1) }),
		"var":              q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Var(1) }),
		"var_ddof0":        q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Var(0) }),
		"product":          q((*ursus.LazyFrame).Product),
		"quantile_nearest": q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Quantile(0.5, ursus.InterpNearest) }),
		"null_count":       q((*ursus.LazyFrame).NullCount),
		"empty_sum":        q((*ursus.LazyFrame).Sum),
		"empty_mean":       q((*ursus.LazyFrame).Mean),
		"empty_min":        q((*ursus.LazyFrame).Min),
		"empty_null_count": q((*ursus.LazyFrame).NullCount),
		"empty_product":    q((*ursus.LazyFrame).Product),
		"gen_sum": q(func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Sum().WithColumns(c("i").Cast(ursus.Int64), c("i32").Cast(ursus.Int64))
		}),
		"gen_min":        q((*ursus.LazyFrame).Min),
		"gen_max":        q((*ursus.LazyFrame).Max),
		"gen_mean":       q((*ursus.LazyFrame).Mean),
		"gen_median":     q((*ursus.LazyFrame).Median),
		"gen_null_count": q((*ursus.LazyFrame).NullCount),
		"gen_var":        q(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Var(1) }),
	}
	// feature-gaps.md §9 (step 300): a Sum widened to Int128 not to wrap, a Product a
	// Float64 for every number, a count a Uint64 not to wrap at 2^32 rows; a sum or
	// product of no value null, not 0 or 1.
	int128 := "an integer or Boolean sum is an Int128 here, Polars' an Int64 or a UInt32"
	oracleDifferences["frameagg/sum"] = oracleDifference{why: int128, dtypes: map[string]string{"i": "Int128", "i8": "Int128", "b": "Int128"}}
	oracleDifferences["frameagg/product"] = oracleDifference{
		why:    "a product is a Float64 for every number here, Polars' keeps an integer's type",
		dtypes: map[string]string{"i": "Float64", "i8": "Float64", "b": "Float64"},
	}
	counts := func(cols ...string) oracleDifference {
		d := oracleDifference{why: "a count is a Uint64 here, Polars' a UInt32", dtypes: map[string]string{}}
		for _, col := range cols {
			d.dtypes[col] = "Uint64"
		}
		return d
	}
	oracleDifferences["frameagg/null_count"] = counts("i", "i8", "f", "b", "s", "d", "t", "u", "l")
	oracleDifferences["frameagg/empty_null_count"] = counts("i", "s")
	oracleDifferences["frameagg/gen_null_count"] = counts("i", "f", "i32")
	oracleDifferences["frameagg/empty_sum"] = oracleDifference{
		why:    "a sum of no value is null here, Polars' 0; and an Int128",
		dtypes: map[string]string{"i": "Int128"}, cells: map[string]map[int]any{"i": {0: nil}},
	}
	oracleDifferences["frameagg/empty_product"] = oracleDifference{
		why:    "a product of no value is null here, Polars' 1; and a Float64",
		dtypes: map[string]string{"i": "Float64"}, cells: map[string]map[int]any{"i": {0: nil}},
	}
}
