package ursus

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/source/ndjson"
	"github.com/advenn/ursus/internal/uerr"
)

// NDJSON read (step 334): Polars' read_ndjson and scan_ndjson.

// NDJSONOption configures an NDJSON scan.
type NDJSONOption func(*ndjsonCfg)

type ndjsonCfg struct {
	opts ndjson.Options
	err  error
}

// WithNDJSONSchema reads the lines as schema, its keys as its columns in its order and
// no other key, as Polars' schema=: no line is read to infer one. A value is read as
// JSON gives it and cast to its column's type, so a Date column reads "2024-01-02".
func WithNDJSONSchema(s *Schema) NDJSONOption {
	return func(c *ndjsonCfg) { c.opts.Schema = s }
}

// WithNDJSONSchemaOverrides reads the named columns as these types, the others'
// inferred or given, as Polars' schema_overrides: a column of numbers read as text,
// or of text as dates. Each name must be one of the schema's. New in 0.6.7.
func WithNDJSONSchemaOverrides(m map[string]dtype.DataType) NDJSONOption {
	return func(c *ndjsonCfg) { c.opts.Overrides = m }
}

// WithInferSchemaLines infers the schema from the first n lines, 100 unless set, as
// Polars' infer_schema_length; 0 reads every line first. A JSON document's are its
// objects.
func WithInferSchemaLines(n int) NDJSONOption {
	return func(c *ndjsonCfg) {
		if n < 0 {
			c.err = uerr.New(uerr.KindValue, "scan_ndjson", "infer_schema_length is %d, and must not be negative", n)
		}
		c.opts.InferLines = n
	}
}

// WithNDJSONIgnoreErrors reads a value its column cannot take as a null, as Polars'
// ignore_errors: text in an Int64 column, an element of a list that does not fit its
// type, a Date column's "not a date". A line that is not JSON is still refused.
func WithNDJSONIgnoreErrors(b bool) NDJSONOption {
	return func(c *ndjsonCfg) { c.opts.IgnoreErrors = b }
}

func scanNDJSON(desc string, files []ndjson.File, opts []NDJSONOption) *LazyFrame {
	cfg := ndjsonCfg{opts: ndjson.Options{InferLines: ndjson.DefaultInferLines}}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.err != nil {
		return &LazyFrame{err: cfg.err}
	}
	if len(files) == 0 {
		return &LazyFrame{err: uerr.New(uerr.KindValue, "scan_ndjson", "no files given")}
	}
	return scan(ndjson.New(desc, files, cfg.opts))
}

func ndjsonFile(path string) ndjson.File {
	return ndjson.File{Name: path, Open: func(context.Context) (io.ReadCloser, error) { return os.Open(path) }}
}

// ScanNDJSON reads a file of newline-delimited JSON, a JSON object a line, as Polars'
// scan_ndjson and read_ndjson: a column a key, in the order the keys are first seen.
// The types are inferred from the first 100 lines, as Polars infers them:
//
//   - an integer is an Int64, or an Int128 past it; a number with a fraction or an
//     exponent a Float64; true and false a Boolean; text a String; only nulls Null;
//   - an array a List and an object a Struct, of their values' types;
//   - a key holding integers and floats Float64, and text beside anything String, the
//     other value as Polars writes it: a number as Rust prints it, 1.5 for 1.50, and
//     an array or an object as JSON;
//   - an array's elements by Polars' rules for them: a Boolean and an integer meet in
//     Int64, and two types no rule meets in String, so [1.0, {"b": 2}] is a
//     List(String).
//
// A Boolean beside a number, or a List or a Struct beside a number or a Boolean, has
// no type in common and is refused, as Polars refuses it, as is a later line whose
// value does not fit its column, unless WithNDJSONIgnoreErrors. A key missing from a
// line is a null, a key given twice its first value, a key the schema does not have
// skipped, and a blank line too. An error names its line and its key.
//
// The file is read in blocks, each cut at its lines into a part a thread, the parts
// parsed at once; only the columns the query uses are built, and a query that wants
// only the first rows reads little more than them.
func ScanNDJSON(path string, opts ...NDJSONOption) *LazyFrame {
	return scanNDJSON(path, []ndjson.File{ndjsonFile(path)}, opts)
}

