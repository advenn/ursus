package ursus_test

// The update group (step 501, from update_test.go's hand answers): Polars'
// DataFrame.update, each spelled as bench/scripts/oracle/update.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	upd := func(o ursus.UpdateOptions) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Update(in.frame("other"), o) }
	}
	on := []string{"A"}
	oracleGroups["update"] = map[string]oracleQuery{
		"by_place":                 upd(ursus.UpdateOptions{}),
		"by_place_nulls":           upd(ursus.UpdateOptions{IncludeNulls: true}),
		"on_a":                     upd(ursus.UpdateOptions{On: on}),
		"on_a_inner":               upd(ursus.UpdateOptions{On: on, How: ursus.UpdateInner}),
		"on_a_full":                upd(ursus.UpdateOptions{On: on, How: ursus.UpdateFull}),
		"on_a_full_nulls":          upd(ursus.UpdateOptions{On: on, How: ursus.UpdateFull, IncludeNulls: true}),
		"on_a_full_right_order":    upd(ursus.UpdateOptions{On: on, How: ursus.UpdateFull, MaintainOrder: ursus.JoinOrderRight}),
		"a_key_twice_in_other":     upd(ursus.UpdateOptions{On: on}),
		"a_key_twice_in_the_frame": upd(ursus.UpdateOptions{On: on}),
		"a_float_over_an_integer":  upd(ursus.UpdateOptions{On: on}),
		"text_over_an_integer":     upd(ursus.UpdateOptions{On: on}),
		"keys_named_apart":         upd(ursus.UpdateOptions{LeftOn: on, RightOn: []string{"AA"}}),
		"no_column_in_common":      upd(ursus.UpdateOptions{On: on}),
		"int32_key_left":           upd(ursus.UpdateOptions{On: on}),
		"int32_key_full":           upd(ursus.UpdateOptions{On: on, How: ursus.UpdateFull}),
		"by_place_other_shorter":   upd(ursus.UpdateOptions{}),
		"update_gen_left":          upd(ursus.UpdateOptions{On: []string{"k"}}),
		"update_gen_full":          upd(ursus.UpdateOptions{On: []string{"k"}, How: ursus.UpdateFull}),
		"update_gen_inner_nulls":   upd(ursus.UpdateOptions{On: []string{"k"}, How: ursus.UpdateInner, IncludeNulls: true}),
	}
}
