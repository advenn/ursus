package ursus_test

// The I/O groups' layouts, options and shared differences (step 502). A case over a
// directory of files records the layout as the param files, each file's relative
// path to its text or to the name of the input frame it holds (bench/scripts/oracle/
// iotext.py); the methods here write the same layout under a temporary directory with
// ursus's own writers, and the registry asks the same question of it. A path column is
// stripped of the directory's root on both sides, so a recording holds relative paths.
//
// A reader's or a writer's options are the param options, the keyword arguments the
// recording asked Polars with (iotext.options); in.csvOptions and its kin spell each
// as ursus's option, so both sides read and write with the same options, and an
// argument none of them takes fails the case.

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
)

// textFiles writes the case's layout of texts and returns the directory's root.
func (in oracleInputs) textFiles() string {
	root := in.t.TempDir()
	for rel, text := range in.texts("files") {
		oracleWriteFile(in, root, rel, func(path string) error { return os.WriteFile(path, []byte(text), 0o644) })
	}
	return root
}

// frameFiles writes the case's layout of frames, each by write, and returns the
// directory's root.
func (in oracleInputs) frameFiles(write func(lf *ursus.LazyFrame, path string) error) string {
	root := in.t.TempDir()
	for rel, name := range in.texts("files") {
		lf := in.frame(name)
		oracleWriteFile(in, root, rel, func(path string) error { return write(lf, path) })
	}
	return root
}

func oracleWriteFile(in oracleInputs, root, rel string, write func(path string) error) {
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		in.t.Fatal(err)
	}
	if err := write(path); err != nil {
		in.t.Fatal(err)
	}
}

// oracleRelative strips the directory's root off a path column, as the recording's
// query did (iotext.strip).
func oracleRelative(name, root string) ursus.Expr {
	return ursus.Col(name).Str().StripPrefix(root + "/")
}

// oracleListing is every file below dir by its path from dir, sorted, a line each: a
// dataset's layout as a written text (iotext.listing).
func oracleListing(in oracleInputs, dir string) string { return strings.Join(files(in.t, dir), "\n") }

// oracleStrings is the case's param name, a list of strings, or nil if it has none.
func oracleStrings(in oracleInputs, name string) []string {
	list, ok := in.params[name].([]any)
	if !ok {
		return nil
	}
	out := make([]string, len(list))
	for i, v := range list {
		out[i] = oracleString(in.t, v)
	}
	return out
}

// shaped is lf as the recording shaped the frame it read: the params select (a
// column), then head or tail (a row count), where the case has them.
func (in oracleInputs) shaped(lf *ursus.LazyFrame) *ursus.LazyFrame {
	if v, ok := in.params["select"]; ok {
		lf = lf.Select(ursus.Col(oracleString(in.t, v)))
	}
	if v, ok := in.params["head"]; ok {
		lf = lf.Head(int(oracleInt(in.t, v)))
	}
	if v, ok := in.params["tail"]; ok {
		lf = lf.Tail(int(oracleInt(in.t, v)))
	}
	return lf
}

// oracleRefusedAs is q, whose refusal must be of kind as well (ursus.ErrValue,
// ErrSchema, ErrType): the recording holds Polars' refusal and the phrase ursus's
// must name, and the kind is ursus's own. It collects q's frame itself, so that the
// error is seen here.
func oracleRefusedAs(kind error, q oracleQuery) oracleQuery {
	return func(in oracleInputs) *ursus.LazyFrame {
		lf := q(in)
		err := *in.failed
		if err == nil {
			var df *ursus.DataFrame
			if df, err = lf.Collect(in.t.Context()); err == nil {
				return df.Lazy()
			}
			*in.failed = err
		}
		if !errors.Is(err, kind) {
			in.t.Errorf("refused as a %s error, want %v: %v", kindOf(err), kind, err)
		}
		return nil
	}
}

// --- options -------------------------------------------------------------------------

