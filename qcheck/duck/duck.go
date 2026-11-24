//go:build duckdb

package duck

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strconv"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/duckdb/duckdb-go/v2"

	"github.com/advenn/ursus/qcheck/ast"
	"github.com/advenn/ursus/qcheck/model"
	"github.com/advenn/ursus/qcheck/sqlgen"
)

// Supported is whether this binary has DuckDB.
const Supported = true

// Settings are what a DB sets before anything else: one thread, so its float
// sums and its row order through a pipeline do not move; IEEE floats, x/0 an
// infinity and 0/0 NaN, not null (sqlgen's T-DIV-FLOAT); the insertion order kept.
var Settings = []string{
	"SET threads TO 1",
	"SET ieee_floating_point_ops TO true",
	"SET preserve_insertion_order TO true",
	"SET memory_limit TO '2GB'",
}

// DB is one in-memory DuckDB database on one connection.
type DB struct {
	db     *sql.DB
	conn   *sql.Conn
	ar     *duckdb.Arrow
	tables []string // the tables Load made
	views  int      // views registered, for fresh names
}

// Open opens an in-memory DuckDB, applies Settings and creates sqlgen's macros.
func Open() (*DB, error) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, err
	}
	d := &DB{db: db, conn: conn}
	err = conn.Raw(func(dc any) error {
		ar, err := duckdb.NewArrowFromConn(dc.(driver.Conn))
		d.ar = ar
		return err
	})
	if err != nil {
		d.Close()
		return nil, err
	}
	for _, s := range append(append([]string(nil), Settings...), sqlgen.Macros...) {
		if err := d.Exec(ctx, s); err != nil {
			d.Close()
			return nil, fmt.Errorf("duck: %s: %w", s, err)
		}
	}
	return d, nil
}

// Close closes the connection and the database.
func (d *DB) Close() error {
	if d.conn != nil {
		d.conn.Close()
	}
	return d.db.Close()
}

// Exec runs a statement.
func (d *DB) Exec(ctx context.Context, s string) error {
	_, err := d.conn.ExecContext(ctx, s)
	return err
}

// Query runs a query and returns its answer as Arrow records, which the caller
// releases.
func (d *DB) Query(ctx context.Context, s string) (*arrow.Schema, []arrow.RecordBatch, error) {
	r, err := d.ar.QueryContext(ctx, s)
	if err != nil {
		return nil, nil, err
	}
	defer r.Release()
	var recs []arrow.RecordBatch
	for r.Next() {
		rec := r.RecordBatch()
		rec.Retain()
		recs = append(recs, rec)
	}
	if err := r.Err(); err != nil {
		for _, rec := range recs {
			rec.Release()
		}
		return nil, nil, err
	}
	return r.Schema(), recs, nil
}

// Register makes rec a native table called name, through an Arrow view, which can
// be read once.
func (d *DB) Register(name string, rec arrow.RecordBatch) error {
	d.views++
	view := fmt.Sprintf("u_view%d", d.views)
	rr, err := array.NewRecordReader(rec.Schema(), []arrow.RecordBatch{rec})
	if err != nil {
		return err
	}
	defer rr.Release()
	release, err := d.ar.RegisterView(rr, view)
	if err != nil {
		return err
	}
	defer release()
	ctx := context.Background()
	if err := d.Exec(ctx, "CREATE OR REPLACE TABLE "+sqlgen.Quote(name)+" AS SELECT * FROM "+sqlgen.Quote(view)); err != nil {
		return err
	}
	return d.Exec(ctx, "DROP VIEW IF EXISTS "+sqlgen.Quote(view))
}

// Load makes tables the database's, replacing the last Load's: each record with
// __rid, its row numbers, first, a Date as an Int32 of its days, and a Datetime, a
// Duration and a Time as an Int64 of its ticks (WithRID).
func (d *DB) Load(tables []*model.Table) error {
	ctx := context.Background()
	for _, name := range d.tables {
		if err := d.Exec(ctx, "DROP TABLE IF EXISTS "+sqlgen.Quote(name)); err != nil {
			return err
		}
	}
	d.tables = d.tables[:0]
	for _, t := range tables {
		rec := WithRID(t.Rec)
		err := d.Register(t.Name, rec)
		rec.Release()
		if err != nil {
			return fmt.Errorf("duck: registering %s: %w", t.Name, err)
		}
		d.tables = append(d.tables, t.Name)
	}
	return nil
}

// Answer runs s's query.
func (d *DB) Answer(ctx context.Context, q *ast.Query, s *sqlgen.SQL) (*arrow.Schema, []arrow.RecordBatch, error) {
	return d.Query(ctx, s.Text)
}

// Counts runs s's counts of each node.
func (d *DB) Counts(ctx context.Context, q *ast.Query, s *sqlgen.SQL) ([]int64, error) {
	_, recs, err := d.Query(ctx, s.Counts)
	if err != nil {
		return nil, err
	}
	defer func() {
		for _, r := range recs {
			r.Release()
		}
	}()
	out := make([]int64, len(s.Nodes))
	seen := 0
	for _, r := range recs {
		for i := range int(r.NumRows()) {
			idx, err := intAt(r.Column(0), i)
			if err != nil {
				return nil, err
			}
			n, err := intAt(r.Column(1), i)
			if err != nil {
				return nil, err
			}
			if idx < 0 || int(idx) >= len(out) {
				return nil, fmt.Errorf("duck: a count of node %d of %d", idx, len(out))
			}
			out[idx] = n
			seen++
		}
	}
	if seen != len(out) {
		return nil, fmt.Errorf("duck: %d counts of %d nodes", seen, len(out))
	}
	return out, nil
}

func intAt(a arrow.Array, i int) (int64, error) {
	switch v := a.(type) {
	case *array.Int32:
		return int64(v.Value(i)), nil
	case *array.Int64:
		return v.Value(i), nil
	}
	return 0, fmt.Errorf("duck: a count of Arrow %s: %s", a.DataType(), strconv.Quote(a.String()))
}
