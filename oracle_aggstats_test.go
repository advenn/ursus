package ursus_test

// The aggstats group (step 500, from aggstats_test.go's tables): skew and kurtosis,
// the bitwise and small aggregates, entropy, mode and Spearman's correlation, each
// spelled as bench/scripts/oracle/aggstats.py asks Polars.

import (
	"math"

	"github.com/advenn/ursus"
)

func init() {
	c := ursus.Col
	x, i, y, g := c("x"), c("i"), c("y"), c("g")
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	byG := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().GroupBy(g).Agg(exprs...).Sort(ursus.Asc(g)) }
	}
	moments := map[string]ursus.Expr{
		"skew": x.Skew(true), "skew_unbiased": x.Skew(false), "kurtosis": x.Kurtosis(true, true),
		"kurtosis_pearson": x.Kurtosis(false, true), "kurtosis_unbiased": x.Kurtosis(true, false),
	}
	small := func() []ursus.Expr {
		return []ursus.Expr{i.BitwiseAnd().Alias("and"), i.BitwiseOr().Alias("or"), i.BitwiseXor().Alias("xor"), x.HasNulls().Alias("has_nulls"),
			x.IsEmpty().Alias("is_empty"), x.NanMin().Alias("nan_min"), i.ApproxNUnique().Cast(ursus.Int64).Alias("approx_n_unique"),
			x.Dot(y).Alias("dot")}
	}
	// skew_near_1e9 and moments_far_2000 are asked of Polars as the moments of x less its
	// first, and entropy_raw_by_g as -sum(x ln x) (step 507): ursus shifts the moments by
	// the first value, and its raw entropy of one value is -x ln x, where Polars 2.1
	// loses seven digits at 1e9 and answers 0.0 for one value.
	entropies := map[string]ursus.Expr{
		"entropy": x.Entropy(math.E, true), "entropy_bits": x.Entropy(2, true), "spearman": ursus.CorrSpearman(x, y),
	}
	spearman := sel(ursus.CorrSpearman(c("a"), c("b")))
	reg := map[string]oracleQuery{
		"skew_unbiased_nulls_by_g":     byG(x.Skew(false).IsNull()),
		"kurtosis_unbiased_nulls_by_g": byG(x.Kurtosis(true, false).IsNull()),
		"skew_one_value_not_null_by_g": byG(x.Skew(true).IsNull()),
		"skew_ints":                    sel(i.Skew(true)),
		"skew_near_1e9":                sel(x.Skew(true)),
		"moments_far_2000":             sel(x.Skew(true), x.Kurtosis(false, true).Alias("k")),
		"moments_all_null":             sel(x.Skew(true), x.Kurtosis(true, true).Alias("k")),
		"skew_nan":                     sel(x.Skew(true)),

		"small_all":      sel(small()...),
		"small_by_g":     byG(small()...),
		"bitwise_bools":  sel(c("b").BitwiseAnd().Alias("a"), c("b").BitwiseOr().Alias("o"), c("b").BitwiseXor().Alias("x")),
		"bitwise_narrow": sel(c("a").BitwiseOr(), c("u").BitwiseXor(), c("a").BitwiseAnd().Alias("and8")),
		"small_all_null": sel(i.BitwiseAnd().Alias("a"), i.HasNulls().Alias("h"), i.ApproxNUnique().Cast(ursus.Int64).Alias("n"), i.IsEmpty().Alias("e")),
		"small_no_rows":  sel(i.BitwiseOr().Alias("o"), i.IsEmpty().Alias("e"), i.ApproxNUnique().Cast(ursus.Int64).Alias("n")),
		"nan_min_max":    sel(x.NanMin().Alias("nmin"), x.Min().Alias("min"), x.NanMax().Alias("nmax")),
		"dot_ints":       sel(c("a").Dot(c("b")).Cast(ursus.Int64)),

		"entropy_raw_all":   sel(x.Entropy(math.E, false)),
		"entropy_raw_by_g":  byG(x.Entropy(math.E, false)),
		"entropy_ints":      sel(i.Entropy(math.E, true)),
		"entropy_zero":      sel(x.Entropy(math.E, true)),
		"entropy_negative":  sel(x.Entropy(math.E, true)),
		"entropy_base_1":    sel(x.Entropy(1, true)),
		"entropy_f32":       sel(x.Cast(ursus.Float32).Entropy(2, true)),
		"entropy_none":      sel(x.Entropy(math.E, true)),
		"spearman_ties":     spearman,
		"spearman_ints":     spearman,
		"spearman_constant": spearman,
		"spearman_nan":      spearman,
		"spearman_nulls":    spearman,

		"mode":                 sel(x.Mode(false)),
		"mode_ties_sorted":     sel(c("a").Mode(false)),
		"mode_ties_first_seen": sel(c("a").Mode(true)),
		"mode_null_among":      sel(c("a").Mode(false)),
		"mode_nan":             sel(c("a").Mode(false)),
		"mode_zeros":           sel(c("a").Mode(false)),
		"mode_bools":           sel(c("a").Mode(false)),
		"mode_no_rows":         sel(c("a").Mode(false)),
		"mode_by_g_first_seen": byG(i.Mode(true)),
		"mode_by_g_sorted":     byG(x.Mode(false)),

		"small_gen": byG(small()...),
		"mode_gen_by_g": byG(i.Mode(true).Alias("i_first"), i.Mode(false).Alias("i_sorted"), c("s").Mode(true).Alias("s_first"),
			c("s").Mode(false).Alias("s_sorted"), c("f").Mode(false).Alias("f_sorted")),
		"mode_gen_frame": sel(i.Mode(false)),
	}
	for name, e := range moments {
		reg[name+"_all"] = sel(e)
		reg[name+"_by_g"] = byG(e)
	}
	for name, e := range entropies {
		reg[name+"_all"] = sel(e.Alias("r"))
		reg[name+"_by_g"] = byG(e.Alias("r"))
	}
	var momentsGen, entropiesGen []ursus.Expr
	for _, name := range []string{"skew", "skew_unbiased", "kurtosis", "kurtosis_pearson", "kurtosis_unbiased"} {
		momentsGen = append(momentsGen, moments[name].Alias(name))
	}
	for _, name := range []string{"entropy", "entropy_bits", "spearman"} {
		entropiesGen = append(entropiesGen, entropies[name].Alias(name))
	}
	reg["moments_gen"] = byG(momentsGen...)
	reg["entropy_spearman_gen"] = byG(append(entropiesGen, i.Entropy(2, true).Alias("ient"))...)
	oracleGroups["aggstats"] = reg

	// A group of no value is null here, as Sum's is, where Polars' entropy is -0.0
	// (feature-gaps.md §9, step 277).
	oracleDifferences["aggstats/entropy_none"] = oracleDifference{
		why:   "the entropy of no value is null here, as Sum's is; Polars' is -0.0",
		cells: map[string]map[int]any{"x": {0: nil}},
	}
}
