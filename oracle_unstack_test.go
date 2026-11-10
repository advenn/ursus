package ursus_test

// The unstack group (step 501, from unstack_test.go's hand answers): Polars' unstack,
// vertical and horizontal, a column, a fill and the padded suffixes, each spelled as
// bench/scripts/oracle/unstack.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	un := func(o ursus.UnstackOptions) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame {
			return in.eager(collect(in.t, in.df()).Unstack(in.t.Context(), o))
		}
	}
	h := ursus.UnstackHorizontal
	oracleGroups["unstack"] = map[string]oracleQuery{
		"vertical":                  un(ursus.UnstackOptions{Step: 3}),
		"horizontal":                un(ursus.UnstackOptions{Step: 3, How: h}),
		"a_column":                  un(ursus.UnstackOptions{Step: 3, Columns: []string{"y"}}),
		"horizontal_two_across":     un(ursus.UnstackOptions{Step: 2, How: h}),
		"vertical_step_past_height": un(ursus.UnstackOptions{Step: 5}),
		"a_fill_for_all":            un(ursus.UnstackOptions{Step: 2, Fill: []any{0}}),
		"text_for_all":              un(ursus.UnstackOptions{Step: 2, Fill: []any{"z"}}),
		"a_fill_each":               un(ursus.UnstackOptions{Step: 2, Fill: []any{2.5, "q"}}),
		"a_null_kept":               un(ursus.UnstackOptions{Step: 2, Fill: []any{0}}),
		"horizontal_filled":         un(ursus.UnstackOptions{Step: 2, How: h, Fill: []any{9}}),
		"padded_25_by_2":            un(ursus.UnstackOptions{Step: 2}),
		"padded_25_by_2_horizontal": un(ursus.UnstackOptions{Step: 2, How: h}),
		"padded_100_by_1":           un(ursus.UnstackOptions{Step: 1}),
		"padded_10_by_1":            un(ursus.UnstackOptions{Step: 1}),
		"padded_9_by_1":             un(ursus.UnstackOptions{Step: 1}),
		"unstack_gen":               un(ursus.UnstackOptions{Step: 7}),
		"unstack_gen_horizontal":    un(ursus.UnstackOptions{Step: 7, How: h, Fill: []any{0.5, "pad"}}),
	}
}
