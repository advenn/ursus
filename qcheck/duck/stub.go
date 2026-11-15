//go:build !duckdb

package duck

import (
	"context"
	"errors"

	"github.com/apache/arrow-go/v18/arrow"

	"github.com/advenn/ursus/qcheck/ast"
	"github.com/advenn/ursus/qcheck/model"
	"github.com/advenn/ursus/qcheck/sqlgen"
)

// ErrUnsupported is every call's error in a binary built without the duckdb tag.
var ErrUnsupported = errors.New("duck: built without -tags duckdb,duckdb_arrow: DuckDB is unsupported " +
	"(it is cgo and links a 131 MB library; ci.yml's qcheck-duckdb job builds it)")

// Supported is whether this binary has DuckDB.
const Supported = false

// DB is DuckDB, which this binary does not have.
type DB struct{}

// Open reports that this binary was built without DuckDB.
func Open() (*DB, error) { return nil, ErrUnsupported }

// Close does nothing.
func (d *DB) Close() error { return nil }

// Exec reports ErrUnsupported.
func (d *DB) Exec(ctx context.Context, sql string) error { return ErrUnsupported }

// Query reports ErrUnsupported.
func (d *DB) Query(ctx context.Context, sql string) (*arrow.Schema, []arrow.RecordBatch, error) {
	return nil, nil, ErrUnsupported
}

// Register reports ErrUnsupported.
func (d *DB) Register(name string, rec arrow.RecordBatch) error { return ErrUnsupported }

// Load reports ErrUnsupported.
func (d *DB) Load(tables []*model.Table) error { return ErrUnsupported }

// Answer reports ErrUnsupported.
func (d *DB) Answer(ctx context.Context, q *ast.Query, s *sqlgen.SQL) (*arrow.Schema, []arrow.RecordBatch, error) {
	return nil, nil, ErrUnsupported
}

// Counts reports ErrUnsupported.
func (d *DB) Counts(ctx context.Context, q *ast.Query, s *sqlgen.SQL) ([]int64, error) {
	return nil, ErrUnsupported
}
