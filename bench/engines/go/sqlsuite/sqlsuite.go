// Package sqlsuite loads the shared SQL text the SQL engines run.
//
// duckdb-go and chdb-go execute exactly the same files as the Python duckdb,
// datafusion and chdb engines: engines/sql/<suite>/<query>.sql. Keeping one copy
// means a difference between two rows in the report is a difference between two
// engines, not between two people's SQL.
package sqlsuite

import (
	"os"
	"path/filepath"
	"runtime"

	"ursusbench/engine"
)

// Dir is bench/engines/sql, resolved from this file's own location so the
// runner works from any working directory.
func Dir() string {
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		return "engines/sql"
	}
	// .../bench/engines/go/sqlsuite/sqlsuite.go -> .../bench/engines/sql
	return filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(self))), "sql")
}

// Read returns the SQL for one query, or ErrUnsupported when there is none.
func Read(a engine.Args) (string, error) {
	path := filepath.Join(Dir(), a.Suite, a.Query+".sql")
	blob, err := os.ReadFile(path)
	if err != nil {
		return "", engine.Unsupported("no SQL for %s/%s (%s)", a.Suite, a.Query, err)
	}
	return string(blob), nil
}
