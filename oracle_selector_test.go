package ursus_test

// The selector group (step 503, from selector/selector_test.go's expansions): each
// selector and their algebra over one frame of eight types, the frame they select,
// and Expand's names, each spelled as bench/scripts/oracle/selector.py asks Polars.

import (
	"github.com/advenn/ursus"
	cs "github.com/advenn/ursus/selector"
)

func init() {
	sel := func(s cs.Selector) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(s.Expr()) }
	}
	// expand is the names a selector expands to over the input's schema, as a frame
	// of one String column, as Polars' expand_selector was recorded.
	expand := func(s cs.Selector) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame {
			schema, err := in.df().CollectSchema(in.t.Context())
			if err != nil {
				in.t.Fatal(err)
			}
			names, err := s.Expand(schema)
			if err != nil {
				in.t.Fatal(err)
			}
			if names == nil {
				names = []string{}
			}
			return ursus.Frame(ursus.Values("name", names))
		}
	}
	oracleGroups["selector"] = map[string]oracleQuery{
		"numeric":                   sel(cs.Numeric()),
		"integer":                   sel(cs.Integer()),
		"float":                     sel(cs.Float()),
		"temporal":                  sel(cs.Temporal()),
		"string":                    sel(cs.String()),
		"boolean":                   sel(cs.Boolean()),
		"starts_with_p":             sel(cs.StartsWith("p")),
		"ends_with_e":               sel(cs.EndsWith("e")),
		"contains_a":                sel(cs.Contains("a")),
		"matches":                   sel(cs.Matches("^[a-z]{2}$")),
		"first":                     sel(cs.First()),
		"last":                      sel(cs.Last()),
		"by_index_0_last":           sel(cs.ByIndex(0, -1)),
		"by_name_as_written":        sel(cs.ByName("qty", "id")),
		"numeric_minus_id":          sel(cs.Numeric().Difference(cs.ByName("id"))),
		"not_numeric":               sel(cs.Numeric().Complement()),
		"numeric_and_starts_with_q": sel(cs.Numeric().Intersect(cs.StartsWith("q"))),
		"numeric_or_string":         sel(cs.Numeric().Union(cs.String())),
		"numeric_xor_integer":       sel(cs.Numeric().SymmetricDifference(cs.Integer())),
		"numeric_exclude_id_nope":   sel(cs.Numeric().Exclude("id", "nope")),
		"alpha":                     sel(cs.Alpha()),
		"datetime":                  sel(cs.Datetime()),
		"date":                      sel(cs.Date()),
		"binary":                    sel(cs.Binary()),
		"all_exclude_id_qty":        sel(cs.All().Exclude("id", "qty")),
		"all":                       sel(cs.All()),
		"by_dtype_int64_bool":       sel(cs.ByDType(ursus.Int64, ursus.Bool)),
		"expand_digit":              expand(cs.Digit()),
		"expand_names_or_float":     expand(cs.ByName("qty", "id").Union(cs.Float())),
		"expand_names":              expand(cs.ByName("qty", "id")),
		"numeric_doubled": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().Select(cs.Numeric().Expr().Mul(2))
		},
		"gen_numeric":                  sel(cs.Numeric()),
		"gen_temporal_or_boolean":      sel(cs.Temporal().Union(cs.Boolean())),
		"gen_matches_letter_digit":     sel(cs.Matches(`^[a-z]_\d$`)),
		"gen_by_index":                 sel(cs.ByIndex(1, 3, -1)),
		"gen_not_text_nor_binary":      sel(cs.String().Union(cs.Binary()).Complement()),
		"gen_contains_t_minus_integer": sel(cs.Contains("t").Difference(cs.Integer())),
	}
}
