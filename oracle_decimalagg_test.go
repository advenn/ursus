package ursus_test

// The decimalagg group (step 503, from internal/expr/decimalagg_test.go's table of
// each aggregate's type over a Decimal): every aggregate of a Decimal(10, 2) by
// group, its type and its values, each spelled as bench/scripts/oracle/decimalagg.py
// asks Polars.

import (
	"math"

	"github.com/advenn/ursus"
)

func init() {
	x := ursus.Col("x")
	agg := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().GroupBy(ursus.Col("g")).Agg(exprs...) }
	}
	oracleGroups["decimalagg"] = map[string]oracleQuery{
		"sum":        agg(x.Sum().Alias("sum")),
		"mean":       agg(x.Mean().Alias("mean")),
		"min_max":    agg(x.Min().Alias("min"), x.Max().Alias("max")),
		"first_last": agg(x.First().Alias("first"), x.Last().Alias("last")),
		// Asked of Polars without the nulls: NUnique does not count one (feature-gaps §9).
		"counts": agg(x.Count().Alias("count"), x.Len().Alias("len"), x.NullCount().Alias("null_count"),
			x.NUnique().Alias("n_unique")),
		"arg_min_max": agg(x.ArgMin().Cast(ursus.Int64).Alias("arg_min"), x.ArgMax().Cast(ursus.Int64).Alias("arg_max")),
		"var_std":     agg(x.Var(1).Alias("var"), x.Std(1).Alias("std"), x.Var(0).Alias("var0")),
		// Asked of Polars as the floats' product: a Product is a Float64 for every
		// number (feature-gaps §9), exact over these multiples of a quarter.
		"product": agg(x.Product().Alias("product")),
		// Polars 2.1 answers any and all of a Decimal, true where a value is not zero;
		// Any and AllTrue take a Boolean alone (internal/expr.ResolveAggBinding: "compare
		// first"), as a Decimal's cast to a Boolean is refused (castbool/decimal_to_bool).
		"any_all":     oracleFinding("Any and AllTrue refuse a Decimal; Polars 2.1 answers any and all of one, true where a value is not zero"),
		"bitwise_and": agg(x.BitwiseAnd().Alias("and")),
		"median_quantile": agg(x.Median().Alias("median"), x.Quantile(0.75, ursus.InterpNearest).Alias("q75_nearest"),
			x.Quantile(0.25, ursus.InterpLinear).Alias("q25_linear")),
		"implode":       agg(x.Implode().Alias("all")),
		"top_bottom_k":  agg(x.TopK(2).List().Sort().Alias("top"), x.BottomK(2).List().Sort().Alias("bottom")),
		"skew_kurtosis": agg(x.Skew(true).Alias("skew"), x.Kurtosis(true, true).Alias("kurtosis")),
		"nan_min":       agg(x.NanMin().Alias("nan_min")),
		"entropy":       agg(x.Entropy(math.E, true).Alias("entropy")),
		"item":          agg(x.Item(false).Alias("item")),
		"bounds":        agg(x.LowerBound().Alias("lower"), x.UpperBound().Alias("upper")),
		"is_sorted":     agg(x.IsSorted(false, false).Alias("asc"), x.IsSorted(true, false).Alias("desc")),
		"mode":          agg(x.Mode(true).Alias("mode")),
		"gen": agg(x.Sum().Alias("sum"), x.Min().Alias("min"), x.Max().Alias("max"), x.First().Alias("first"),
			x.Last().Alias("last"), x.Mean().Alias("mean"), x.Median().Alias("median"), x.Implode().Alias("all")),
	}
	// A count is a Uint64 here, Polars' a UInt32 (feature-gaps §9).
	oracleDifferences["decimalagg/counts"] = oracleDifference{
		why:    "a count is a Uint64 here, Polars' a UInt32",
		dtypes: map[string]string{"count": "Uint64", "len": "Uint64", "null_count": "Uint64", "n_unique": "Uint64"},
	}
}
