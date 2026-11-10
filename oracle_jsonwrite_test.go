package ursus_test

// The jsonwrite group (step 502, from jsonwrite_test.go's texts): write_ndjson and
// write_json of one frame holding every kind of value, far years, durations and no
// rows, each WriteNDJSON or WriteJSON of the input, the text compared byte for byte
// with what bench/scripts/oracle/jsonwrite.py recorded.

import (
	"bytes"

	"github.com/advenn/ursus"
)

func init() {
	ndjson := func(head bool) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame {
			lf := in.df()
			if head {
				lf = lf.Head(0)
			}
			var b bytes.Buffer
			err := lf.WriteNDJSON(in.t.Context(), &b)
			return in.written(b.String(), err)
		}
	}
	json := func(head bool) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame {
			lf := in.df()
			if head {
				lf = lf.Head(0)
			}
			var b bytes.Buffer
			err := lf.WriteJSON(in.t.Context(), &b)
			return in.written(b.String(), err)
		}
	}
	oracleGroups["jsonwrite"] = map[string]oracleQuery{
		"ndjson":         ndjson(false),
		"json":           json(false),
		"far_years":      ndjson(false),
		"durations":      ndjson(false),
		"no_rows_ndjson": ndjson(true),
		"no_rows_json":   json(true),
		"ndjson_gen":     ndjson(false),
		"json_gen":       json(false),
	}
}
