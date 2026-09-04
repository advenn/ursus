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
//     The defence is the min > max sanity check in each comparison below.
//
//  3. Statistics are optional. A file written without them has no min or max at
//     all, and every group must be read.

// prunable reports whether the pruner understands this conjunct at all. It runs at
// PLAN time, to decide what to classify Inexact, and must agree with what
// evalPredicate actually does — a conjunct claimed and then ignored is only a
// wasted Filter, but a conjunct ignored here and used there would be a lie.
func prunable(e expr.Node, s *dtype.Schema) bool {
	switch t := e.(type) {
	case *expr.Binary:
		switch t.Op {
		case expr.OpAnd, expr.OpOr:
			return prunable(t.L, s) && prunable(t.R, s)
		case expr.OpEq, expr.OpNe, expr.OpLt, expr.OpLe, expr.OpGt, expr.OpGe:
			_, _, ok := colAndLit(t, s)
			return ok
		}
	case *expr.Unary:
		if t.Op == expr.OpIsNull || t.Op == expr.OpIsNotNull {
			c, ok := t.Child.(*expr.Col)
			return ok && s.Has(c.Name)
		}
	}
	return false
}

// colAndLit matches `col OP literal` or `literal OP col`, returning the column
// index and the literal with the operator normalised so the column is on the left.
func colAndLit(b *expr.Binary, s *dtype.Schema) (col int, lit *expr.Lit, ok bool) {
	if c, isCol := b.L.(*expr.Col); isCol {
		if l, isLit := b.R.(*expr.Lit); isLit {
			if i := s.IndexOf(c.Name); i >= 0 {
				return i, l, true
			}
		}
	}
	if c, isCol := b.R.(*expr.Col); isCol {
		if l, isLit := b.L.(*expr.Lit); isLit {
			if i := s.IndexOf(c.Name); i >= 0 {
				return i, l, true
			}
		}
	}
	return 0, nil, false
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
func canSkipRowGroup(rg *metadata.RowGroupMetaData, s *dtype.Schema, preds []expr.Node) (bool, error) {
	for _, p := range preds {
		may, err := mayMatch(rg, s, p)
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
func mayMatch(rg *metadata.RowGroupMetaData, s *dtype.Schema, e expr.Node) (bool, error) {
	switch t := e.(type) {
	case *expr.Binary:
		switch t.Op {
		case expr.OpAnd:
			l, err := mayMatch(rg, s, t.L)
			if err != nil || !l {
				return false, err
			}
			return mayMatch(rg, s, t.R)

		case expr.OpOr:
			l, err := mayMatch(rg, s, t.L)
			if err != nil {
				return false, err
			}
			if l {
				return true, nil
			}
			return mayMatch(rg, s, t.R)

		case expr.OpEq, expr.OpNe, expr.OpLt, expr.OpLe, expr.OpGt, expr.OpGe:
			col, lit, ok := colAndLit(t, s)
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
		col := s.IndexOf(c.Name)
		if col < 0 {
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
		// Only prunable when every value is the same one the predicate excludes.
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
