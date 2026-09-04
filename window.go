package ursus

import (
	"ursus/internal/expr"
	"ursus/internal/uerr"
)

// MappingStrategy decides how a window's per-partition result returns to the frame.
type MappingStrategy = expr.MappingStrategy

const (
	// MapGroupsToRows gives every row its own partition's value, in the original
	// row order. The default, and the only one that composes with other columns.
	MapGroupsToRows = expr.MapGroupsToRows

	// MapExplode leaves the output in partition order instead of permuting it back.
	// Cheaper, because it skips the inverse permutation — and it REORDERS the frame,
	// so it is only meaningful when every column is windowed the same way.
	MapExplode = expr.MapExplode

	// MapJoin would aggregate each partition into a List repeated across its rows.
	// Refused: ursus has no List column layout yet.
	MapJoin = expr.MapJoin
)

// RankMethod decides how Rank breaks ties.
type RankMethod = expr.RankMethod

const (
	// RankOrdinal gives every row a distinct rank, ties broken by input order.
	RankOrdinal = expr.RankOrdinal
	// RankDense gives tied rows the same rank and does not skip the next value:
	// [10,20,20,30] ranks as 1,2,2,3.
	RankDense = expr.RankDense
	// RankMin and RankMax give tied rows the lowest or highest of their positions:
	// [10,20,20,30] ranks as 1,2,2,4 and 1,3,3,4.
	RankMin = expr.RankMin
	RankMax = expr.RankMax
	// RankAverage gives tied rows the mean of their positions, so it returns
	// Float64 where every other method returns Uint32.
	RankAverage = expr.RankAverage
)

// WindowSpec is the full form of a window, for OverWith.
type WindowSpec struct {
	PartitionBy []Expr
	OrderBy     []SortKey
	Mapping     MappingStrategy
}

// Over computes the expression within partitions and writes the answer back onto
// every row.
//
//	// each row's deviation from its group's mean
//	ursus.Col("x").Sub(ursus.Col("x").Mean().Over(ursus.Col("g")))
//
//	// dense rank of speed within each type
//	ursus.Col("speed").Rank(ursus.RankDense, true).Over(ursus.Col("type"))
//
// # The distinction from GroupBy
//
// GroupBy REDUCES: N rows in, one row per group out. Over PRESERVES: N rows in, N
// rows out, each carrying its own partition's answer. That is why an aggregate is
// refused in Select but the same aggregate under Over is not.
//
// With no partition keys the whole frame is one partition, so
// `Col("x").Sum().Over()` puts the grand total on every row.
//
// # Nulls form their own partition
//
// A null partition key groups with other nulls, the same rule GroupBy uses — as
// opposed to joins, where null keys match nothing. The two differ on purpose and
// each is documented where it applies.
func (e Expr) Over(partitionBy ...Expr) Expr {
	return e.OverWith(WindowSpec{PartitionBy: partitionBy})
}

// OverWith is Over with an ordering and a mapping strategy.
//
// An ordering is what makes Rank, the cumulative functions and Shift meaningful:
// without one they run in input order, which is well-defined but rarely what a
// ranking wants.
func (e Expr) OverWith(spec WindowSpec) Expr {
	part := make([]expr.Node, len(spec.PartitionBy))
	for i, p := range spec.PartitionBy {
		if p.n == nil {
			return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "over",
				"partition key %d is the zero Expr", i)})
		}
		part[i] = p.n
	}
	ord := make([]expr.OrderKey, len(spec.OrderBy))
	for i, k := range spec.OrderBy {
		if k.k.Expr == nil {
			return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "over",
				"order key %d is the zero SortKey", i)})
		}
		ord[i] = k.k
	}
	return wrap(&expr.Window{
		Child: e.n, PartitionBy: part, OrderBy: ord, Mapping: spec.Mapping,
	})
}

// --- ordered window functions ---------------------------------------------------

// Rank numbers rows by value within the window, smallest first unless descending.
//
// Ties are resolved by method; see RankMethod. Rank is never null — a null value
// still occupies a position — and returns Uint32, or Float64 for RankAverage.
func (e Expr) Rank(method RankMethod, descending bool) Expr {
	return e.winFn(expr.WinRank, expr.WinParams{Method: method, Descending: descending})
}

// CumCount numbers rows within the window, starting at 1.
//
// With reverse it counts from the end, so the last row is 1. That is what makes
// IsLastDistinct expressible as a window rather than a second pass.
func (e Expr) CumCount(reverse bool) Expr {
	return e.winFn(expr.WinCumCount, expr.WinParams{Reverse: reverse})
}

// CumSum, CumProd, CumMin and CumMax are running reductions over the window.
//
// Nulls are SKIPPED by the running value and PRESERVED in place: cum_sum of
// [1, null, 3] is [1, null, 4], not [1, 1, 4] and not [1, null, null]. The running
// total ignores what is not there; the output still says the value was missing.
//
// CumSum uses the same accumulator width as Sum, so the last row of `cum_sum(x)`
// equals `sum(x)` rather than differing by an overflow. CumProd returns Float64 for
// the reason Product does: i128 has addition but no multiplication.
func (e Expr) CumSum(reverse bool) Expr {
	return e.winFn(expr.WinCumSum, expr.WinParams{Reverse: reverse})
}

