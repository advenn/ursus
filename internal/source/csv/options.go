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

	// SkipLines discards this many lines before anything is read, by their line
	// ends alone, a quote read as any byte, as Polars' skip_lines (step 358). It and
	// SkipRows are not both set, as Polars refuses them both.
	SkipLines int

	// SkipRowsAfterHeader discards this many records after the header, or at the
	// start where there is none, a blank line one, as Polars' skip_rows_after_header.
	SkipRowsAfterHeader int

	// NoQuote reads a quote as any byte, as Polars' quote_char=None: no field is
	// quoted, and a separator or a line end always ends one.
	NoQuote bool

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

	// TruncateRaggedLines accepts a record of too many fields, the extra ones
	// dropped, as Polars' truncate_ragged_lines. A record of too few is padded with
	// nulls whatever it says, and a blank line read as a record of nulls, as Polars
	// reads them (step 357).
	TruncateRaggedLines bool

	// MaxRecordSize bounds one record. 0 selects the default (16 MiB).
	MaxRecordSize int

	// TryParseDates infers a column of dates, instants or times of day as a Date, a
	// Datetime in microseconds, in UTC where its values have offsets, or a Time, by
	// Polars' patterns, as Polars' try_parse_dates (step 355).
	TryParseDates bool

	// IgnoreErrors reads a value its column's type cannot take as a null, as Polars'
	// ignore_errors (step 357). A record of too many fields is refused still, and
	// text that is not UTF-8, as Polars refuses them.
	IgnoreErrors bool

	// NoInference reads every column as a String but those a schema or an override
	// types, as Polars' infer_schema=False.
	NoInference bool

	// AllowEmpty reads an empty file as a frame of no columns, as Polars'
	// raise_if_empty=False; otherwise it is refused.
	AllowEmpty bool

	// DecimalComma reads a float's and a Decimal's point as a comma, 1,5, as Polars'
	// decimal_comma: a value with a '.' is then no number.
	DecimalComma bool

	// Lossy reads text that is not UTF-8 with each invalid sequence a U+FFFD, as
	// Polars' encoding="utf8-lossy"; otherwise a String's such text is refused, as
	// Polars refuses it.
	Lossy bool
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
