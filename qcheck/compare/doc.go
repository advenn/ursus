// Package compare checks qcheck's random queries against another engine, DuckDB
// (plan step 453b, step 513): each case's query runs in ursus, inside
// fuzzcheck.Run, and its SQL, sqlgen's, in the engine, and the two answers must
// agree.
//
// The comparator reads both answers into canonical cells by the query's types
// (FromUrsus, FromArrow). An answer whose order the query promises compares row by
// row, any other as a multiset. Cells compare exactly, floats by their bits, with
// two allowances, each counted when it fires, both from the error model of the
// critic's correction to 437:
//
//   - A noisy column, a float Sum or Mean of a group-by that runs on every thread,
//     differs from the engine's, which adds in the rows' order, by its order of
//     addition when ursus ran on several threads: |Δ| ≤ 2·n·eps·Σ|x| for a Sum and
//     2·eps·Σ|x| + 2 ulp for a Mean, Σ|x| and n from ast.Bounded's columns.
//   - An integer Mean is ursus's exact sum divided once; DuckDB's SQL rounds the sum
//     to a double first, exact below 2^53, so a mean of magnitude 2^39 or more, a
//     sum past 2^53 over at most 2^14 rows, may differ by an ulp (T-MEAN-INT).
//
// Float arithmetic compares exactly: each engine rounds each IEEE operation once,
// and the SQL is written to do the same operations; so do a float Sum, Mean, Min
// and Max on one thread, which the SQL adds in the rows' order.
package compare
