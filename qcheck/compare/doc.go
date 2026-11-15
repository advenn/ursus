// Package compare checks qcheck's random queries against another engine, DuckDB
// (plan step 453b, step 513): each case's query runs in ursus, inside
// fuzzcheck.Run, and its SQL, sqlgen's, in the engine, and the two answers must
// agree.
//
// The comparator reads both answers into canonical cells by the query's types
// (FromUrsus, FromArrow). An answer whose order the query promises compares row by
// row, any other as a multiset. Cells compare exactly, floats by their bits, but
// for one allowance, counted when it fires, from the error model of the critic's
// correction to 437: a noisy column, a float Sum or Mean of a group-by that runs on
// every thread, differs from the engine's, which adds in the rows' order, by its
// order of addition when ursus ran on several threads: |Δ| ≤ 2·n·eps·Σ|x| for a
// Sum and 2·eps·Σ|x| + 2 ulp for a Mean, Σ|x| and n from ast.Bounded's columns.
//
// Everything else compares exactly, float arithmetic and aggregates included: each
// engine rounds each IEEE operation once, and the SQL is written to do the same
// operations; it adds a float Sum or Mean in the rows' order, as ursus does on one
// thread, and computes an integer Mean's nearest double exactly, as ursus does.
package compare