// oracleOptions is the case's param options: the keyword arguments the recording
// asked Polars' reader or writer with, a type spelled as ursus prints it, a schema as
// its [name, type] pairs in order, overrides by column name.
type oracleOptions struct {
	in   oracleInputs
	args map[string]any
	used map[string]bool
}

func (in oracleInputs) options() *oracleOptions {
	args, _ := in.params["options"].(map[string]any)
	return &oracleOptions{in: in, args: args, used: map[string]bool{}}
}

// arg is the argument name, and whether the recording asked with it.
func (o *oracleOptions) arg(name string) (any, bool) {
	v, ok := o.args[name]
	if ok {
		o.used[name] = true
	}
	return v, ok
}

func (o *oracleOptions) bool(name string, set func(bool)) {
	if v, ok := o.arg(name); ok {
		b, isBool := v.(bool)
		if !isBool {
			o.in.t.Fatalf("the option %s is %v, not a Boolean", name, v)
		}
		set(b)
	}
}

func (o *oracleOptions) int(name string, set func(int)) {
	if v, ok := o.arg(name); ok {
		set(int(oracleInt(o.in.t, v)))
	}
}

func (o *oracleOptions) text(name string, set func(string)) {
	if v, ok := o.arg(name); ok {
		set(oracleString(o.in.t, v))
	}
}

// char is an argument of one byte: a separator or a quote.
func (o *oracleOptions) char(name string, set func(byte)) {
	o.text(name, func(s string) {
		if len(s) != 1 {
			o.in.t.Fatalf("the option %s is %q, not one byte", name, s)
		}
		set(s[0])
	})
}

// types is an argument of types by column name: schema overrides.
func (o *oracleOptions) types(name string, set func(map[string]dtype.DataType)) {
	v, ok := o.arg(name)
	if !ok {
		return
	}
	obj, isObj := v.(map[string]any)
	if !isObj {
		o.in.t.Fatalf("the option %s is %v, not types by name", name, v)
	}
	m := make(map[string]dtype.DataType, len(obj))
	for col, dt := range obj {
		m[col] = oracleDType(o.in.t, oracleString(o.in.t, dt))
	}
	set(m)
}

// schema is an argument of [name, type] pairs: a schema, in its order.
func (o *oracleOptions) schema(name string, set func(*ursus.Schema)) {
	v, ok := o.arg(name)
	if !ok {
		return
	}
	pairs, isList := v.([]any)
	if !isList {
		o.in.t.Fatalf("the option %s is %v, not [name, type] pairs", name, v)
	}
	fields := make([]dtype.Field, len(pairs))
	for i, p := range pairs {
		pair, _ := p.([]any)
		if len(pair) != 2 {
			o.in.t.Fatalf("the option %s holds %v, not a [name, type] pair", name, p)
		}
		fields[i] = dtype.Of(oracleString(o.in.t, pair[0]), oracleDType(o.in.t, oracleString(o.in.t, pair[1])))
	}
	set(dtype.MustSchema(fields...))
}

// done fails the case on an argument no spelling took.
func (o *oracleOptions) done() {
	for name, v := range o.args {
		if !o.used[name] {
			o.in.t.Fatalf("the option %s=%v has no spelling here", name, v)
		}
	}
}

