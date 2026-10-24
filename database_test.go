package ursus_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/advenn/ursus"
)

// A fake database/sql driver, whose queries are canned results named by their text;
// "args" answers its arguments as a row. A typed result's driver names its columns'
// types, as lib/pq or go-sqlite3 do; a bare one's names none, as a driver that
// implements no ColumnType interface.
type fakeCol struct {
	name, dbType string
	scan         reflect.Type
	notNull      bool
	prec, scale  int64
	decimal      bool
}

type fakeResult struct {
	cols  []fakeCol
	rows  [][]driver.Value
	typed bool
	err   error // Next's, after the rows
}

var (
	fakeOnce    sync.Once
	fakeMu      sync.Mutex
	fakeResults = map[string]fakeResult{}
)

func fakeDB(t *testing.T) *sql.DB {
	fakeOnce.Do(func() { sql.Register("ursus-fake", fakeDriver{}) })
	db, err := sql.Open("ursus-fake", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// fake registers r as query's result.
func fake(query string, r fakeResult) string {
	fakeMu.Lock()
	defer fakeMu.Unlock()
	fakeResults[query] = r
	return query
}

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) { return fakeConn{}, nil }

type fakeConn struct{}

func (fakeConn) Prepare(q string) (driver.Stmt, error) { return fakeStmt(q), nil }
func (fakeConn) Close() error                          { return nil }
func (fakeConn) Begin() (driver.Tx, error)             { return fakeTx{}, nil }

type fakeTx struct{}

func (fakeTx) Commit() error   { return nil }
func (fakeTx) Rollback() error { return nil }

type fakeStmt string

func (fakeStmt) Close() error                               { return nil }
func (fakeStmt) NumInput() int                              { return -1 }
func (fakeStmt) Exec([]driver.Value) (driver.Result, error) { return nil, errors.New("fake: no exec") }

func (s fakeStmt) Query(args []driver.Value) (driver.Rows, error) {
	if s == "args" {
		cols := make([]fakeCol, len(args))
		for i := range args {
			cols[i] = fakeCol{name: fmt.Sprint("a", i)}
		}
		return &bareRows{res: fakeResult{cols: cols, rows: [][]driver.Value{args}}}, nil
	}
	fakeMu.Lock()
	r, ok := fakeResults[string(s)]
	fakeMu.Unlock()
	if !ok {
		return nil, fmt.Errorf("fake: no result for %q", string(s))
	}
	if r.typed {
		return &typedRows{bareRows{res: r}}, nil
	}
	return &bareRows{res: r}, nil
}

type bareRows struct {
	res fakeResult
	at  int
}

func (r *bareRows) Columns() []string {
	names := make([]string, len(r.res.cols))
	for i, c := range r.res.cols {
		names[i] = c.name
	}
	return names
}

func (r *bareRows) Close() error { return nil }

func (r *bareRows) Next(dest []driver.Value) error {
	if r.at == len(r.res.rows) {
		if r.res.err != nil {
			return r.res.err
		}
		return io.EOF
	}
	copy(dest, r.res.rows[r.at])
	r.at++
	return nil
}

type typedRows struct{ bareRows }

func (r *typedRows) ColumnTypeScanType(i int) reflect.Type {
	if st := r.res.cols[i].scan; st != nil {
		return st
	}
	return reflect.TypeFor[any]()
}

func (r *typedRows) ColumnTypeDatabaseTypeName(i int) string { return r.res.cols[i].dbType }

func (r *typedRows) ColumnTypeNullable(i int) (nullable, ok bool) {
	return !r.res.cols[i].notNull, true
}

func (r *typedRows) ColumnTypePrecisionScale(i int) (precision, scale int64, ok bool) {
	c := r.res.cols[i]
	return c.prec, c.scale, c.decimal
}

// A driver's own types: text, a value, and a struct fmt writes.
type myText string

type myValuer struct{ n int64 }

func (v myValuer) Value() (driver.Value, error) { return v.n, nil }

type myStruct struct{ a, b int }

// column is a result of one column, x, of these values.
func column(c fakeCol, vals ...driver.Value) fakeResult {
	c.name = "x"
	rows := make([][]driver.Value, len(vals))
	for i, v := range vals {
		rows[i] = []driver.Value{v}
	}
	return fakeResult{cols: []fakeCol{c}, rows: rows, typed: c != fakeCol{name: "x"}}
}

func dbCells(t *testing.T, db *sql.DB, query string) string {
	t.Helper()
	df, err := ursus.ReadDatabase(t.Context(), db, query)
	if err != nil {
		t.Fatal(err)
	}
	return cells(t, df.Lazy(), "x")
}

// TestReadDatabase is step 353: Polars' read_database, through database/sql. A
// column's type is its values', met as Polars meets them; every answer of a bare
// driver's is Polars 1.44.1's reading SQLite's values through sqlite3, but where noted.
func TestReadDatabase(t *testing.T) {
	db := fakeDB(t)
	utc := func(y int, m time.Month, d, h, mi, s, us int) time.Time {
		return time.Date(y, m, d, h, mi, s, us*1000, time.UTC)
	}
	plus5 := time.FixedZone("", 5*3600)
	bare := fakeCol{}
	for _, tc := range []struct {
		name string
		res  fakeResult
		want string
	}{
		{"integers", column(bare, int64(1), nil), "1 <nil> Int64"},
		{"floats", column(bare, 1.5, nil), "1.5 <nil> Float64"},
		{"text", column(bare, "a", nil), "a <nil> String"},
		{"bytes", column(bare, []byte{1, 2}, nil), "[1 2] <nil> Binary"},
		{"Booleans", column(bare, true, false), "true false Bool"},
		{"an integer and a float", column(bare, int64(1), 2.5), "1 2.5 Float64"},
		{"a large integer and a float", column(bare, int64(1<<53+1), 2.5), "9.007199254740992e+15 2.5 Float64"},
		{"an integer and text", column(bare, int64(1), "a"), "1 a String"},
		{"a float and text", column(bare, 2.5, "a"), "2.5 a String"},
		{"bytes and text", column(bare, []byte{1}, "a"), "[1] [97] Binary"},
		{"no value", column(bare, nil, nil), "<nil> <nil> Null"},
		{"a null, then an integer", column(bare, nil, int64(1)), "<nil> 1 Int64"},
		// Polars, the values built from Python's: [True, 2] is Int64, [True, "a"] text.
		{"a Boolean and an integer", column(bare, true, int64(2)), "1 2 Int64"},
		{"a Boolean and text", column(bare, true, "a"), "true a String"},
		// Polars: a naive datetime is a Datetime(us), an aware one the instant in UTC.
		{"an instant in UTC", column(bare, utc(2020, 1, 2, 3, 4, 5, 6)), "2020-01-02 03:04:05.000006 +0000 UTC Datetime(us)"},
		{"an instant in a zone", column(bare, time.Date(2020, 1, 1, 0, 0, 0, 0, plus5)), "2019-12-31 19:00:00 +0000 UTC Datetime(us, UTC)"},
		// Polars refuses a datetime among text, "could not append value"; it meets at
		// text by Polars' supertype here, the instant's text ursus's cast's, which a
		// naive one ends with a Z (feature-gaps §1 and §9).
		{"an instant and text", column(bare, utc(2020, 1, 2, 3, 4, 5, 6), "a"), "2020-01-02T03:04:05.000006Z a String"},
		// Polars: [1, 2**63] is an Int128.
		{"past an Int64", column(bare, uint64(math.MaxUint64)), "18446744073709551615 Uint64"},
		{"an Int64 and past one", column(bare, int64(1), uint64(1<<63)), "1 9223372036854775808 Int128"},
		{"a driver's own text", column(bare, myText("x")), "x String"},
		{"a driver's own value", column(bare, myValuer{7}), "7 Int64"},
		{"a driver's own struct", column(bare, myStruct{1, 2}), "{1 2} String"},
		{"an int32 and a float32", column(bare, int32(3), float32(1.5)), "3 1.5 Float64"},
		{"text not UTF-8", column(bare, "\xff"), "[255] Binary"},
	} {
		q := fake("bare "+tc.name, tc.res)
		if got := dbCells(t, db, q); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}

	// Every row's value types the column, where Polars reads 100: 150 integers and a
	// float are Float64, as Polars' infer_schema_length=None reads them; by default it
	// reads 1 for the float.
	vals := make([]driver.Value, 151)
	for i := range 150 {
		vals[i] = int64(i)
	}
	vals[150] = 1.5
	df, err := ursus.ReadDatabase(t.Context(), db, fake("a float after 150 integers", column(bare, vals...)))
	if err != nil {
		t.Fatal(err)
	}
	if got := cells(t, df.Lazy().Tail(1), "x"); got != "1.5 Float64" {
		t.Errorf("a float after 150 integers: %s", got)
	}

	// Its arguments, as Polars' execute_options' parameters.
	df, err = ursus.ReadDatabase(t.Context(), db, "args", 5, "p")
	if err != nil {
		t.Fatal(err)
	}
	if got := cells(t, df.Lazy(), "a0") + " " + cells(t, df.Lazy(), "a1"); got != "5 Int64 p String" {
		t.Errorf("arguments: %s", got)
	}
}

// TestReadDatabaseDriverTypes is a column of the type its driver names, where every
// value fits it.
func TestReadDatabaseDriverTypes(t *testing.T) {
	db := fakeDB(t)
	scan := func(st reflect.Type, name string) fakeCol { return fakeCol{scan: st, dbType: name} }
	numeric := func(p, s int64) fakeCol {
		return fakeCol{dbType: "NUMERIC", prec: p, scale: s, decimal: true}
	}
	named := func(name string) fakeCol { return fakeCol{dbType: name} }
	day := time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		res  fakeResult
		want string
	}{
		{"an INT4", column(scan(reflect.TypeFor[int32](), "INT4"), int64(1), int64(-2)), "1 -2 Int32"},
		{"an INT4 past its width", column(scan(reflect.TypeFor[int32](), "INT4"), int64(1<<40)), "1099511627776 Int64"},
		{"a FLOAT4", column(scan(reflect.TypeFor[float32](), "FLOAT4"), 1.5), "1.5 Float32"},
		{"a BOOLEAN of integers", column(scan(reflect.TypeFor[sql.NullBool](), "BOOLEAN"), int64(1), int64(0)), "true false Bool"},
		{"a NUMERIC(10, 2)", column(numeric(10, 2), []byte("1.25"), []byte("-3"), nil), "1.25 -3.00 <nil> Decimal(10, 2)"},
		// Polars reads Python Decimals of no stated size as Decimal(38, their scale).
		{"a NUMERIC of no size", column(named("NUMERIC"), []byte("1.5"), []byte("2.25")), "1.50 2.25 Decimal(38, 2)"},
		{"a NUMERIC's NaN", column(named("NUMERIC"), []byte("1.5"), []byte("NaN")), "1.5 NaN String"},
		// A DATE is each instant's own date, in its zone.
		{"a DATE", column(named("DATE"), day, time.Date(2020, 1, 3, 0, 0, 0, 0, time.FixedZone("", 5*3600))), "2020-01-02 00:00:00 +0000 UTC 2020-01-03 00:00:00 +0000 UTC Date"},
		{"a DATE as text", column(named("DATE"), []byte("2020-01-02")), "2020-01-02 00:00:00 +0000 UTC Date"},
		// Oracle's DATE holds a time of day too, which a Date would lose.
		{"a DATE with a time of day", column(named("DATE"), time.Date(2020, 1, 2, 3, 0, 0, 0, time.UTC)), "2020-01-02 03:00:00 +0000 UTC Datetime(us)"},
		{"a TIME", column(named("TIME"), time.Date(0, 1, 1, 10, 30, 0, 0, time.UTC)), "10h30m0s Time(ns)"},
		{"a TIME as text", column(named("TIME"), "10:30:00.5"), "10h30m0.5s Time(ns)"},
		{"a TIMESTAMPTZ in UTC", column(named("TIMESTAMPTZ"), day), "2020-01-02 00:00:00 +0000 UTC Datetime(us, UTC)"},
		{"a DATETIME as text", column(named("DATETIME"), []byte("2020-01-02 03:04:05")), "2020-01-02 03:04:05 +0000 UTC Datetime(us)"},
		{"a VARCHAR's bytes", column(named("VARCHAR"), []byte("ab")), "ab String"},
		{"a UUID's bytes", column(named("UUID"), []byte("a0eebc99")), "a0eebc99 String"},
		{"a BLOB", column(named("BLOB"), []byte("ab")), "[97 98] Binary"},
		{"an INTERVAL's bytes", column(named("INTERVAL"), []byte("1 day")), "1 day String"},
		// SQLite's NUMERIC is a float to go-sqlite3, and holds integers as integers,
		// which Polars reads as Int64; with a float among them, Float64.
		{"SQLite's NUMERIC of integers", column(scan(reflect.TypeFor[sql.NullFloat64](), "NUMERIC"), int64(3), int64(4)), "3 4 Int64"},
		{"SQLite's NUMERIC", column(scan(reflect.TypeFor[sql.NullFloat64](), "NUMERIC"), int64(3), 4.5), "3 4.5 Float64"},
		{"no value, of an INT4", column(scan(reflect.TypeFor[int32](), "INT4"), nil), "<nil> Int32"},
		{"no value, of a TIMESTAMP", column(named("TIMESTAMP"), nil), "<nil> Datetime(us)"},
	} {
		q := fake("typed "+tc.name, tc.res)
		if got := dbCells(t, db, q); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}

	// No row: each column of its driver's type, or a Null; NOT NULL, not nullable.
	q := fake("no row", fakeResult{typed: true, cols: []fakeCol{
		{name: "i", scan: reflect.TypeFor[int32](), dbType: "INT4", notNull: true},
		{name: "s", dbType: "TEXT"},
		{name: "u"},
	}})
	df, err := ursus.ReadDatabase(t.Context(), db, q)
	if err != nil {
		t.Fatal(err)
	}
	if got := df.Schema().String(); got != "{i: Int32!, s: String, u: Null}" {
		t.Errorf("no row: %s", got)
	}

	// NOT NULL where the values agree.
	q = fake("not null", fakeResult{typed: true, rows: [][]driver.Value{{int64(1), nil}}, cols: []fakeCol{
		{name: "i", dbType: "INT8", notNull: true},
		{name: "j", dbType: "INT8", notNull: true},
	}})
	if df, err = ursus.ReadDatabase(t.Context(), db, q); err != nil {
		t.Fatal(err)
	}
	if got := df.Schema().String(); got != "{i: Int64!, j: Null}" {
		t.Errorf("NOT NULL: %s", got)
	}
}

