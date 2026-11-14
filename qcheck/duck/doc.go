// Package duck will run a qcheck query's SQL in DuckDB in the same process, through
// duckdb-go, and read its answer back as Arrow (plan step 453b). It is step 513's;
// step 512 leaves it empty, and adds no dependency. duckdb-go is cgo and links a
// large prebuilt library, so 513 puts it behind the duckdb build tag, with a stub
// for !duckdb that reports it unsupported, as bench/engines/go/duckdbengine does.
package duck
