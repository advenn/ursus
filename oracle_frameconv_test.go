package ursus_test

// The frameconv group (step 501, from frameconv_test.go's hand answers): a frame's
// corr, gather, the horizontal reductions and fold, each spelled as
// bench/scripts/oracle/frameconv.py asks Polars.

import "github.com/advenn/ursus"

// oracleParamInts is the case's param name, a list of integers.
func oracleParamInts(in oracleInputs, name string) []int {
	list, ok := in.params[name].([]any)
	if !ok {
		in.t.Fatalf("the case has no list param %q", name)
	}
	out := make([]int, len(list))
	for i, v := range list {
		out[i] = int(oracleInt(in.t, v))
	}
	return out
}

func init() {
	eager := func(f func(in oracleInputs, df *ursus.DataFrame) (*ursus.DataFrame, error)) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.eager(f(in, collect(in.t, in.df()))) }
	}
	corr := eager(func(in oracleInputs, df *ursus.DataFrame) (*ursus.DataFrame, error) { return df.Corr(in.t.Context()) })
	// series is a reduction to one column, as a frame of it.
	series := func(f func(in oracleInputs, df *ursus.DataFrame) (*ursus.Column, error)) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame {
			col, err := f(in, collect(in.t, in.df()))
			if err != nil {
				in.t.Fatal(err)
			}
			return ursus.Frame(col)
		}
	}
	oracleGroups["frameconv"] = map[string]oracleQuery{
		"corr":                        corr,
		"corr_ten":                    corr,
		"corr_with_null_and_constant": corr,
		"corr_no_rows":                corr,
		"corr_one_row":                corr,
		"corr_clipped":                corr,
		"corr_decimal":                corr,
		"gather": eager(func(in oracleInputs, df *ursus.DataFrame) (*ursus.DataFrame, error) {
			return df.Gather(in.t.Context(), []int{2, 0, -1}, false)
		}),
		"gather_null_on_oob": eager(func(in oracleInputs, df *ursus.DataFrame) (*ursus.DataFrame, error) {
			return df.Gather(in.t.Context(), []int{9, 1}, true)
		}),
		"sum_horizontal": series(func(in oracleInputs, df *ursus.DataFrame) (*ursus.Column, error) {
			return df.SumHorizontal(in.t.Context(), true)
		}),
		"min_horizontal": series(func(in oracleInputs, df *ursus.DataFrame) (*ursus.Column, error) {
			return df.MinHorizontal(in.t.Context())
		}),
		"max_horizontal": series(func(in oracleInputs, df *ursus.DataFrame) (*ursus.Column, error) {
			return df.MaxHorizontal(in.t.Context())
		}),
		"mean_horizontal_null_skipped": series(func(in oracleInputs, df *ursus.DataFrame) (*ursus.Column, error) {
			return df.MeanHorizontal(in.t.Context(), true)
		}),
		"sum_horizontal_null_not_ignored": series(func(in oracleInputs, df *ursus.DataFrame) (*ursus.Column, error) {
			return df.SumHorizontal(in.t.Context(), false)
		}),
		"fold": series(func(in oracleInputs, df *ursus.DataFrame) (*ursus.Column, error) {
			return df.Fold(in.t.Context(), func(acc, x ursus.Expr) ursus.Expr { return acc.Add(x) })
		}),
		"corr_gen": corr,
		"gather_gen": eager(func(in oracleInputs, df *ursus.DataFrame) (*ursus.DataFrame, error) {
			return df.Gather(in.t.Context(), oracleParamInts(in, "idx"), false)
		}),
		"gather_gen_null_on_oob": eager(func(in oracleInputs, df *ursus.DataFrame) (*ursus.DataFrame, error) {
			return df.Gather(in.t.Context(), oracleParamInts(in, "idx"), true)
		}),
		"horizontal_gen": func(in oracleInputs) *ursus.LazyFrame {
			df, ctx := collect(in.t, in.df()), in.t.Context()
			var cols []*ursus.Column
			for _, f := range []func() (*ursus.Column, error){
				func() (*ursus.Column, error) { return df.SumHorizontal(ctx, true) },
				func() (*ursus.Column, error) { return df.MinHorizontal(ctx) },
				func() (*ursus.Column, error) { return df.MaxHorizontal(ctx) },
				func() (*ursus.Column, error) { return df.MeanHorizontal(ctx, false) },
			} {
				col, err := f()
				if err != nil {
					in.t.Fatal(err)
				}
				cols = append(cols, col)
			}
			return ursus.Frame(cols...)
		},
	}
}
