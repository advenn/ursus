// Package sqlgen turns a qcheck query into DuckDB's SQL (plan step 453b, step 513).
// It reads the same typed ast.Query the self check runs, and writes SQL that
// computes ursus's answer: where ursus answers differently from DuckDB by a rule of
// its own, the SQL follows ursus's rule. Each translation is a Rule, with an ID and
// the ursus doc or feature-gaps §9 entry it encodes (rules.go), and Translate
// counts the rules each query fires.
//
// The SQL is a chain of CTEs, one or a few per frame operation, each holding the
// frame's columns and __o, the row's place in the frame's order: a scan's __rid, a
// sort's row number, a group's first row, a join's left and right places. An
// answer whose order the query promises ends ORDER BY __o.
//
// The tables are registered as model.Table's records with __rid beside their
// columns, a Date as an INTEGER of days (T-DATE-DAYS); the macros the SQL calls
// are Macros, created once a connection.
package sqlgen
