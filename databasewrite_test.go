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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/advenn/ursus"
)

// A recording database/sql driver for WriteDatabase (step 366): it keeps each table's
// statement and rows, logs every statement, answers the dialects' existence queries
// from its tables, and undoes a rolled-back transaction. Each test opens a database
// of its own.
type wtable struct {
	ddl  string
	cols []string
	rows [][]driver.Value
}

type wdb struct {
	mu         sync.Mutex
	tables     map[string]*wtable
	snap       map[string]*wtable
	log        []string
	inserts    []string
	failInsert int // the nth INSERT fails, from 1; 0 none
}

var (
	wOnce sync.Once
	wMu   sync.Mutex
	wDBs  = map[string]*wdb{}
	wSeq  int
)

func writeDB(t *testing.T) (*sql.DB, *wdb) {
	wOnce.Do(func() { sql.Register("ursus-fakewrite", wDriver{}) })
	wMu.Lock()
	wSeq++
	dsn := fmt.Sprint(t.Name(), wSeq)
	st := &wdb{tables: map[string]*wtable{}}
	wDBs[dsn] = st
	wMu.Unlock()
	db, err := sql.Open("ursus-fakewrite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db, st
}

type wDriver struct{}

func (wDriver) Open(dsn string) (driver.Conn, error) {
	wMu.Lock()
	defer wMu.Unlock()
	return &wConn{db: wDBs[dsn]}, nil
}

type wConn struct{ db *wdb }

func (c *wConn) Prepare(q string) (driver.Stmt, error) { return &wStmt{c.db, q}, nil }
func (c *wConn) Close() error                          { return nil }
func (c *wConn) Begin() (driver.Tx, error) {
	d := c.db
	d.mu.Lock()
	defer d.mu.Unlock()
	d.log = append(d.log, "BEGIN")
	d.snap = map[string]*wtable{}
	for k, v := range d.tables {
		cp := *v
		cp.rows = slices.Clone(v.rows)
		d.snap[k] = &cp
	}
	return wTx{d}, nil
}

type wTx struct{ db *wdb }

func (t wTx) Commit() error {
	t.db.mu.Lock()
	defer t.db.mu.Unlock()
	t.db.log, t.db.snap = append(t.db.log, "COMMIT"), nil
	return nil
}

func (t wTx) Rollback() error {
	t.db.mu.Lock()
	defer t.db.mu.Unlock()
	t.db.log = append(t.db.log, "ROLLBACK")
	if t.db.snap != nil {
		t.db.tables, t.db.snap = t.db.snap, nil
	}
	return nil
}

type wStmt struct {
	db *wdb
	q  string
}

func (s *wStmt) Close() error  { return nil }
func (s *wStmt) NumInput() int { return -1 }

// bare is a quoted name's text: its last dotted part, unquoted.
func bare(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	switch {
	case strings.HasPrefix(name, `"`):
		return strings.ReplaceAll(name[1:len(name)-1], `""`, `"`)
	case strings.HasPrefix(name, "`"):
		return strings.ReplaceAll(name[1:len(name)-1], "``", "`")
	case strings.HasPrefix(name, "["):
		return strings.ReplaceAll(name[1:len(name)-1], "]]", "]")
	}
	return name
}

func (s *wStmt) Exec(args []driver.Value) (driver.Result, error) {
	d := s.db
	d.mu.Lock()
	defer d.mu.Unlock()
	q := s.q
	switch {
	case strings.HasPrefix(q, "DROP TABLE IF EXISTS "):
		d.log = append(d.log, q)
		delete(d.tables, bare(strings.TrimPrefix(q, "DROP TABLE IF EXISTS ")))
	case strings.HasPrefix(q, "CREATE TABLE "):
		d.log = append(d.log, q)
		name := bare(q[len("CREATE TABLE "):strings.Index(q, " (")])
		if d.tables[name] != nil {
			return nil, fmt.Errorf("fake: table %q exists", name)
		}
		d.tables[name] = &wtable{ddl: q}
	case strings.HasPrefix(q, "INSERT INTO "):
		d.inserts = append(d.inserts, q)
		if d.failInsert > 0 && len(d.inserts) == d.failInsert {
			return nil, errors.New("fake: the disk is full")
		}
		open := strings.Index(q, " (")
		name := bare(q[len("INSERT INTO "):open])
		tab := d.tables[name]
		if tab == nil {
			return nil, fmt.Errorf("fake: no table %q", name)
		}
		var cols []string
		for _, c := range strings.Split(q[open+2:strings.Index(q, ") VALUES ")], ", ") {
			cols = append(cols, bare(c))
		}
		if tab.cols == nil {
			tab.cols = cols
		}
		if len(args)%len(cols) != 0 {
			return nil, fmt.Errorf("fake: %d arguments for %d columns", len(args), len(cols))
		}
		for lo := 0; lo < len(args); lo += len(cols) {
			tab.rows = append(tab.rows, slices.Clone(args[lo:lo+len(cols)]))
		}
		d.log = append(d.log, fmt.Sprint("INSERT ", len(args)/len(cols)))
	default:
		return nil, fmt.Errorf("fake: no exec of %q", q)
	}
	return driver.RowsAffected(0), nil
}

func (s *wStmt) Query(args []driver.Value) (driver.Rows, error) {
	d := s.db
	d.mu.Lock()
	defer d.mu.Unlock()
	d.log = append(d.log, s.q)
	if len(args) == 0 {
		return nil, fmt.Errorf("fake: no query %q", s.q)
	}
	_, there := d.tables[bare(args[len(args)-1].(string))]
	var v driver.Value = int64(0)
	if there {
		v = int64(1)
	}
	if strings.Contains(s.q, "to_regclass") {
		v = there
	}
	return &oneRow{v: v}, nil
}

type oneRow struct {
	v    driver.Value
	done bool
}

func (r *oneRow) Columns() []string { return []string{"v"} }
func (r *oneRow) Close() error      { return nil }
func (r *oneRow) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	dest[0], r.done = r.v, true
	return nil
}

