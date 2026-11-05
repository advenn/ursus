package ursus_test

// The format group (step 496): every type the format spells, generated, through the
// identity, so the Python encoder and the Go loader are proved to agree on each; a
// frame of two inputs; a set-ordered answer; a refusal. bench/scripts/oracle/format.py
// asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	identity := func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(ursus.All()) }
	oracleGroups["format"] = map[string]oracleQuery{
		"identity_ints":               identity,
		"identity_floats":             identity,
		"identity_bool_string_binary": identity,
		"identity_temporal":           identity,
		"identity_decimals":           identity,
		"identity_categorical_enum":   identity,
		"identity_nested":             identity,
		"identity_empty":              identity,
		"two_inputs_concat": func(in oracleInputs) *ursus.LazyFrame {
			return ursus.Concat([]*ursus.LazyFrame{in.frame("top"), in.frame("bottom")})
		},
		"set_group_sum":       func(in oracleInputs) *ursus.LazyFrame { return in.df().GroupBy(c("k")).Agg(c("v").Sum()) },
		"refusal_abs_of_text": func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(c("s").Abs()) },
	}
}
