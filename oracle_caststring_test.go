package ursus_test

// The caststring group (step 503, from internal/kernel/caststring_test.go's values):
// a number, an Int128, a Decimal and a Boolean cast to text, each spelled as
// bench/scripts/oracle/caststring.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	text := func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(ursus.All().Cast(ursus.String)) }
	oracleGroups["caststring"] = map[string]oracleQuery{
		"int64_edges":  text,
		"uint64_edges": text,
		"int128_edges": text,
		"decimal_10_2": text,
		"decimal_38_0": text,
		"bool":         text,
		"gen_ints":     text,
		"gen_uints":    text,
		"gen_int128":   text,
		"gen_decimals": text,
		"gen_floats":   text,
		"gen_bools":    text,
	}
}