// writeFrame is a frame of every type a table takes, n rows, nulls in its text.
func writeFrame(t *testing.T, n int) *ursus.DataFrame {
	t.Helper()
	b := make([]bool, n)
	i8 := make([]int8, n)
	i64 := make([]int64, n)
	u64 := make([]uint64, n)
	f32 := make([]float32, n)
	f64 := make([]float64, n)
	s := make([]string, n)
	ok := make([]bool, n)
	ts := make([]time.Time, n)
	for i := range n {
		b[i], i8[i], i64[i] = i%3 == 0, int8(i-60), int64(i)*1_000_000_007-5
		u64[i], f32[i], f64[i] = uint64(i)*3, float32(i)/4, float64(i)/8-3
		s[i], ok[i] = fmt.Sprint("s", i, `"'`), i%5 != 2
		ts[i] = time.Date(2024, 1, 2, 3, 4, 5, 123456000, time.UTC).Add(time.Duration(i) * 97 * time.Minute)
	}
	c := ursus.Col
	df, err := ursus.Frame(
		ursus.Values("b", b), ursus.Values("i8", i8), ursus.Values("i64", i64), ursus.Values("u64", u64),
		ursus.Values("f32", f32), ursus.Values("f64", f64), ursus.ValuesNullable("s", s, ok), ursus.Values("ts", ts),
	).WithColumns(
		c("s").Cast(ursus.Binary).Alias("bin"),
		c("ts").Dt().Date().Alias("d"),
		c("ts").Dt().Time().Alias("tm"),
		c("ts").Dt().ConvertTimeZone("Asia/Tokyo").Alias("tz"),
		c("ts").Dt().ReplaceTimeZone("").Alias("ts"),
		c("f64").Round(2).Cast(ursus.Decimal(10, 2)).Alias("dec"),
		c("s").Cast(ursus.Categorical).Alias("cat"),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return df
}

// driverValues is ToMaps' rows as the default parameter conversion stores them, a
// time of day as its text, and for SQLite a date or an instant as SQLite's text.
func driverValues(t *testing.T, df *ursus.DataFrame, sqlite bool) [][]driver.Value {
	t.Helper()
	maps, err := df.ToMaps()
	if err != nil {
		t.Fatal(err)
	}
	names := df.Columns()
	schema := df.Schema()
	var out [][]driver.Value
	for _, m := range maps {
		row := make([]driver.Value, len(names))
		for j, name := range names {
			switch v := m[name].(type) {
			case int8:
				row[j] = int64(v)
			case uint64:
				row[j] = int64(v)
			case float32:
				row[j] = float64(v)
			case time.Duration:
				row[j] = time.Time{}.Add(v).Format("15:04:05.999999999")
			case time.Time:
				row[j] = v
				if sqlite {
					switch typ := schema.Field(j).Type; {
					case typ == ursus.Date:
						row[j] = v.Format("2006-01-02")
					case typ.TimeZone() == "":
						row[j] = v.Format("2006-01-02 15:04:05.999999999")
					default:
						row[j] = v.Format("2006-01-02 15:04:05.999999999-07:00")
					}
				}
			default:
				row[j] = v
			}
		}
		out = append(out, row)
	}
	return out
}

func sameWritten(t *testing.T, got, want [][]driver.Value) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d rows written, want %d", len(got), len(want))
	}
	for i := range want {
		for j := range want[i] {
			g, w := got[i][j], want[i][j]
			if gt, ok := g.(time.Time); ok {
				if wt, ok := w.(time.Time); !ok || !gt.Equal(wt) {
					t.Fatalf("row %d, column %d: %v, want %v", i, j, g, w)
				}
				continue
			}
			if !reflect.DeepEqual(g, w) {
				t.Fatalf("row %d, column %d: %#v, want %#v", i, j, g, w)
			}
		}
	}
}

