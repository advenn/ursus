package ursus

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"iter"
	"math"
	"strings"
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/exec"
	"github.com/advenn/ursus/internal/source/sqlrows"
	"github.com/advenn/ursus/internal/uerr"
)

// A database written (step 366, L1 of v0.6.8-scope.md): Polars' write_database.

// DatabaseWriter is what WriteDatabase writes through: a *sql.DB or a *sql.Conn, whose
// write is one transaction of its own, or a *sql.Tx, inside which it writes.
type DatabaseWriter interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
}

// IfTableExists is what WriteDatabase does with a table already there: Polars'
// if_table_exists.
type IfTableExists int

const (
	// IfExistsFail refuses a table already there, and creates one that is not: the
	// default.
	IfExistsFail IfTableExists = iota
	// IfExistsReplace drops a table already there and creates it again.
	IfExistsReplace
	// IfExistsAppend inserts into a table already there, and creates one that is not.
	IfExistsAppend
)

// SQLDialect names the database WriteDatabase writes to, which database/sql cannot
// tell: its placeholders, its quoting and its columns' types.
type SQLDialect int

const (
	// DialectAuto names the dialect by the driver's package: modernc's or mattn's
	// SQLite, pgx or lib/pq, the MySQL driver, or Microsoft's SQL Server driver. A
	// *sql.Tx shows no driver, so writing into one names its dialect.
	DialectAuto SQLDialect = iota
	DialectSQLite
	DialectPostgres
	DialectMySQL
	DialectSQLServer
)

// DatabaseWriteOption configures WriteDatabase: WithIfTableExists, WithDialect, or any
// CollectOption, which the query runs with.
type DatabaseWriteOption interface{ applyDatabaseWrite(*dbWriteCfg) }

type dbWriteCfg struct {
	ifExists IfTableExists
	dialect  SQLDialect
	collect  collectCfg
}

type dbWriteOpt func(*dbWriteCfg)

func (o dbWriteOpt) applyDatabaseWrite(c *dbWriteCfg)    { o(c) }
func (o CollectOption) applyDatabaseWrite(c *dbWriteCfg) { o(&c.collect) }

// WithIfTableExists says what to do with a table already there. Default
// IfExistsFail. New in 0.6.8.
func WithIfTableExists(mode IfTableExists) DatabaseWriteOption {
	return dbWriteOpt(func(c *dbWriteCfg) { c.ifExists = mode })
}

// WithDialect names the database's dialect, which the driver otherwise names. New in
// 0.6.8.
func WithDialect(d SQLDialect) DatabaseWriteOption {
	return dbWriteOpt(func(c *dbWriteCfg) { c.dialect = d })
}

// WriteDatabase writes the frame to table, as WriteDatabase of its LazyFrame does.
// New in 0.6.8.
func (df *DataFrame) WriteDatabase(ctx context.Context, db DatabaseWriter, table string,
	opts ...DatabaseWriteOption) (int64, error) {
	return df.Lazy().WriteDatabase(ctx, db, table, opts...)
}

// WriteDatabase runs the query and inserts its rows into table, and answers how many
// it wrote: Polars' write_database. A name of dots is a schema's table, "s.t", each
// part quoted.
//
// A table already there is refused, replaced or appended to, as WithIfTableExists
// says; one that is not is created, its columns of the types ReadDatabase reads back
// as theirs: an Int64 a BIGINT, a Float64 a DOUBLE PRECISION, text a TEXT, an instant
// a TIMESTAMP, with a zone a TIMESTAMPTZ, in the dialect's own spelling. A List, an
// Array, a Struct and a Duration have no column type, and are refused before
// anything is written.
//
// The rows go as they are read, in INSERTs of as many rows as the dialect's
// parameters allow, up to a thousand, all in one transaction: a *sql.DB's or a
// *sql.Conn's own, committed at the end and rolled back on an error, or the caller's
// *sql.Tx. A value is what ToMaps gives, but a time of day, which is its text, and an
// unsigned integer past 2^63-1, which is its text, or refused by SQLite, which holds
// none. New in 0.6.8.
func (lf *LazyFrame) WriteDatabase(ctx context.Context, db DatabaseWriter, table string,
	opts ...DatabaseWriteOption) (n int64, err error) {
	defer uerr.Catch(&err, "write_database")
	if db == nil {
		return 0, uerr.New(uerr.KindValue, "write_database", "no database to write to")
	}
	if table == "" {
		return 0, uerr.New(uerr.KindValue, "write_database", "no table named")
	}
	cfg := dbWriteCfg{collect: baseCollectCfg()}
	for _, o := range opts {
		o.applyDatabaseWrite(&cfg)
	}
	cfg.collect.finish()
	defer cfg.collect.report()
	if cfg.ifExists < IfExistsFail || cfg.ifExists > IfExistsAppend {
		return 0, uerr.New(uerr.KindValue, "write_database", "%d is not an IfTableExists", cfg.ifExists).
			Hint("IfExistsFail, IfExistsReplace or IfExistsAppend")
	}
	// The query first, so a query that cannot run says so whatever the database.
	root, err := lf.compile(ctx, cfg.collect)
	if err != nil {
		return 0, err
	}
	defer root.Close()
	schema := exec.Schema(root)
	if schema.Len() == 0 {
		return 0, uerr.New(uerr.KindValue, "write_database", "a frame of no columns has no row to insert")
	}
	d, err := dialectFor(db, cfg.dialect)
	if err != nil {
		return 0, err
	}
	// Every column's type is checked here, before any statement, whether or not the
	// table is created.
	create, err := d.CreateTable(table, schema)
	if err != nil {
		return 0, err
	}

	w := db
	if b, ok := db.(interface {
		BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	}); ok {
		tx, berr := b.BeginTx(ctx, nil)
		if berr != nil {
			return 0, uerr.Wrap(berr, uerr.KindIO, "write_database", "beginning a transaction")
		}
		// Both read the named result: the rollback on any error, the commit on none,
		// which runs first and whose own error the rollback then finds done.
		defer func() {
			if err != nil {
				err = errors.Join(err, ignoreDone(tx.Rollback()))
			}
		}()
		w = tx
		defer func() {
			if err == nil {
				if cerr := tx.Commit(); cerr != nil {
					n, err = 0, uerr.Wrap(cerr, uerr.KindIO, "write_database", "committing")
				}
			}
		}()
	}

	if err := prepareTable(ctx, w, d, table, create, cfg.ifExists); err != nil {
		return 0, err
	}
	return insertRows(ctx, w, d, table, schema, exec.Batches(ctx, root))
}

