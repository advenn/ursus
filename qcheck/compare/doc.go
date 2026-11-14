// Package compare will compare an ursus answer with DuckDB's (plan step 453b): over
// Arrow cells, exactly except a float from arithmetic, whose tolerance comes from
// an error model; a float aggregate's from ast.Bounded's columns, as the self check
// bounds the float-threads rule. It is step 513's; step 512 leaves it empty. The
// self check's own bit-exact comparison is package self's.
package compare