// TestWriteDatabaseWritesEveryType is step 366's: a frame of every type a table takes,
// written to a table it creates, in INSERTs of the dialect's parameters at most, in
// one transaction, each value as ToMaps gives it, a null as nil: to SQLite, in INSERTs
// of 999 parameters at most, a date and an instant as its text, and to PostgreSQL.
func TestWriteDatabaseWritesEveryType(t *testing.T) {
	df := writeFrame(t, 150)
	for _, tc := range []struct {
		d       ursus.SQLDialect
		create  string
		inserts []string
		exists  string
	}{
		{ursus.DialectSQLite,
			`CREATE TABLE "t" ("b" BOOLEAN, "i8" INTEGER, "i64" INTEGER, "u64" INTEGER, "f32" REAL, ` +
				`"f64" REAL, "s" TEXT, "ts" TIMESTAMP, "bin" BLOB, "d" DATE, "tm" TIME, "tz" TIMESTAMP, ` +
				`"dec" DECIMAL(10, 2), "cat" TEXT)`,
			// 14 columns: 71 rows to an INSERT of 994 parameters, twice, then the rest.
			[]string{"INSERT 71", "INSERT 71", "INSERT 8"},
			`SELECT COUNT(*) FROM sqlite_master WHERE type IN ('table', 'view') AND name = ?`},
		{ursus.DialectPostgres,
			`CREATE TABLE "t" ("b" BOOLEAN, "i8" SMALLINT, "i64" BIGINT, "u64" NUMERIC(20, 0), "f32" REAL, ` +
				`"f64" DOUBLE PRECISION, "s" TEXT, "ts" TIMESTAMP, "bin" BYTEA, "d" DATE, "tm" TIME, ` +
				`"tz" TIMESTAMPTZ, "dec" DECIMAL(10, 2), "cat" TEXT)`,
			[]string{"INSERT 150"},
			`SELECT to_regclass($1) IS NOT NULL`},
	} {
		db, st := writeDB(t)
		n, err := df.WriteDatabase(t.Context(), db, "t", ursus.WithDialect(tc.d))
		if err != nil {
			t.Fatal(err)
		}
		if n != 150 {
			t.Errorf("dialect %d: wrote %d rows, want 150", tc.d, n)
		}
		if got := st.tables["t"].ddl; got != tc.create {
			t.Errorf("dialect %d: created\n %s\nwant\n %s", tc.d, got, tc.create)
		}
		wantLog := append(append([]string{"BEGIN", tc.exists, tc.create}, tc.inserts...), "COMMIT")
		if !slices.Equal(st.log, wantLog) {
			t.Errorf("dialect %d: statements\n %q\nwant\n %q", tc.d, st.log, wantLog)
		}
		sameWritten(t, st.tables["t"].rows, driverValues(t, df, tc.d == ursus.DialectSQLite))
		if got := strings.Count(st.inserts[0], "?"); tc.d == ursus.DialectSQLite && got != 994 {
			t.Errorf("the first INSERT has %d placeholders, want 994", got)
		}
	}
}