// ignoreDone drops the error of a rollback that found the transaction ended, by a
// commit or by the database itself.
func ignoreDone(err error) error {
	if errors.Is(err, sql.ErrTxDone) {
		return nil
	}
	return err
}

func dialectFor(db DatabaseWriter, d SQLDialect) (sqlrows.Dialect, error) {
	switch d {
	case DialectSQLite:
		return sqlrows.SQLite, nil
	case DialectPostgres:
		return sqlrows.Postgres, nil
	case DialectMySQL:
		return sqlrows.MySQL, nil
	case DialectSQLServer:
		return sqlrows.SQLServer, nil
	case DialectAuto:
	default:
		return 0, uerr.New(uerr.KindValue, "write_database", "%d is not a SQLDialect", d)
	}
	got := sqlrows.DialectUnknown
	switch v := db.(type) {
	case *sql.DB:
		got = sqlrows.DialectOf(v.Driver())
	case *sql.Conn:
		_ = v.Raw(func(c any) error { got = sqlrows.DialectOf(c); return nil })
	}
	if got == sqlrows.DialectUnknown {
		return 0, uerr.New(uerr.KindValue, "write_database", "cannot tell the database's dialect from %T", db).
			Hint("name it with WithDialect: DialectSQLite, DialectPostgres, DialectMySQL or DialectSQLServer")
	}
	return got, nil
}

// prepareTable makes table ready for the rows, as mode says.
func prepareTable(ctx context.Context, w DatabaseWriter, d sqlrows.Dialect, table, create string,
	mode IfTableExists) error {
	run := func(stmt string) error {
		if _, err := w.ExecContext(ctx, stmt); err != nil {
			return uerr.Wrap(err, uerr.KindIO, "write_database", "running %s", stmt)
		}
		return nil
	}
	if mode == IfExistsReplace {
		if err := run(d.DropTable(table)); err != nil {
			return err
		}
		return run(create)
	}
	there, err := tableExists(ctx, w, d, table)
	if err != nil {
		return err
	}
	switch {
	case !there:
		return run(create)
	case mode == IfExistsFail:
		return uerr.New(uerr.KindValue, "write_database", "table %q already exists", table).
			Hint("WithIfTableExists(IfExistsAppend) inserts into it, IfExistsReplace replaces it")
	}
	return nil
}

func tableExists(ctx context.Context, w DatabaseWriter, d sqlrows.Dialect, table string) (bool, error) {
	q, args := d.Exists(table)
	rows, err := w.QueryContext(ctx, q, args...)
	if err != nil {
		return false, uerr.Wrap(err, uerr.KindIO, "write_database", "asking whether table %q exists", table)
	}
	defer rows.Close()
	var v any
	if rows.Next() {
		if err := rows.Scan(&v); err != nil {
			return false, uerr.Wrap(err, uerr.KindIO, "write_database", "asking whether table %q exists", table)
		}
	}
	if err := rows.Err(); err != nil {
		return false, uerr.Wrap(err, uerr.KindIO, "write_database", "asking whether table %q exists", table)
	}
	switch v := v.(type) {
	case bool:
		return v, nil
	case int64:
		return v != 0, nil
	case []byte:
		s := string(v)
		return s != "0" && s != "" && s != "f" && s != "false", nil
	case string:
		return v != "0" && v != "" && v != "f" && v != "false", nil
	case nil:
		return false, nil
	}
	return false, uerr.New(uerr.KindIO, "write_database", "asking whether table %q exists answered %T", table, v)
}

