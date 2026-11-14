package ursus_test

// The emptystruct group (step 518): a Struct of no fields, which a JSON or NDJSON
// object field holding {} reads as. The reads are ScanNDJSONBytes of the param ndjson
// and ScanJSONBytes of the param json, under the param options; the questions after
// are asked of the input frame df, other the right side of a join, each spelled as
// bench/scripts/oracle/emptystruct.py asks Polars; a writer's text or refusal is
// in.written's, IPC read back from its own bytes.

import (
	"bytes"

	"github.com/advenn/ursus"
)

func init() {
	c := ursus.Col
	nd := func(in oracleInputs) *ursus.LazyFrame {
		return ursus.ScanNDJSONBytes([]byte(in.text("ndjson")), "t.ndjson", in.ndjsonOptions()...)
	}
	js := func(in oracleInputs) *ursus.LazyFrame {
		return ursus.ScanJSONBytes([]byte(in.text("json")), "t.json", in.ndjsonOptions()...)
	}
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	frame := func(f func(*ursus.LazyFrame) *ursus.LazyFrame) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return f(in.df()) }
	}
	// written is a writer's text of the frame f makes of df, or its refusal.
	written := func(f func(*ursus.LazyFrame) *ursus.LazyFrame, write func(*ursus.LazyFrame, *bytes.Buffer, oracleInputs) error) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame {
			var b bytes.Buffer
			err := write(f(in.df()), &b, in)
			return in.written(b.String(), err)
		}
	}
	// back is df written by write and read back from its bytes by read, or the
	// writer's refusal.
	back := func(write func(*ursus.LazyFrame, *bytes.Buffer, oracleInputs) error, read func([]byte) *ursus.LazyFrame) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame {
			var b bytes.Buffer
			if err := write(in.df(), &b, in); err != nil {
				*in.failed = err
				return nil
			}
			return read(b.Bytes())
		}
	}
	same := func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf }
	ndjson := func(lf *ursus.LazyFrame, b *bytes.Buffer, in oracleInputs) error {
		return lf.WriteNDJSON(in.t.Context(), b)
	}
	json := func(lf *ursus.LazyFrame, b *bytes.Buffer, in oracleInputs) error {
		return lf.WriteJSON(in.t.Context(), b)
	}
	csv := func(lf *ursus.LazyFrame, b *bytes.Buffer, in oracleInputs) error {
		return lf.WriteCSV(in.t.Context(), b)
	}
	ipc := func(lf *ursus.LazyFrame, b *bytes.Buffer, in oracleInputs) error {
		return lf.WriteIPC(in.t.Context(), b)
	}
	ipcStream := func(lf *ursus.LazyFrame, b *bytes.Buffer, in oracleInputs) error {
		return lf.WriteIPCStream(in.t.Context(), b)
	}
	parquet := func(lf *ursus.LazyFrame, b *bytes.Buffer, in oracleInputs) error {
		return lf.WriteParquet(in.t.Context(), b)
	}
	readIPC := func(b []byte) *ursus.LazyFrame { return ursus.ScanIPCBytes(b, "t.arrow") }
	readIPCStream := func(b []byte) *ursus.LazyFrame { return ursus.ScanIPCStreamBytes(b, "t.arrows") }
	readParquet := func(b []byte) *ursus.LazyFrame { return ursus.ScanParquetBytes(b, "t.parquet") }
	head6 := func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Head(6) }
	a := c("a")
	empty := ursus.StructOf()

	reg := map[string]oracleQuery{
		"filter":       frame(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Filter(c("b").Gt(0)) }),
		"filter_on_it": frame(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Filter(a.IsNotNull()) }),
		"head":         frame(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Head(5) }),
		"tail":         frame(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Tail(4) }),
		"slice":        frame(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Slice(3, 7) }),
		"select_alone": sel(a),
		"sort": frame(func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Sort(ursus.Asc(c("b")).NullsLast(), ursus.Asc(c("i")).NullsLast())
		}),
		"sort_descending": frame(func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Sort(ursus.Desc(c("b")).NullsLast(), ursus.Desc(c("i")).NullsLast())
		}),
		"reverse": frame((*ursus.LazyFrame).Reverse),
		"concat": frame(func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return ursus.Concat([]*ursus.LazyFrame{lf, lf.Head(3)})
		}),
		"concat_alone": frame(func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return ursus.Concat([]*ursus.LazyFrame{lf.Select(a), lf.Select(a).Tail(2)})
		}),
		"join_inner": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().Join(in.frame("other"), ursus.JoinOn(c("k"))).Sort(ursus.Asc(c("i")))
		},
		"join_left": func(in oracleInputs) *ursus.LazyFrame {
			return in.frame("other").Join(in.df(), ursus.JoinOn(c("k")), ursus.JoinHow(ursus.JoinLeft)).
				Sort(ursus.Asc(c("v")).NullsLast(), ursus.Asc(c("i")).NullsLast())
		},
		"unnest":                 frame(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Unnest("a") }),
		"unnest_expr":            sel(a.Struct().Unnest(), c("b")),
		"field_refused":          sel(a.Struct().Field("x")),
		"cast_to_itself":         sel(a.Cast(empty), c("i")),
		"cast_to_string_refused": sel(a.Cast(ursus.String)),
		"is_null":                sel(a.IsNull(), a.IsNotNull().Alias("nn")),
		"counts": sel(a.NullCount().Cast(ursus.Int64).Alias("nulls"), a.Count().Cast(ursus.Int64).Alias("n"),
			a.Len().Cast(ursus.Int64).Alias("len")),
		"first_last": sel(a.First(), a.Last().Alias("last")),
		"shift":      sel(c("i"), a.Shift(2)),
		"gather": func(in oracleInputs) *ursus.LazyFrame {
			df, err := in.df().Collect(in.t.Context())
			if err == nil {
				df, err = df.Gather(in.t.Context(), []int{5, 0, 0, 3, -1}, false)
			}
			return in.eager(df, err)
		},
		"implode":    sel(a.Implode()),
		"drop_nulls": frame(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.DropNulls("a") }),
		"group_value": frame(func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.GroupBy(c("k")).Agg(a.First(), a.Last().Alias("last"), a.Count().Cast(ursus.Int64).Alias("n")).Sort(ursus.Asc(c("k")))
		}),
		"group_value_list": frame(func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.GroupBy(c("k")).Agg(a).Sort(ursus.Asc(c("k")))
		}),
		"group_key": frame(func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.GroupBy(a).Agg(c("i").Min().Alias("lo"), c("i").Max().Alias("hi")).Sort(ursus.Asc(c("lo")))
		}),
		"unique":        frame(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Select(a).Unique() }),
		"with_fields":   sel(a.Struct().WithFields(c("b").Alias("z"))),
		"rename_fields": sel(a.Struct().RenameFields()),
		"match_schema_to_none": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().MatchToSchema(ursus.MustSchema(ursus.Of("s", empty)), ursus.MatchOptions{ExtraStructFields: ursus.MatchIgnore})
		},
		"match_schema_from_none": func(in oracleInputs) *ursus.LazyFrame {
			x := ursus.StructOf(ursus.Field{Name: "x", Type: ursus.Int64, Nullable: true})
			return in.df().Select(a).MatchToSchema(ursus.MustSchema(ursus.Of("a", x)), ursus.MatchOptions{MissingStructFields: ursus.MatchInsert})
		},
		"explode":                frame(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Explode("l") }),
		"nested_field":           sel(c("s").Struct().Field("e"), c("i")),
		"nested_sort":            frame(func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Sort(ursus.Desc(c("i"))) }),
		"write_ndjson":           written(head6, ndjson),
		"write_json":             written(head6, json),
		"write_ndjson_nested":    written(same, ndjson),
		"write_csv_refused":      written(same, csv),
		"ipc":                    back(ipc, readIPC),
		"ipc_stream":             back(ipcStream, readIPCStream),
		"ipc_nested":             back(ipc, readIPC),
		"ipc_listed":             back(ipc, readIPC),
		"parquet_refused":        back(parquet, readParquet),
		"parquet_nested_refused": back(parquet, readParquet),
		"parquet_listed_refused": back(parquet, readParquet),
	}
	for _, name := range []string{
		"nd_alone", "nd_beside", "nd_fields_then_none", "nd_none_then_fields", "nd_inside_a_struct", "nd_alone_inside_a_struct",
		"nd_inside_a_list", "nd_null_then_none", "nd_none_then_null", "nd_key_missing", "nd_schema_given", "nd_lines_gen",
	} {
		reg[name] = nd
	}
	for _, name := range []string{
		"json_alone", "json_beside", "json_unnamed_key", "json_one_object", "json_fields_then_none", "json_inside_a_list",
		"json_null_and_missing",
	} {
		reg[name] = js
	}
	oracleGroups["emptystruct"] = reg
}