// ScanNDJSONFiles reads several NDJSON files as one frame, in the order given, as
// Polars' scan_ndjson of a list: the schema is inferred from the first file's first
// lines, and a key a later file adds is skipped.
func ScanNDJSONFiles(paths []string, opts ...NDJSONOption) *LazyFrame {
	files := make([]ndjson.File, len(paths))
	for i, p := range paths {
		files[i] = ndjsonFile(p)
	}
	desc := ""
	if len(paths) > 0 {
		desc = paths[0]
	}
	if len(paths) > 1 {
		desc += " and " + strconv.Itoa(len(paths)-1) + " more"
	}
	return scanNDJSON(desc, files, opts)
}

// ScanNDJSONGlob reads every file matching a shell pattern as one frame, as
// ScanNDJSONFiles of the matches sorted by name.
func ScanNDJSONGlob(pattern string, opts ...NDJSONOption) *LazyFrame {
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return &LazyFrame{err: uerr.Wrap(err, uerr.KindValue, "scan_ndjson", "bad glob pattern %q", pattern)}
	}
	if len(paths) == 0 {
		return &LazyFrame{err: uerr.New(uerr.KindIO, "scan_ndjson", "no files match %q", pattern)}
	}
	sort.Strings(paths)
	return ScanNDJSONFiles(paths, opts...)
}

// ScanNDJSONBytes is ScanNDJSON of NDJSON in memory, named name in Explain.
func ScanNDJSONBytes(b []byte, name string, opts ...NDJSONOption) *LazyFrame {
	open := func(context.Context) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }
	return scanNDJSON(name, []ndjson.File{{Name: name, Open: open}}, opts)
}

// NDJSONFile is one NDJSON file, read as a stream from wherever it lives by code that
// knows how to reach it, as CSVFile.
type NDJSONFile struct {
	// Name identifies the file in errors and in Explain.
	Name string

	// Open returns the file's bytes from the start. It is called more than once
	// a query, once to infer the schema and once a run, and must return a fresh
	// stream each time. ctx is the query's and outlives the stream, which ursus
	// closes when it is done with it.
	Open func(ctx context.Context) (io.ReadCloser, error)
}

// ScanNDJSONFrom reads NDJSON files the caller opens, as one frame in the order
// given, under ScanNDJSONFiles' rules: an object store's, say, each Open the body of
// a GET.
func ScanNDJSONFrom(files []NDJSONFile, opts ...NDJSONOption) *LazyFrame {
	fs := make([]ndjson.File, len(files))
	desc := ""
	for i, f := range files {
		fs[i] = ndjson.File{Name: f.Name, Open: f.Open}
		if fs[i].Name == "" {
			fs[i].Name = "ndjson file " + strconv.Itoa(i+1)
		}
		if f.Open == nil {
			return &LazyFrame{err: uerr.New(uerr.KindValue, "scan_ndjson", "%s has no Open", fs[i].Name)}
		}
	}
	if len(fs) > 0 {
		desc = fs[0].Name
	}
	if len(fs) > 1 {
		desc += " and " + strconv.Itoa(len(fs)-1) + " more"
	}
	return scanNDJSON(desc, fs, opts)
}

// JSON read (step 345): Polars' read_json.

// ScanJSON reads a JSON file of an array of objects, a row an object, or of one
// object, one row, as Polars' read_json: each object is read as ScanNDJSON reads a
// line, under its options and rules, the schema inferred from the first 100 objects
// unless given. As Polars, it refuses an element that is not an object, a null among
// them, a comma before the array's end, text after it, and a key the inferred schema
// has not, which a given schema's reading skips. An error names its object, counted
// from 1. New in 0.6.7.
//
// The file is read as a stream, each object made a line of NDJSON in the buffer it is
// read into and parsed as one, in blocks a part a thread: the document is never held
// whole, nor parsed into a tree.
func ScanJSON(path string, opts ...NDJSONOption) *LazyFrame {
	return scanJSON(path, ndjsonFile(path), opts)
}

// ScanJSONBytes is ScanJSON of a JSON document in memory, named name in Explain. New
// in 0.6.7.
func ScanJSONBytes(b []byte, name string, opts ...NDJSONOption) *LazyFrame {
	open := func(context.Context) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }
	return scanJSON(name, ndjson.File{Name: name, Open: open}, opts)
}

func scanJSON(desc string, f ndjson.File, opts []NDJSONOption) *LazyFrame {
	return scanNDJSON(desc, []ndjson.File{f}, append(opts[:len(opts):len(opts)], func(c *ndjsonCfg) { c.opts.JSON = true }))
}
