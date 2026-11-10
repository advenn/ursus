package ursus_test

// The dummies group (step 501, from dummies_test.go's hand answers): to_dummies, its
// options, and each type's dummy names, each spelled as bench/scripts/oracle/dummies.py
// asks Polars.

import "github.com/advenn/ursus"

func init() {
	dum := func(o ursus.DummiesOptions) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame {
			return in.eager(collect(in.t, in.df()).ToDummies(in.t.Context(), o))
		}
	}
	all := dum(ursus.DummiesOptions{})
	reg := map[string]oracleQuery{
		"a_column_in_its_place":         dum(ursus.DummiesOptions{Columns: []string{"bar"}}),
		"a_separator_the_first_dropped": dum(ursus.DummiesOptions{Columns: []string{"bar"}, Separator: ":", DropFirst: true}),
		"the_null_dropped":              dum(ursus.DummiesOptions{Columns: []string{"bar"}, DropNulls: true}),
		"every_column":                  all,
		"ordered_by_name":               all,
		"ordered_by_name_first_dropped": dum(ursus.DummiesOptions{DropFirst: true}),
		"a_null_first":                  dum(ursus.DummiesOptions{DropFirst: true}),
		"only_nulls_none_dropped_first": dum(ursus.DummiesOptions{DropFirst: true}),
		"text_by_its_bytes":             dum(ursus.DummiesOptions{DropFirst: true}),
		"floats":                        all,
		"a_series": func(in oracleInputs) *ursus.LazyFrame {
			s, err := collect(in.t, in.df()).Column[string]("s")
			if err != nil {
				in.t.Fatal(err)
			}
			return in.eager(s.ToDummies(in.t.Context(), ursus.DummiesOptions{DropFirst: true}))
		},
		"dummies_gen":         all,
		"dummies_gen_dropped": dum(ursus.DummiesOptions{Columns: []string{"s", "i"}, DropFirst: true, DropNulls: true, Separator: ":"}),
	}
	for _, name := range []string{"float64", "float64_more", "float32", "duration_ns", "duration_us", "duration_ms", "date", "datetime_us",
		"datetime_ns", "datetime_ms", "time", "berlin", "kolkata", "tashkent", "utc", "new_york", "binary", "decimal", "decimal_no_scale",
		"uint64", "int8", "boolean", "enum"} {
		reg["names_"+name] = all
	}
	oracleGroups["dummies"] = reg
}