// TestWriteDatabaseModes: Polars' if_table_exists, as pandas' to_sql answers it: fail
// refuses a table there and writes nothing, and creates one that is not; replace drops
// and creates; append inserts, creating a table that is not there.
func TestWriteDatabaseModes(t *testing.T) {
	ctx := t.Context()
	first := ursus.Frame(ursus.Values("a", []int64{1, 2}))
	second := ursus.Frame(ursus.Values("a", []int64{3}))
	sqlite := ursus.WithDialect(ursus.DialectSQLite)
	rowsOf := func(st *wdb) []int64 {
		var out []int64
		for _, r := range st.tables["t"].rows {
			out = append(out, r[0].(int64))
		}
		return out
	}
	t.Run("fail", func(t *testing.T) {
		db, st := writeDB(t)
		if _, err := first.WriteDatabase(ctx, db, "t", sqlite); err != nil {
			t.Fatal(err)
		}
		before := len(st.log)
		_, err := second.WriteDatabase(ctx, db, "t", sqlite)
		if err == nil || !strings.Contains(err.Error(), `table "t" already exists`) {
			t.Fatalf("a second write answered %v", err)
		}
		if got := st.log[before:]; slices.ContainsFunc(got, func(s string) bool {
			return strings.HasPrefix(s, "CREATE") || strings.HasPrefix(s, "INSERT")
		}) || got[len(got)-1] != "ROLLBACK" {
			t.Errorf("a refused write ran %q", got)
		}
		if got := rowsOf(st); !slices.Equal(got, []int64{1, 2}) {
			t.Errorf("rows %v", got)
		}
	})
	t.Run("replace", func(t *testing.T) {
		db, st := writeDB(t)
		for _, lf := range []*ursus.LazyFrame{first, second} {
			if _, err := lf.WriteDatabase(ctx, db, "t", sqlite, ursus.WithIfTableExists(ursus.IfExistsReplace)); err != nil {
				t.Fatal(err)
			}
		}
		if got := rowsOf(st); !slices.Equal(got, []int64{3}) {
			t.Errorf("rows %v", got)
		}
		if !slices.Contains(st.log, `DROP TABLE IF EXISTS "t"`) {
			t.Errorf("no drop in %q", st.log)
		}
	})
	t.Run("append", func(t *testing.T) {
		db, st := writeDB(t)
		for _, lf := range []*ursus.LazyFrame{first, second} {
			if _, err := lf.WriteDatabase(ctx, db, "t", sqlite, ursus.WithIfTableExists(ursus.IfExistsAppend)); err != nil {
				t.Fatal(err)
			}
		}
		if got := rowsOf(st); !slices.Equal(got, []int64{1, 2, 3}) {
			t.Errorf("rows %v", got)
		}
		creates := 0
		for _, s := range st.log {
			if strings.HasPrefix(s, "CREATE") {
				creates++
			}
		}
		if creates != 1 {
			t.Errorf("%d tables created, want 1", creates)
		}
	})
}

