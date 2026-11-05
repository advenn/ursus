package ursus_test

// The unpivot group (step 501, from unpivot_test.go's hand answers): Unpivot's values
// and types in ursus's row-major order, which bench/scripts/oracle/unpivot.py asks of
// Polars through a stable sort on a row index (feature-gaps.md §9).

import "github.com/advenn/ursus"

func init() {
	un := func(o ursus.UnpivotOptions) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Unpivot(o) }
	}
	oracleGroups["unpivot"] = map[string]oracleQuery{
		"row_major":            un(ursus.UnpivotOptions{On: []string{"jan", "feb"}}),
		"promotes_int32_int64": un(ursus.UnpivotOptions{On: []string{"small", "big"}}),
		"index_only":           un(ursus.UnpivotOptions{Index: []string{"id"}}),
		"every_column":         un(ursus.UnpivotOptions{}),
		"named":                un(ursus.UnpivotOptions{Index: []string{"id"}, VariableName: "col", ValueName: "val"}),
		"unpivot_gen":          un(ursus.UnpivotOptions{On: []string{"a", "b", "c"}}),
	}
}