// insertRows inserts every batch's rows into table, in INSERTs of the dialect's size.
func insertRows(ctx context.Context, w DatabaseWriter, d sqlrows.Dialect, table string,
	schema *dtype.Schema, batches iter.Seq2[*data.Batch, error]) (int64, error) {
	cols := schema.Names()
	per := d.RowsPerInsert(len(cols))
	full, err := w.PrepareContext(ctx, d.Insert(table, cols, per))
	if err != nil {
		return 0, uerr.Wrap(err, uerr.KindIO, "write_database", "preparing an INSERT into %q", table)
	}
	defer full.Close()

	var (
		args    = make([]any, 0, per*len(cols))
		pending int
		written int64
	)
	flush := func(stmt *sql.Stmt) error {
		if _, err := stmt.ExecContext(ctx, args...); err != nil {
			return uerr.Wrap(err, uerr.KindIO, "write_database", "inserting %d rows into %q", pending, table)
		}
		written += int64(pending)
		args, pending = args[:0], 0
		return nil
	}
	for b, err := range batches {
		if err != nil {
			return 0, err
		}
		readers := make([]func(int) (any, error), b.NumCols())
		for j := range readers {
			r, err := sqlArg(b.Column(j), d)
			if err != nil {
				return 0, uerr.Wrap(err, uerr.KindType, "write_database", "column %q", cols[j])
			}
			readers[j] = r
		}
		for i := range b.Rows() {
			for _, r := range readers {
				v, err := r(i)
				if err != nil {
					return 0, err
				}
				args = append(args, v)
			}
			if pending++; pending == per {
				if err := flush(full); err != nil {
					return 0, err
				}
			}
		}
	}
	if pending > 0 {
		stmt, err := w.PrepareContext(ctx, d.Insert(table, cols, pending))
		if err != nil {
			return 0, uerr.Wrap(err, uerr.KindIO, "write_database", "preparing an INSERT into %q", table)
		}
		defer stmt.Close()
		if err := flush(stmt); err != nil {
			return 0, err
		}
	}
	return written, nil
}

// sqlArg reads a column's values as a driver takes them: ToMaps' values, but a time
// of day as its text, an Int128 as its text, an unsigned integer past 2^63-1 as its
// text, which a driver's own conversion refuses, a date or an instant for SQLite as
// SQLite's text of one, and an instant for MySQL at its UTC wall clock, which a
// DATETIME holds.
func sqlArg(c *data.Column, d sqlrows.Dialect) (func(int) (any, error), error) {
	read, err := cellReader(c)
	if err != nil {
		return nil, err
	}
	plain := func(i int) (any, error) { return read(i), nil }
	switch t := c.DType(); t.ID() {
	case dtype.TypeTime:
		return func(i int) (any, error) {
			v := read(i)
			if v == nil {
				return nil, nil
			}
			return timeOfDay(v.(time.Duration)), nil
		}, nil
	case dtype.TypeInt128:
		return func(i int) (any, error) {
			v := read(i)
			if v == nil {
				return nil, nil
			}
			return v.(i128.Int128).String(), nil
		}, nil
	case dtype.TypeUint64:
		return func(i int) (any, error) {
			v := read(i)
			if v == nil || v.(uint64) <= math.MaxInt64 {
				return v, nil
			}
			if d == sqlrows.SQLite {
				return nil, uerr.New(uerr.KindValue, "write_database",
					"column %q holds %d, past the 64-bit signed integers SQLite holds", c.Name(), v)
			}
			return fmt.Sprint(v), nil
		}, nil
	case dtype.TypeDate, dtype.TypeDatetime:
		if d == sqlrows.SQLite {
			// SQLite keeps an instant as text, and its own date functions read this
			// form: a date, a wall clock, and an offset where the instant has a zone.
			layout := "2006-01-02"
			switch {
			case t.ID() == dtype.TypeDate:
			case t.TimeZone() == "":
				layout = "2006-01-02 15:04:05.999999999"
			default:
				layout = "2006-01-02 15:04:05.999999999-07:00"
			}
			return func(i int) (any, error) {
				v := read(i)
				if v == nil {
					return nil, nil
				}
				return v.(time.Time).Format(layout), nil
			}, nil
		}
		if d == sqlrows.MySQL && t.TimeZone() != "" {
			return func(i int) (any, error) {
				v := read(i)
				if v == nil {
					return nil, nil
				}
				return v.(time.Time).UTC(), nil
			}, nil
		}
	}
	return plain, nil
}

// timeOfDay is a time of day's text, as a TIME column takes it: 15:04:05, and its
// fraction of a second where it has one, without trailing zeros.
func timeOfDay(since time.Duration) string {
	ns := int64(since)
	h, ns := ns/int64(time.Hour), ns%int64(time.Hour)
	m, ns := ns/int64(time.Minute), ns%int64(time.Minute)
	s, ns := ns/int64(time.Second), ns%int64(time.Second)
	out := fmt.Sprintf("%02d:%02d:%02d", h, m, s)
	if ns > 0 {
		out += strings.TrimRight(fmt.Sprintf(".%09d", ns), "0")
	}
	return out
}