func (e Expr) CumProd(reverse bool) Expr {
	return e.winFn(expr.WinCumProd, expr.WinParams{Reverse: reverse})
}

func (e Expr) CumMin(reverse bool) Expr {
	return e.winFn(expr.WinCumMin, expr.WinParams{Reverse: reverse})
}

func (e Expr) CumMax(reverse bool) Expr {
	return e.winFn(expr.WinCumMax, expr.WinParams{Reverse: reverse})
}

// Shift moves values n places later within the window, filling the vacated rows
// with null. A negative n shifts earlier — Polars' lag and lead, one function.
//
// Rows shifted in from outside the partition are null, never a neighbouring
// partition's value.
func (e Expr) Shift(n int) Expr {
	return e.winFn(expr.WinShift, expr.WinParams{N: int64(n)})
}

// ShiftFill is Shift with a value instead of null in the vacated rows.
func (e Expr) ShiftFill[T Operand](n int, fill T) Expr {
	return wrap(&expr.WinFn{
		Fn: expr.WinShift, Child: e.n, Fill: lift(fill),
		Params: expr.WinParams{N: int64(n)},
	})
}

func (e Expr) winFn(fn expr.WinFnOp, p expr.WinParams) Expr {
	return wrap(&expr.WinFn{Fn: fn, Child: e.n, Params: p})
}

// ForwardFill replaces each null with the last non-null value before it.
//
// limit caps how many consecutive nulls one value may fill; 0 means unlimited. A
// leading run of nulls has nothing to carry and stays null.
//
// Like every ordered function it is a window: with no .Over() it runs over the
// whole frame in scan order, which makes it a pipeline breaker.
func (e Expr) ForwardFill(limit int) Expr {
	return e.winFn(expr.WinForwardFill, expr.WinParams{N: int64(limit)})
}

// BackwardFill replaces each null with the next non-null value after it.
//
// It is ForwardFill walked in the other direction, which costs nothing: the
// ordered walk flips its direction rather than building a second permutation.
func (e Expr) BackwardFill(limit int) Expr {
	return e.winFn(expr.WinBackwardFill, expr.WinParams{N: int64(limit)})
}

// Diff is the difference from the value n rows earlier.
//
// # It costs ONE shift, not two
//
// `e.Sub(e.Shift(n))` mentions the shift twice, and window resolution dedups on
// the rendered expression — so both mentions resolve to the same temporary and the
// shift is computed once. That is real common-subexpression elimination, unlike
// Coalesce, which has none.
//
// The type is whatever subtraction gives, which is the operand's own for a number
// and a DURATION for an instant — the temporal algebra's instant-minus-instant
// rule. Polars' diff carries a null_behavior parameter this composition cannot
// express; the first n rows are null here.
func (e Expr) Diff(n int) Expr { return e.Sub(e.Shift(n)) }

// PctChange is the fractional change from the value n rows earlier.
//
// Always a float on a numeric column, because it divides — and a zero previous
// value gives ±Inf rather than a null, since float division is total. On a
// temporal column it is a plan-time error: the difference is a Duration and the
// temporal algebra defines no Duration ÷ instant.
func (e Expr) PctChange(n int) Expr {
	prev := e.Shift(n)
	return e.Sub(prev).Div(prev)
}

// --- the predicates step 7 deferred to this step ---------------------------------

// IsUnique reports whether each row's value occurs exactly once in the column.
// IsDuplicated is its negation for non-null rows.
//
// # These are windows, and that is why they were not built earlier
//
// The answer for row 0 depends on the last row, so neither is elementwise. Step 7
// deferred them precisely here, and they need no new machinery: a value is unique
// iff the partition keyed by that value has exactly one row.
//
// Equality is GROUPING equality, the same the partitioning uses — so NaN equals NaN
// and -0.0 equals +0.0, and IsUnique agrees with Unique() about the same data.
// Nulls likewise group together, so two nulls are duplicates of each other.
func (e Expr) IsUnique() Expr { return e.Len().Over(e).Eq(int64(1)) }

func (e Expr) IsDuplicated() Expr { return e.Len().Over(e).Gt(int64(1)) }

// IsFirstDistinct and IsLastDistinct mark the first and last occurrence of each
// value, in input order.
//
// Note the asymmetry with IsUnique: a value occurring three times has one first
// occurrence, one last, and is not unique anywhere.
func (e Expr) IsFirstDistinct() Expr {
	return e.CumCount(false).Over(e).Eq(uint32(1))
}

func (e Expr) IsLastDistinct() Expr {
	return e.CumCount(true).Over(e).Eq(uint32(1))
}
