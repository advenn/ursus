// Package duck runs qcheck's SQL in DuckDB, in the same process, through
// duckdb-go v2.10505.0 (DuckDB 1.5.5), and reads its answers back as Arrow (plan
// step 453b, step 513). A DB is one in-memory database on one connection, on one
// thread, so DuckDB's own answers are deterministic, with sqlgen's macros created
// and ieee_floating_point_ops on.
//
// It registers each model.Table's record with a hidden __rid beside its columns,
// each row's place, a Date as an INTEGER of its days (sqlgen's T-DATE-DAYS), and a
// Datetime, a Duration and a Time as a BIGINT of its ticks (T-TICKS, step 539),
// copied into a native table, as an Arrow view can be read once. A DB is a
// compare.Engine.
//
// duckdb-go is cgo and links a prebuilt library of 131 MB, so the runner is behind
// the duckdb build tag, with duckdb-go's own duckdb_arrow tag for its Arrow
// interface: go test -tags duckdb,duckdb_arrow. Without the tag a stub reports it
// unsupported, as bench/engines/go/duckdbengine's does. The link takes 1–2 GB, so
// it runs on CI, in ci.yml's qcheck-duckdb job, not on a laptop.
package duck