// TestWriteDatabaseDialects: each dialect's quoting, placeholders, column types and
// question whether the table is there, and its INSERT's size.
func TestWriteDatabaseDialects(t *testing.T) {
	df := ursus.Frame(ursus.Values("a", []int64{1, 2}), ursus.ValuesNullable("s", []string{"x", ""}, []bool{true, false}))
	for _, tc := range []struct {
		d                     ursus.SQLDialect
		exists, create, input string
	}{
		{ursus.DialectPostgres, `SELECT to_regclass($1) IS NOT NULL`,
			`CREATE TABLE "s"."t" ("a" BIGINT, "s" TEXT)`, `INSERT INTO "s"."t" ("a", "s") VALUES ($1, $2), ($3, $4)`},
		{ursus.DialectMySQL, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = ? AND table_name = ?`,
			"CREATE TABLE `s`.`t` (`a` BIGINT, `s` LONGTEXT)", "INSERT INTO `s`.`t` (`a`, `s`) VALUES (?, ?), (?, ?)"},
		{ursus.DialectSQLServer, `SELECT CASE WHEN OBJECT_ID(@p1, N'U') IS NULL THEN 0 ELSE 1 END`,
			`CREATE TABLE [s].[t] ([a] BIGINT, [s] NVARCHAR(MAX))`, `INSERT INTO [s].[t] ([a], [s]) VALUES (@p1, @p2), (@p3, @p4)`},
		{ursus.DialectSQLite, `SELECT COUNT(*) FROM "s".sqlite_master WHERE type IN ('table', 'view') AND name = ?`,
			`CREATE TABLE "s"."t" ("a" INTEGER, "s" TEXT)`, `INSERT INTO "s"."t" ("a", "s") VALUES (?, ?), (?, ?)`},
	} {
		db, st := writeDB(t)
		n, err := df.WriteDatabase(t.Context(), db, "s.t", ursus.WithDialect(tc.d))
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"BEGIN", tc.exists, tc.create, "INSERT 2", "COMMIT"}
		if n != 2 || !slices.Equal(st.log, want) {
			t.Errorf("dialect %d: %d rows,\n %q\nwant\n %q", tc.d, n, st.log, want)
		}
		if got := st.inserts[len(st.inserts)-1]; got != tc.input {
			t.Errorf("dialect %d: %s, want %s", tc.d, got, tc.input)
		}
		if got := st.tables["t"].rows[1][1]; got != nil {
			t.Errorf("dialect %d: a null written as %#v", tc.d, got)
		}
	}
}

// TestWriteDatabaseRefuses: a type no table takes, a frame of no columns, a driver
// whose dialect is unknown, are refused before any statement; an unsigned integer past
// 2^63-1 is refused by SQLite and written as text for PostgreSQL.
func TestWriteDatabaseRefuses(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	sqlite := ursus.WithDialect(ursus.DialectSQLite)
	for name, lf := range map[string]*ursus.LazyFrame{
		"a Duration": ursus.Frame(ursus.Values("a", []int64{1})).Select(c("a").Cast(ursus.Duration(ursus.Micro))),
		"a List":     ursus.Frame(ursus.Values("a", []int64{1})).Select(c("a").Implode()),
		"no columns": ursus.Frame(ursus.Values("a", []int64{1})).Select(),
	} {
		db, st := writeDB(t)
		if _, err := lf.WriteDatabase(ctx, db, "t", sqlite); err == nil {
			t.Errorf("%s was written", name)
		}
		if len(st.log) > 0 {
			t.Errorf("%s ran %q", name, st.log)
		}
	}
	db, st := writeDB(t)
	_, err := ursus.Frame(ursus.Values("a", []int64{1})).WriteDatabase(ctx, db, "t")
	if err == nil || !strings.Contains(err.Error(), "WithDialect") || len(st.log) > 0 {
		t.Errorf("an unknown driver answered %v and ran %q", err, st.log)
	}
	big := ursus.Frame(ursus.Values("u", []uint64{1, math.MaxUint64}))
	db, st = writeDB(t)
	if _, err := big.WriteDatabase(ctx, db, "t", sqlite); err == nil || !strings.Contains(err.Error(), "SQLite") {
		t.Errorf("2^64-1 written to SQLite: %v", err)
	}
	if _, there := st.tables["t"]; there || st.log[len(st.log)-1] != "ROLLBACK" {
		t.Errorf("a refused write left %q", st.log)
	}
	db, st = writeDB(t)
	if _, err := big.WriteDatabase(ctx, db, "t", ursus.WithDialect(ursus.DialectPostgres)); err != nil {
		t.Fatal(err)
	}
	if got := st.tables["t"].rows[1][0]; got != "18446744073709551615" {
		t.Errorf("2^64-1 written to PostgreSQL as %#v", got)
	}
}

// TestWriteDatabaseRollsBack: an INSERT that fails undoes the write, the table it
// created among it; a caller's transaction is written in, and not committed.
func TestWriteDatabaseRollsBack(t *testing.T) {
	ctx := t.Context()
	db, st := writeDB(t)
	st.failInsert = 2
	_, err := writeFrame(t, 200).WriteDatabase(ctx, db, "t", ursus.WithDialect(ursus.DialectSQLite))
	if err == nil || !strings.Contains(err.Error(), "the disk is full") {
		t.Fatalf("a failed INSERT answered %v", err)
	}
	if _, there := st.tables["t"]; there || st.log[len(st.log)-1] != "ROLLBACK" {
		t.Errorf("a failed write left %q", st.log)
	}

	db, st = writeDB(t)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ursus.Frame(ursus.Values("a", []int64{1})).WriteDatabase(ctx, tx, "t",
		ursus.WithDialect(ursus.DialectSQLite)); err != nil {
		t.Fatal(err)
	}
	if got := st.log; slices.Contains(got, "COMMIT") || slices.Index(got, "BEGIN") != 0 || slices.Index(got[1:], "BEGIN") >= 0 {
		t.Errorf("a write in the caller's transaction ran %q", got)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// TestWriteDatabaseQuotesNames: a quote in a name is doubled, a dotted table is a
// schema's, and the query's rows are those the plan answers, a filter's.
func TestWriteDatabaseQuotesNames(t *testing.T) {
	db, st := writeDB(t)
	lf := ursus.Frame(ursus.Values(`a"b`, []int64{1, 2, 3})).Filter(ursus.Col(`a"b`).Gt(1))
	n, err := lf.WriteDatabase(context.Background(), db, "main.t", ursus.WithDialect(ursus.DialectSQLite),
		ursus.WithThreads(2))
	if err != nil {
		t.Fatal(err)
	}
	if want := `CREATE TABLE "main"."t" ("a""b" INTEGER)`; n != 2 || st.tables["t"].ddl != want {
		t.Errorf("%d rows, %s; want 2, %s", n, st.tables["t"].ddl, want)
	}
}
