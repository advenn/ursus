//go:build duckdb

// Package duckdbengine runs the benchmark suites against duckdb-go.
//
// This is the honest ceiling for a Go program: a real analytical engine reached
// through database/sql. It runs the same SQL text as the Python duckdb engine,
// so the gap between the two rows in the report is the cost of the binding, not
// of the query.
//
// Behind the `duckdb` build tag because it is cgo and links a large prebuilt
// library. `make setup-cgo` builds it.
package duckdbengine

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/duckdb/duckdb-go/v2"

	"ursusbench/engine"
	"ursusbench/sqlsuite"
)

func init() { engine.Register("duckdbgo", Build) }

// Build loads the query text and returns a closure that opens a fresh in-memory
// database, registers the input tables as views, and runs it.
func Build(a engine.Args) (engine.Once, error) {
	query, err := sqlsuite.Read(a)
	if err != nil {
		return nil, err
	}

	return func(ctx context.Context) (engine.Answer, error) {
		db, err := sql.Open("duckdb", "")
		if err != nil {
			return nil, err
		}
		if err := prepare(ctx, db, a); err != nil {
			db.Close()
			return nil, err
		}
		// Materialise into a temporary table: `Answer` needs a row count and
		// either the full result or a set of column sums, and pulling millions
		// of rows through database/sql to count them would measure the driver
		// rather than the engine.
		if _, err := db.ExecContext(ctx,
			"CREATE OR REPLACE TEMP TABLE answer AS "+query); err != nil {
			db.Close()
			return nil, err
		}
		var rows int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM answer").Scan(&rows); err != nil {
			db.Close()
			return nil, err
		}
		return &answer{db: db, rows: rows}, nil
	}, nil
}

func prepare(ctx context.Context, db *sql.DB, a engine.Args) error {
	if _, err := db.ExecContext(ctx, "SET preserve_insertion_order TO false"); err != nil {
		return err
	}
	if a.Threads > 0 {
		if _, err := db.ExecContext(ctx, fmt.Sprintf("SET threads TO %d", a.Threads)); err != nil {
			return err
		}
	}
	for _, table := range a.Inputs() {
		reader := "read_parquet"
		if a.IO == "csv" {
			reader = "read_csv_auto"
		}
		stmt := fmt.Sprintf("CREATE OR REPLACE VIEW %s AS SELECT * FROM %s('%s')",
			table, reader, a.TablePath(table))
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// answer keeps the connection alive so the temp table can be re-read for the
// checksum or copied out to Parquet.
type answer struct {
	db   *sql.DB
	rows int
}

func (a *answer) Rows() int { return a.rows }

// Close releases the connection. The harness calls it when this answer is
// superseded by the next iteration and once more at the end.
func (a *answer) Close() error { return a.db.Close() }

func (a *answer) WriteFrame(ctx context.Context, path string) error {
	_, err := a.db.ExecContext(ctx, fmt.Sprintf(
		"COPY answer TO '%s' (FORMAT PARQUET, COMPRESSION ZSTD)", path))
	return err
}

func (a *answer) Checksum(ctx context.Context, cols []string) (map[string]float64, error) {
	projection := ""
	for i, name := range cols {
		if i > 0 {
			projection += ", "
		}
		projection += fmt.Sprintf(`sum(CAST("%s" AS DOUBLE))`, name)
	}

	row := a.db.QueryRowContext(ctx, "SELECT "+projection+" FROM answer")
	scanned := make([]any, len(cols))
	targets := make([]*sql.NullFloat64, len(cols))
	for i := range cols {
		targets[i] = &sql.NullFloat64{}
		scanned[i] = targets[i]
	}
	if err := row.Scan(scanned...); err != nil {
		return nil, err
	}

	sums := make(map[string]float64, len(cols))
	for i, name := range cols {
		if targets[i].Valid {
			sums[name] = targets[i].Float64
		}
	}
	return sums, nil
}
