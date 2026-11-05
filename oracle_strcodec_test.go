package ursus_test

// The strcodec group (step 499, from strcodec_test.go's hand tables): encode and
// decode, to_integer, replace's n, concat_str past nulls, json_path_match and
// json_decode, each spelled as bench/scripts/oracle/strcodec.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c, el := ursus.Col, ursus.Element
	s := c("s").Str()
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	ab := ursus.StructOf(ursus.Field{Name: "a", Type: ursus.Int64}, ursus.Field{Name: "b", Type: ursus.String})
	paths := []string{"$.d", "$.a.b[0]", "$.a.b[2].c", "$.a.b", "$.e", "$.f", "$.a", "$['d']", "$.zz", "$.a.b[-1]", "$..c"}
	matches := make([]ursus.Expr, len(paths))
	for i, p := range paths {
		matches[i] = s.JsonPathMatch(p).Alias(p)
	}
	sum := func(acc, x ursus.Expr) ursus.Expr { return acc.Add(x) }
	oracleGroups["strcodec"] = map[string]oracleQuery{
		"encode":                         sel(s.Encode(ursus.Hex).Alias("hex"), s.Encode(ursus.Base64).Alias("b64")),
		"decode_hex":                     sel(s.Decode(ursus.Hex, false)),
		"decode_base64":                  sel(s.Decode(ursus.Base64, false)),
		"to_integer_base_16":             sel(s.ToIntegerWith(16, ursus.Int64, false)),
		"to_integer_base_a_row":          sel(s.ToIntegerWith(c("b"), ursus.Int64, false)),
		"to_integer_int8":                sel(s.ToIntegerWith(10, ursus.Int8, false)),
		"to_integer_uint8":               sel(s.ToIntegerWith(10, ursus.Uint8, false)),
		"to_integer_sign_not_underscore": sel(s.ToIntegerWith(10, ursus.Int64, false)),
		"replace_n2":                     sel(s.ReplaceN("a", "X", true, 2)),
		"replace_n0":                     sel(s.ReplaceN("a", "X", true, 0)),
		"replace_n_minus_1":              sel(s.ReplaceN("a", "X", true, -1)),
		"replace_regex_n1":               sel(s.ReplaceN("a+", "X", false, 1)),
		"replace_regex_group_n1":         sel(s.ReplaceN("(a)", "<$1>", false, 1)),
		"concat_str_ignore_nulls":        sel(ursus.ConcatStrIgnoreNulls("-", c("a"), c("b"))),
		"concat_str_with_a_literal":      sel(ursus.ConcatStrIgnoreNulls("", c("b"), ursus.Lit("!"))),
		"json_path_match":                sel(matches...),
		"json_path_numbers":              sel(s.JsonPathMatch("$")),
		"json_path_repeated_key":         sel(s.JsonPathMatch("$.a").Alias("a"), s.JsonPathMatch("$").Alias("all")),
		"json_path_escapes":              sel(s.JsonPathMatch("$")),
		"json_path_recursive":            sel(s.JsonPathMatch("$..c")),
		"json_decode_struct":             sel(s.JsonDecode(ab).Alias("d")),
		"json_decode_list":               sel(s.JsonDecode(ursus.List(ursus.Int64))),
		"json_decode_int64":              sel(s.JsonDecode(ursus.List(ursus.Int64))),
		"json_decode_int8":               sel(s.JsonDecode(ursus.List(ursus.Int8))),
		"json_decode_uint8":              sel(s.JsonDecode(ursus.List(ursus.Uint8))),
		"json_decode_uint64":             sel(s.JsonDecode(ursus.List(ursus.Uint64))),
		"json_decode_float32":            sel(s.JsonDecode(ursus.List(ursus.Float32))),
		"json_decode_string":             sel(s.JsonDecode(ursus.List(ursus.String))),
		"json_decode_scalar_as_list":     sel(s.JsonDecode(ursus.List(ursus.Int64))),
		"json_decode_scalars_as_lists":   sel(s.JsonDecode(ursus.List(ursus.List(ursus.Int64)))),
		"json_decode_repeated_key":       sel(s.JsonDecode(ursus.StructOf(ursus.Field{Name: "a", Type: ursus.Int64}))),
		"implode_then_eval":              sel(c("a").Implode().List().Eval(el().Mul(2))),
		"fold_of_aggregates":             sel(ursus.Fold(0, sum, c("a").Sum(), c("b").Max()).Alias("f")),

		"codec_gen": sel(s.Encode(ursus.Hex).Alias("hex"), s.Encode(ursus.Base64).Alias("b64"),
			s.Encode(ursus.Hex).Str().Decode(ursus.Hex, true).Alias("hex_back"), s.Encode(ursus.Base64).Str().Decode(ursus.Base64, true).Alias("b64_back")),
		"to_integer_gen": sel(s.ToIntegerWith(c("b"), ursus.Int64, false).Alias("v"), s.ToIntegerWith(16, ursus.Int64, false).Alias("v16"),
			s.ToIntegerWith(c("b"), ursus.Int16, false).Alias("v16bit")),
		"replace_n_gen": sel(s.ReplaceN("a", "X", true, 1).Alias("n1"), s.ReplaceN("a", "X", true, 2).Alias("n2"),
			s.ReplaceN("a", "X", true, -1).Alias("all"), s.ReplaceN("b+", "_", false, 1).Alias("re1"),
			ursus.ConcatStrIgnoreNulls("/", c("s"), c("s").Str().LenChars()).Alias("cat")),
		"json_decode_gen":        sel(s.JsonDecode(ursus.List(ursus.Int64)).Alias("l"), s.JsonPathMatch("$[0]").Alias("first")),
		"json_decode_struct_gen": sel(s.JsonDecode(ab).Alias("d"), s.JsonPathMatch("$.b").Alias("b")),
	}
	// A literal 0 has its Go value's type here, Int64, where Polars' is an Int32, and
	// an integer Sum is an Int128 (feature-gaps.md §9): the fold's acc widens to it.
	oracleDifferences["strcodec/fold_of_aggregates"] = oracleDifference{
		why:    "a literal 0 is an Int64 here and a Sum an Int128, so the fold is an Int128; Polars' is an Int32 (feature-gaps.md §9)",
		dtypes: map[string]string{"f": "Int128"}}
}
