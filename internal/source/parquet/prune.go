package parquet

import (
	"github.com/apache/arrow-go/v18/parquet/metadata"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// Row-group pruning: the one part of this package that can change which rows come
// back, and therefore the one that is written to be conservative in a single
// direction.
//
// # The rule
//
// Every function here answers "could this row group contain a matching row?" and
// is allowed to answer YES when the truth is no. It is never allowed to answer NO
// when the truth is yes. A wrong YES costs a wasted read; a wrong NO silently
// deletes data.
//
// That asymmetry is why every unknown — a missing statistic, an unfamiliar
// expression node, a type the comparison does not handle — returns "maybe".
//
// # Three ways arrow-go's statistics will mislead a naive pruner
//
// All three are reachable with files arrow-go itself writes.
//
//  1. Statistics().NumValues() is the NON-NULL count, not the row count
//     (metadata/column_chunk.go computes nvalues = NumValues - NullCount). So the
//     obvious "the group is all nulls if NullCount == NumValues" is ALWAYS FALSE,
//     and it would silently never prune. The correct comparison is against the
//     column chunk's NumValues, which is the row count.
//
//  2. HasMinMax() is an OR over two independently-droppable fields.
//     ApplyStatSizeLimits clears Max on its own when it exceeds MaxStatsSize
//     (4096 by default), and the read path sets hasMinMax = IsSetMax() ||
//     IsSetMin(). A column with one 5 KB string therefore reads back as
//     HasMinMax() == true with Max == "". A pruner that trusts that would decide
//     `col > "z"` cannot match and skip a group that is full of matches.
//
//     This comment used to say the defence was the min > max check below. It is
//     not, whenever the min is ALSO "": "" > "" is false, so a group holding ""
//     and one long string read as a range holding only "" (audit.md §5, I4). The
//     defence is statBounds: an empty string max is no bound at all.
//
//  3. Statistics are optional. A file written without them has no min or max at
//     all, and every group must be read.
//
// # And two ways the values themselves mislead it
//
//  4. NaN is left out of a float's min and max, and NaN != v is true, so a
//     group can hold a row matching != that its bounds say it cannot. See the Ne
//     arm below (I3).
//
//  5. A NESTED column has no statistics of its own, only its leaves' — a
//     struct's fields, a list's elements — and none of them states whether the
//     column itself is null. Nested columns are never pruned (I5).
//
// Two more are recorded and not handled, because only a third-party writer
// reaches them and nothing in arrow-go's read API can detect them: HasNullCount()
// is true for every statistic read from a file, so a file written without
// null_count reads as having no nulls; and a one-sided integer or float min or max
// reads its absent side as 0.

// prunable reports whether the pruner understands this conjunct at all. It runs at
// PLAN time, to decide what to classify Inexact, and must agree with what
// evalPredicate actually does — a conjunct claimed and then ignored is only a
// wasted Filter, but a conjunct ignored here and used there would be a lie.
//
// Only FLAT columns are prunable. A struct or a list has no statistics of its
// own, only its leaves', and no leaf says whether the column itself is null: a
// struct's first field can be null in every row of a group of present structs,
// and a list's element leaf counts an empty list as a null. Both pruned IsNotNull
// groups that held matches (audit.md §5, I5).
func prunable(e expr.Node, s *dtype.Schema) bool {
	switch t := e.(type) {
	case *expr.Binary:
		switch t.Op {
		case expr.OpAnd, expr.OpOr:
			return prunable(t.L, s) && prunable(t.R, s)
		case expr.OpEq, expr.OpNe, expr.OpLt, expr.OpLe, expr.OpGt, expr.OpGe:
			name, lit, ok := colAndLit(t)
			return ok && flat(s, name) && sameTicks(s, name, lit)
		}
	case *expr.Unary:
		if t.Op == expr.OpIsNull || t.Op == expr.OpIsNotNull {
			c, ok := t.Child.(*expr.Col)
			return ok && flat(s, c.Name)
		}
	}
	return false
}

// sameTicks reports whether a temporal literal's ticks can be compared with the
// column's statistics as they are: only when its dtype is the column's, unit and
// zone included. A Datetime in microseconds against one in milliseconds holds a
// thousand times the column's ticks. The evaluator casts one side to the other;
// the pruner compares raw numbers, and would skip groups that match (step 142).
// A literal that is not temporal, against a column that is not, is unchanged.
func sameTicks(s *dtype.Schema, name string, lit *expr.Lit) bool {
	col := s.Field(s.IndexOf(name)).Type
	if !lit.DT.IsTemporal() && !col.IsTemporal() {
		return true
	}
	return lit.DT == col
}

// flat reports whether name is a column of s that is not nested.
func flat(s *dtype.Schema, name string) bool {
	i := s.IndexOf(name)
	return i >= 0 && !s.Field(i).Type.IsNested()
}

// leafOf resolves a column NAME to the index of its one leaf in the file being
// read, or reports that it has no single leaf whose statistics describe it.
//
// By name, and per file, because a column's leaf index is neither its position in
// the schema nor the same in every file. A struct or list before it adds leaves:
// in {st: struct<a, b>, c}, c is field 1 and leaf 2, and reading leaf 1 took st.b's
// statistics for c (audit.md §5, I2).
type leafOf func(name string) (leaf int, ok bool)

// colAndLit matches `col OP literal` or `literal OP col`, returning the column's
// name and the literal.
func colAndLit(b *expr.Binary) (name string, lit *expr.Lit, ok bool) {
	if c, isCol := b.L.(*expr.Col); isCol {
		if l, isLit := b.R.(*expr.Lit); isLit {
			return c.Name, l, true
		}
	}
	if c, isCol := b.R.(*expr.Col); isCol {
		if l, isLit := b.L.(*expr.Lit); isLit {
			return c.Name, l, true
		}
	}
	return "", nil, false
}

// flipped returns the operator with its operands swapped, so `5 < col` can be
// evaluated as `col > 5`.
func flipped(op expr.BinaryOp) expr.BinaryOp {
	switch op {
	case expr.OpLt:
		return expr.OpGt
	case expr.OpLe:
		return expr.OpGe
	case expr.OpGt:
		return expr.OpLt
	case expr.OpGe:
		return expr.OpLe
	default:
		return op // Eq and Ne are symmetric
	}
}

// canSkipRowGroup reports whether every row of the group provably fails at least
// one conjunct.
func canSkipRowGroup(rg *metadata.RowGroupMetaData, leaf leafOf, preds []expr.Node) (bool, error) {
	for _, p := range preds {
		may, err := mayMatch(rg, leaf, p)
		if err != nil {
			return false, err
		}
		if !may {
			// Conjuncts are ANDed, so one impossible conjunct rules out the group.
			return true, nil
		}
	}
	return false, nil
}

// mayMatch reports whether any row of the group could satisfy e. "true" means
// maybe; only "false" is a claim, and it must be provable.
func mayMatch(rg *metadata.RowGroupMetaData, leaf leafOf, e expr.Node) (bool, error) {
	switch t := e.(type) {
	case *expr.Binary:
		switch t.Op {
		case expr.OpAnd:
			l, err := mayMatch(rg, leaf, t.L)
			if err != nil || !l {
				return false, err
			}
			return mayMatch(rg, leaf, t.R)

		case expr.OpOr:
			l, err := mayMatch(rg, leaf, t.L)
			if err != nil {
				return false, err
			}
			if l {
				return true, nil
			}
			return mayMatch(rg, leaf, t.R)

		case expr.OpEq, expr.OpNe, expr.OpLt, expr.OpLe, expr.OpGt, expr.OpGe:
			name, lit, ok := colAndLit(t)
			if !ok {
				return true, nil
			}
			col, ok := leaf(name)
			if !ok {
				return true, nil
			}
			op := t.Op
			if _, isCol := t.L.(*expr.Col); !isCol {
				op = flipped(op)
			}
			return compareToStats(rg, col, op, lit)
		}

	case *expr.Unary:
		c, ok := t.Child.(*expr.Col)
		if !ok {
			return true, nil
		}
		col, ok := leaf(c.Name)
		if !ok {
			return true, nil
		}
		switch t.Op {
		case expr.OpIsNull:
			return mayHaveNull(rg, col)
		case expr.OpIsNotNull:
			return mayHaveValue(rg, col)
		}
	}
	// An expression this pruner does not model. Read the group.
	return true, nil
}

// statsFor returns the statistics for a column chunk, or nil if there are none to
// trust.
func statsFor(rg *metadata.RowGroupMetaData, col int) (metadata.TypedStatistics, *metadata.ColumnChunkMetaData, error) {
	// arrow-go's ColumnChunk PANICS on an index past the last column, and a pruner
	// with a wrong mapping would reach it. No statistics is the safe answer.
	if col < 0 || col >= rg.NumColumns() {
		return nil, nil, nil
	}
	cc, err := rg.ColumnChunk(col)
	if err != nil {
		return nil, nil, uerr.Wrap(err, uerr.KindIO, "scan_parquet",
			"reading column chunk metadata")
	}
	set, err := cc.StatsSet()
	if err != nil || !set {
		return nil, cc, nil
	}
	st, err := cc.Statistics()
	if err != nil {
		return nil, cc, nil // unreadable statistics are absent statistics
	}
	return st, cc, nil
}

// mayHaveNull reports whether the group could contain a null in this column.
func mayHaveNull(rg *metadata.RowGroupMetaData, col int) (bool, error) {
	st, _, err := statsFor(rg, col)
	if err != nil {
		return false, err
	}
	if st == nil || !st.HasNullCount() {
		return true, nil
	}
	return st.NullCount() > 0, nil
}

// mayHaveValue reports whether the group could contain a non-null in this column.
//
// Note what is NOT written here: `st.NullCount() == st.NumValues()`. Statistics'
// NumValues is the NON-NULL count, so that comparison is true only when both are
// zero and would never prune anything. The row count lives on the column chunk.
func mayHaveValue(rg *metadata.RowGroupMetaData, col int) (bool, error) {
	st, cc, err := statsFor(rg, col)
	if err != nil {
		return false, err
	}
	if st == nil || !st.HasNullCount() || cc == nil {
		return true, nil
	}
	return st.NullCount() < cc.NumValues(), nil
}

// compareToStats decides whether `column op literal` could hold for some row.
//
// A null never satisfies a comparison, so a group whose column is entirely null
// can be skipped regardless of the bounds — which is worth doing separately
// because such a group typically has no min or max at all.
func compareToStats(rg *metadata.RowGroupMetaData, col int, op expr.BinaryOp, lit *expr.Lit) (bool, error) {
	st, cc, err := statsFor(rg, col)
	if err != nil {
		return false, err
	}
	if st == nil {
		return true, nil
	}
	if cc != nil && st.HasNullCount() && st.NullCount() == cc.NumValues() && cc.NumValues() > 0 {
		return false, nil // every row is null; no comparison can be true
	}
	if !st.HasMinMax() {
		return true, nil
	}

	lo, hi, ok := statBounds(st)
	if !ok {
		return true, nil // a type this pruner does not compare
	}
	// The sanity check that defends against arrow-go dropping one bound and leaving
	// HasMinMax true. If min > max the pair is not a range, so it tells us nothing.
	if lo.cmp(hi) > 0 {
		return true, nil
	}

	v, ok := litBound(lit, lo.kind)
	if !ok {
		return true, nil
	}

	switch op {
	case expr.OpEq:
		return lo.cmp(v) <= 0 && v.cmp(hi) <= 0, nil
	case expr.OpNe:
		// Only prunable when every value is the same one the predicate excludes —
		// which a float's statistics cannot show. Writers leave NaN out of min and
		// max, and NaN != v is TRUE, so a group of 1s and NaNs reads as [1, 1] and
		// still holds rows that match (audit.md §5, I3). Ne is the only arm NaN can
		// defeat: it makes every other comparison false, so their pruning is sound.
		if lo.kind == boundFloat {
			return true, nil
		}
		return !(lo.cmp(hi) == 0 && lo.cmp(v) == 0), nil
	case expr.OpLt:
		return lo.cmp(v) < 0, nil
	case expr.OpLe:
		return lo.cmp(v) <= 0, nil
	case expr.OpGt:
		return hi.cmp(v) > 0, nil
	case expr.OpGe:
		return hi.cmp(v) >= 0, nil
	}
	return true, nil
}