// csvOptions is the options as read_csv's and scan_csv's, ursus's CSV options.
func (in oracleInputs) csvOptions() []ursus.CSVOption {
	o := in.options()
	var out []ursus.CSVOption
	add := func(opt ursus.CSVOption) { out = append(out, opt) }
	o.bool("has_header", func(b bool) { add(ursus.WithHasHeader(b)) })
	o.char("separator", func(c byte) { add(ursus.WithSeparator(c)) })
	// quote_char None reads no quotes, a byte quotes by it.
	if v, ok := o.arg("quote_char"); ok && v == nil {
		add(ursus.WithQuoting(false))
	} else if ok {
		s := oracleString(in.t, v)
		if len(s) != 1 {
			in.t.Fatalf("the option quote_char is %q, not one byte", s)
		}
		add(ursus.WithQuote(s[0]))
	}
	o.int("skip_rows", func(n int) { add(ursus.WithSkipRows(n)) })
	o.int("skip_lines", func(n int) { add(ursus.WithSkipLines(n)) })
	o.int("skip_rows_after_header", func(n int) { add(ursus.WithSkipRowsAfterHeader(n)) })
	o.schema("schema", func(s *ursus.Schema) { add(ursus.WithSchema(s)) })
	o.types("schema_overrides", func(m map[string]dtype.DataType) { add(ursus.WithSchemaOverrides(m)) })
	o.bool("infer_schema", func(b bool) { add(ursus.WithInferSchema(b)) })
	o.bool("ignore_errors", func(b bool) { add(ursus.WithIgnoreErrors(b)) })
	o.bool("raise_if_empty", func(b bool) { add(ursus.WithRaiseIfEmpty(b)) })
	o.bool("decimal_comma", func(b bool) { add(ursus.WithDecimalComma(b)) })
	o.bool("try_parse_dates", func(b bool) { add(ursus.WithTryParseDates(b)) })
	o.text("include_file_paths", func(name string) { add(ursus.WithCSVIncludeFilePaths(name)) })
	o.text("encoding", func(e string) {
		switch e {
		case "utf8-lossy":
			add(ursus.WithEncoding(ursus.UTF8Lossy))
		case "utf8":
		default:
			in.t.Fatalf("no CSV encoding is spelled %q", e)
		}
	})
	o.done()
	return out
}

// ndjsonOptions is the options as read_ndjson's, read_json's and scan_ndjson's,
// ursus's NDJSON options; infer_schema_length None is every line's.
func (in oracleInputs) ndjsonOptions() []ursus.NDJSONOption {
	o := in.options()
	var out []ursus.NDJSONOption
	add := func(opt ursus.NDJSONOption) { out = append(out, opt) }
	o.schema("schema", func(s *ursus.Schema) { add(ursus.WithNDJSONSchema(s)) })
	o.types("schema_overrides", func(m map[string]dtype.DataType) { add(ursus.WithNDJSONSchemaOverrides(m)) })
	if v, ok := o.arg("infer_schema_length"); ok {
		n := 0
		if v != nil {
			n = int(oracleInt(in.t, v))
		}
		add(ursus.WithInferSchemaLines(n))
	}
	o.bool("ignore_errors", func(b bool) { add(ursus.WithNDJSONIgnoreErrors(b)) })
	o.text("include_file_paths", func(name string) { add(ursus.WithNDJSONIncludeFilePaths(name)) })
	o.done()
	return out
}

// databaseOptions is the options as read_database's. Every row's value types a
// column here (feature-gaps.md §9), which the recording asks of Polars as
// infer_schema_length None; ursus has no other spelling of it.
func (in oracleInputs) databaseOptions() []ursus.DatabaseOption {
	o := in.options()
	var out []ursus.DatabaseOption
	o.types("schema_overrides", func(m map[string]dtype.DataType) { out = append(out, ursus.WithDatabaseSchemaOverrides(m)) })
	if v, ok := o.arg("infer_schema_length"); ok && v != nil {
		in.t.Fatalf("infer_schema_length=%v: every row types a column here, None in Polars", v)
	}
	o.done()
	return out
}

// parquetOptions is the options as scan_parquet's.
func (in oracleInputs) parquetOptions() []ursus.ParquetOption {
	o := in.options()
	var out []ursus.ParquetOption
	add := func(opt ursus.ParquetOption) { out = append(out, opt) }
	o.bool("hive_partitioning", func(b bool) { add(ursus.WithHivePartitioning(b)) })
	o.schema("hive_schema", func(s *ursus.Schema) { add(ursus.WithHiveSchema(s)) })
	o.bool("try_parse_hive_dates", func(b bool) { add(ursus.WithHiveDates(b)) })
	o.text("include_file_paths", func(name string) { add(ursus.WithIncludeFilePaths(name)) })
	o.done()
	return out
}

