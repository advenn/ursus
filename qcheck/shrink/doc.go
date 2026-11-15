// Package shrink shrinks a qcheck finding to the smallest case that still fails the
// same way, and writes it out as data and as the code that reproduces it (plan step
// 455, step 514).
//
// A Case is JSON: its tables, generated (a seed and a shape) or rows once shrunk;
// its query, step 512's typed AST; the configurations it runs under; and the
// Signature of how it fails: the kind (a mismatch, a refusal, ErrInternal, a panic,
// an allocation past the bound, a goroutine left behind, a hang), with the
// differing column's operation and type, or the error's class. A case from the self
// check compares the reference configuration with the failing one; a case from
// DuckDB (package compare) compares sqlgen's SQL in DuckDB with ursus under the
// failing configuration.
//
// Shrink reduces a case in passes, each step kept only where the case still fails
// with the same signature and its query passes ast.Check: the fewest configuration
// factors, by the self check's bisection (self.Bisect); ddmin on each table's rows;
// the query's operations dropped; aggregations, keys, columns and sort keys
// dropped; expressions replaced by simpler ones; columns and tables no longer read
// dropped; and values simplified, toward 0, to short text, to fewer distinct
// values. A step is kept only where it lowers the case's cost, which ends the
// passes. The budget is 2,000 runs or 120 seconds; every query runs inside
// fuzzcheck.Run.
//
// A shrunk case is emitted as a Go test in package ursus_test, a differential of
// the reference configuration against the failing one over the shrunk rows
// (EmitGo), with DuckDB's SQL for a DuckDB case (EmitSQL) and a Polars snippet
// where the operations map (EmitPolars). qcheck/testdata/corpus holds shrunk
// cases, and TestCorpus replays each on every run: one that fails and is not
// marked open, naming the step that fixes it, fails the test, and so does one
// marked open that no longer fails.
package shrink
