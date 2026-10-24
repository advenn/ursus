package ursus

import (
	"context"
	"database/sql"
	"maps"

	"github.com/advenn/ursus/internal/source/sqlrows"
	"github.com/advenn/ursus/internal/uerr"
)

// Queryer is what ReadDatabase runs a query on: a *sql.DB, a *sql.Tx or a *sql.Conn.
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// DatabaseOption configures ReadDatabaseRows.
type DatabaseOption func(*sqlrows.Options)

// WithDatabaseSchemaOverrides gives the named columns their types, as Polars'
// schema_overrides does: each value is cast to its column's type, and one that does not
// fit it is an error. A name the result has no column of is an error too. New in 0.6.7.
func WithDatabaseSchemaOverrides(types map[string]DataType) DatabaseOption {
	return func(o *sqlrows.Options) {
		if o.Overrides == nil {
			o.Overrides = make(map[string]DataType, len(types))
		}
		maps.Copy(o.Overrides, types)
	}
}

// ReadDatabase runs query, with args, on db and reads its rows as a frame: Polars'
// read_database.
//
// A column's type is its values', as the driver gives them: integers an Int64, floats
// a Float64, Booleans, text a String, bytes a Binary, and instants a Datetime in
// microseconds, of no zone where every one is in UTC, as a driver gives a timestamp
// without one, and in UTC where one is not. Values of two kinds meet as Polars meets
// them: an integer and a float at Float64, a Boolean and a number at the number, text
// and a number, a Boolean or an instant at text, and text and bytes at bytes; two that
// do not meet are an error. Every row is read for it, where Polars reads 100 and
// truncates or refuses a later one.
//
// Then a column is of the type its driver names, where every value fits it: an integer
// of the width the driver scans it to, a Float32, a Boolean; a DECIMAL of its precision
// and scale, or of 38 digits where it gives none and the values are text, as Polars
// reads a Python Decimal; a DATE, a TIME, or a TIMESTAMP in a zone, from an instant's
// wall clock or parsed from text; and text where the driver gives bytes for a type with
// a name that is not a binary one. A column of no value is of its driver's type, or a
// Null. NOT NULL, where the driver says it of a column, makes its field non-nullable.
//
// The frame is read whole, as Polars' is. ReadDatabaseRows reads a query already run,
// and takes options. New in 0.6.7.
func ReadDatabase(ctx context.Context, db Queryer, query string, args ...any) (*DataFrame, error) {
	if db == nil {
		return nil, uerr.New(uerr.KindValue, "read_database", "no database to query")
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, uerr.Wrap(err, uerr.KindIO, "read_database", "running the query")
	}
	return ReadDatabaseRows(rows)
}

// ReadDatabaseRows reads every row of rows as a frame, as ReadDatabase reads its
// query's, and closes it: a statement's result, or one a caller ran itself. New in
// 0.6.7.
func ReadDatabaseRows(rows *sql.Rows, opts ...DatabaseOption) (*DataFrame, error) {
	if rows == nil {
		return nil, uerr.New(uerr.KindValue, "read_database", "no rows to read")
	}
	var o sqlrows.Options
	for _, opt := range opts {
		opt(&o)
	}
	b, err := sqlrows.Read(rows, o)
	if err != nil {
		return nil, err
	}
	return &DataFrame{batch: b}, nil
}