// ipcOptions is the options as scan_ipc's.
func (in oracleInputs) ipcOptions() []ursus.IPCScanOption {
	o := in.options()
	var out []ursus.IPCScanOption
	add := func(opt ursus.IPCScanOption) { out = append(out, opt) }
	o.bool("hive_partitioning", func(b bool) { add(ursus.WithIPCHivePartitioning(b)) })
	o.schema("hive_schema", func(s *ursus.Schema) { add(ursus.WithIPCHiveSchema(s)) })
	o.bool("try_parse_hive_dates", func(b bool) { add(ursus.WithIPCHiveDates(b)) })
	o.text("include_file_paths", func(name string) { add(ursus.WithIPCIncludeFilePaths(name)) })
	o.done()
	return out
}

// csvWriteOptions is the options as write_csv's, ursus's CSV writer's.
func (in oracleInputs) csvWriteOptions() []ursus.CSVSinkOption {
	o := in.options()
	var out []ursus.CSVSinkOption
	add := func(opt ursus.CSVSinkOption) { out = append(out, opt) }
	o.char("separator", func(c byte) { add(ursus.WithWriteSeparator(c)) })
	o.char("quote_char", func(c byte) { add(ursus.WithWriteQuote(c)) })
	o.text("quote_style", func(s string) {
		styles := map[string]ursus.QuoteStyle{"necessary": ursus.QuoteNecessary, "always": ursus.QuoteAlways,
			"never": ursus.QuoteNever, "non_numeric": ursus.QuoteNonNumeric}
		style, ok := styles[s]
		if !ok {
			in.t.Fatalf("no quote style is spelled %q", s)
		}
		add(ursus.WithQuoteStyle(style))
	})
	o.bool("include_header", func(b bool) { add(ursus.WithWriteHeader(b)) })
	o.bool("include_bom", func(b bool) { add(ursus.WithIncludeBOM(b)) })
	o.text("null_value", func(s string) { add(ursus.WithNullValue(s)) })
	o.text("date_format", func(f string) { add(ursus.WithDateFormat(f)) })
	o.text("datetime_format", func(f string) { add(ursus.WithDatetimeFormat(f)) })
	o.text("time_format", func(f string) { add(ursus.WithTimeFormat(f)) })
	o.int("float_precision", func(n int) { add(ursus.WithFloatPrecision(n)) })
	o.bool("float_scientific", func(b bool) { add(ursus.WithFloatScientific(b)) })
	o.bool("decimal_comma", func(b bool) { add(ursus.WithWriteDecimalComma(b)) })
	o.done()
	return out
}

// --- the differences every I/O group names ------------------------------------------

// oracleHeaderless is the documented naming of a headerless CSV's n columns:
// column_1, ... here, column_0, ... in Polars 2 (step 487 §3, feature-gaps.md §11).
func oracleHeaderless(n int) oracleDifference {
	names := make(map[string]string, n)
	for i := range n {
		names["column_"+strconv.Itoa(i)] = "column_" + strconv.Itoa(i+1)
	}
	return oracleDifference{why: "a headerless CSV's columns are column_1, ... here, column_0, ... in Polars 2 (step 487 §3)", names: names}
}

// oracleCount is the documented type of a count: a Uint64 here, so that it cannot
// wrap at 2^32 rows (internal/expr.ResolveAggBinding), Polars' a UInt32.
func oracleCount(col string) oracleDifference {
	return oracleDifference{why: "a count is a Uint64 here, Polars' a UInt32", dtypes: map[string]string{col: "Uint64"}}
}
