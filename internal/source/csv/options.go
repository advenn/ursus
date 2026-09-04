// Package csv reads and writes delimited text files.
//
// It is a streaming source: it never holds more than one batch plus one buffer,
// so a file larger than memory is a normal case rather than a special one.
//
// # What it pushes down
//
// Projection, by not PARSING the columns nobody asked for. Field splitting is
// unavoidable — field 7 cannot be found without walking past 1–6 — but converting
// bytes to values is skipped for every column outside the projection, which is
// where essentially all the cost is.
//
// ScanSpec.MaxRows, by stopping. Head(10) on a 10 GB file is the first thing
// anyone types, and it should take a millisecond.
//
// Predicates: nothing. See Caps.
package csv

import (
	"github.com/advenn/ursus/dtype"
)

// Options configures a CSV source. The zero value is not usable; call
// DefaultOptions and adjust.
type Options struct {
	// Separator and Quote are single bytes. Multi-byte separators are not
	// supported: they are rare, and supporting them would put a byte-compare in
	// the innermost loop of the scanner for every file.
	Separator byte
	Quote     byte

	// Comment, if non-empty, makes any line starting with it a comment. It is a
	// byte slice rather than a byte because "//" and "#!" are both real.
	Comment []byte

	// HasHeader treats the first record as column names.
	HasHeader bool

	// ColumnNames overrides the names, whether or not there is a header. With
	// HasHeader false and no names, columns are called column_1, column_2, …
	ColumnNames []string

	// SkipRows discards this many records before the header is read. Files with a
	// banner above the header are common enough that leaving this out means
	// telling people to preprocess with sed.
	SkipRows int

	// Schema, if set, skips inference entirely. This is the option to reach for
	// when the file is big: inference costs a read of the sample, and an explicit
	// schema also removes the risk of a column being inferred from rows that do
	// not represent it.
	Schema *dtype.Schema

	// SchemaOverrides fixes individual columns while inferring the rest. The
	// common case is one identity column that looks numeric and must not be.
	SchemaOverrides map[string]dtype.DataType

	// InferRows bounds how many records inference reads. 0 means the whole file,
	// which is exact and, on a large file, expensive.
	InferRows int

	// NullValues are the texts that mean NULL, in addition to the empty string for
	// non-string columns. Case-sensitive.
	NullValues []string

	// TruncateRaggedLines accepts records with the wrong field count: extra fields
	// are dropped, missing ones become null. Off by default, because a changed
	// field count usually means the file is not what the reader thinks it is, and
	// silently padding with nulls turns that into a data problem discovered much
	// later.
	TruncateRaggedLines bool

	// MaxRecordSize bounds one record. 0 selects the default (16 MiB).
	MaxRecordSize int
}

// DefaultOptions returns RFC 4180 defaults with a header.
func DefaultOptions() Options {
	return Options{
		Separator: ',',
		Quote:     '"',
		HasHeader: true,
		InferRows: 100,
		// "NULL" and "null" are not defaults. In a string column they are values,
		// and a reader that silently turns the four characters N-U-L-L into a
		// missing value cannot be argued with after the fact. The empty string
		// already means null for non-string columns, which covers the common case.
		NullValues: nil,
	}
}

// normalise fills in anything the caller left at its zero value, so a
// hand-constructed Options still works.
func (o Options) normalise() Options {
	if o.Separator == 0 {
		o.Separator = ','
	}
	if o.Quote == 0 {
		o.Quote = '"'
	}
	if o.MaxRecordSize <= 0 {
		o.MaxRecordSize = defaultMaxRecord
	}
	return o
}
