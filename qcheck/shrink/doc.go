// Package shrink will shrink a case that fails to the smallest that still fails
// with the same signature (plan step 455): AST delta debugging, ddmin on rows,
// dropped columns, simplified values, then the fewest configuration factors; and
// emit it as a Go test in package ursus_test, DuckDB's SQL and a Polars snippet. It
// is step 514's; step 512 leaves it empty. A case is a seed (gen.NewCase), and a
// self finding names its factors already (self.Finding.Factors).
package shrink
