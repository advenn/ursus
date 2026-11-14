// Package sqlgen will turn a qcheck query into DuckDB's SQL, by rules each with an
// ID and a source: T-NULLS, T-IEEE-CMP, T-KEY-ZERO, T-INT-WRAP, T-FLOOR,
// T-DIV-FLOAT, T-LIT-TYPE, T-ISIN-NULL and T-CAST-EXACT (plan step 453b). It is
// step 513's; step 512 leaves it empty. It reads the ast package's typed tree and
// the model package's tables, and a table's rows reach DuckDB as gen's Arrow
// records with a hidden __rid column.
package sqlgen