// TestReadDatabaseOverrides is WithDatabaseSchemaOverrides, Polars' schema_overrides.
func TestReadDatabaseOverrides(t *testing.T) {
	ctx := t.Context()
	db := fakeDB(t)
	read := func(q string, types map[string]ursus.DataType) (*ursus.DataFrame, error) {
		rows, err := db.QueryContext(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		return ursus.ReadDatabaseRows(rows, ursus.WithDatabaseSchemaOverrides(types))
	}
	bare := fakeCol{}
	for _, tc := range []struct {
		name string
		res  fakeResult
		to   ursus.DataType
		want string
	}{
		{"an Int32", column(bare, int64(1)), ursus.Int32, "1 Int32"},
		{"text of an integer and text", column(bare, int64(1), "a"), ursus.String, "1 a String"},
		{"a float as a Decimal", column(bare, 1.25), ursus.Decimal(10, 2), "1.25 Decimal(10, 2)"},
		// Polars refuses the first and nulls the second, its builder parsing no text;
		// the cast parses both here.
		{"text as a Date", column(bare, "2020-01-02"), ursus.Date, "2020-01-02 00:00:00 +0000 UTC Date"},
		{"text as a Decimal", column(bare, "1.25"), ursus.Decimal(10, 2), "1.25 Decimal(10, 2)"},
		// Bytes do not meet an integer, but each casts to text.
		{"bytes and an integer as text", column(bare, []byte("b"), int64(1)), ursus.String, "b 1 String"},
	} {
		df, err := read(fake("override "+tc.name, tc.res), map[string]ursus.DataType{"x": tc.to})
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got := cells(t, df.Lazy(), "x"); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
	for _, tc := range []struct {
		name string
		res  fakeResult
		to   ursus.DataType
	}{
		// Polars: "could not append value: 3000000000 of type: i64".
		{"past an Int32", column(bare, int64(3e9)), ursus.Int32},
		// Polars truncates 1.5 to 1, as its cast does; ursus's casts are exact or
		// refused (feature-gaps §9).
		{"a float as an integer", column(bare, 1.5), ursus.Int64},
		// Polars: "could not append value: b"\x01" of type: binary".
		{"bytes not UTF-8 as text", column(bare, []byte{0xff}), ursus.String},
	} {
		if _, err := read(fake("override "+tc.name, tc.res), map[string]ursus.DataType{"x": tc.to}); !errors.Is(err, ursus.ErrValue) {
			t.Errorf("%s: %v, want a value error", tc.name, err)
		}
	}
	// Polars ignores a name the result has none of; refused here, as by NDJSON.
	if _, err := read(fake("override of none", column(bare, int64(1))), map[string]ursus.DataType{"y": ursus.Int32}); !errors.Is(err, ursus.ErrSchema) {
		t.Errorf("an override of no column: %v", err)
	}
}

// TestReadDatabaseRefusals is what ReadDatabase refuses, and where it reads from.
func TestReadDatabaseRefusals(t *testing.T) {
	ctx := t.Context()
	db := fakeDB(t)
	bare := fakeCol{}
	broken := errors.New("the connection broke")
	for _, tc := range []struct {
		name  string
		query string
		is    error
		says  string
	}{
		// Polars: "failed to determine supertype of binary and i64".
		{"bytes and an integer", fake("bytes and an integer", column(bare, []byte{1}, int64(1))), ursus.ErrType, "Binary"},
		{"bytes, text and an integer", fake("bytes, text and an integer", column(bare, []byte{1}, "a", int64(1))), ursus.ErrType, "Int64"},
		{"an instant and a Boolean", fake("an instant and a Boolean", column(bare, time.Now(), true)), ursus.ErrType, "Bool"},
		// Polars: "column 'a' appears more than once in the query/result cursor".
		{"a name twice", fake("a name twice", fakeResult{cols: []fakeCol{{name: "a"}, {name: "a"}}}), ursus.ErrSchema, "more than once"},
		{"a broken result", fake("a broken result", fakeResult{cols: []fakeCol{{name: "x"}}, rows: [][]driver.Value{{int64(1)}}, err: broken}), broken, "reading the result"},
		{"no such query", "no such query", ursus.ErrIO, "running the query"},
		{"an instant past a Datetime", fake("an instant past a Datetime", column(bare, time.Date(300000, 1, 1, 0, 0, 0, 0, time.UTC))), ursus.ErrValue, "row 1"},
	} {
		_, err := ursus.ReadDatabase(ctx, db, tc.query)
		if !errors.Is(err, tc.is) || !strings.Contains(fmt.Sprint(err), tc.says) {
			t.Errorf("%s: %v, want %v saying %q", tc.name, err, tc.is, tc.says)
		}
	}
	if _, err := ursus.ReadDatabase(ctx, nil, "x"); !errors.Is(err, ursus.ErrValue) {
		t.Errorf("no database: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := ursus.ReadDatabase(cancelled, db, fake("cancelled", column(bare, int64(1)))); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled context: %v", err)
	}

	// A transaction, a connection and a statement's rows are read alike.
	q := fake("one", column(bare, int64(1)))
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	stmt, err := db.PrepareContext(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	rows, err := stmt.QueryContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for name, read := range map[string]func() (*ursus.DataFrame, error){
		"a transaction": func() (*ursus.DataFrame, error) { return ursus.ReadDatabase(ctx, tx, q) },
		"a connection":  func() (*ursus.DataFrame, error) { return ursus.ReadDatabase(ctx, conn, q) },
		"a statement":   func() (*ursus.DataFrame, error) { return ursus.ReadDatabaseRows(rows) },
	} {
		df, err := read()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := cells(t, df.Lazy(), "x"); got != "1 Int64" {
			t.Errorf("%s: %s", name, got)
		}
	}
}

// TestReadDatabaseManyRows is a column whose kinds change far into it, and one of
// text past a batch's rows.
func TestReadDatabaseManyRows(t *testing.T) {
	db := fakeDB(t)
	const n = 10_000
	vals := make([]driver.Value, n)
	for i := range n {
		switch {
		case i%7 == 3:
		case i < n/2:
			vals[i] = int64(i)
		default:
			vals[i] = fmt.Sprint("t", i)
		}
	}
	df, err := ursus.ReadDatabase(t.Context(), db, fake("many rows", column(fakeCol{}, vals...)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := df.Lazy().Select(ursus.Col("x")).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	s, err := got.Column[string]("x")
	if err != nil {
		t.Fatal(err)
	}
	for i := range n {
		v, ok := s.Get(i)
		want := fmt.Sprint(vals[i])
		if ok != (vals[i] != nil) || (ok && v != want) {
			t.Fatalf("row %d: %q %v, want %v", i, v, ok, vals[i])
		}
	}
}
