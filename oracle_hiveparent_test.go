package ursus_test

// The hiveparent group (step 502, from hiveparent_test.go): a partition path with a
// parent directory component is refused, a backslash or `..` alone written; each
// SinkParquet of the input by the param key, the dataset's file names the written
// text, as bench/scripts/oracle/hiveparent.py asks Polars; a refusal is a value error.

import (
	"path/filepath"

	"github.com/advenn/ursus"
)

func init() {
	part := func(in oracleInputs) *ursus.LazyFrame {
		dir := filepath.Join(in.t.TempDir(), "out")
		if err := in.df().SinkParquet(in.t.Context(), dir, ursus.WithPartitionBy(in.text("key"))); err != nil {
			return in.written("", err)
		}
		return in.written(oracleListing(in, dir), nil)
	}
	oracleGroups["hiveparent"] = map[string]oracleQuery{
		"a_value":                 oracleRefusedAs(ursus.ErrValue, part),
		"a_keys_name":             oracleRefusedAs(ursus.ErrValue, part),
		"a_value_of_a_second_key": oracleRefusedAs(ursus.ErrValue, part),
		"a_backslash_written":     part,
		"dotdot_alone_written":    part,
		"segments_gen":            part,
	}
}
