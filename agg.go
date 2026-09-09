package ursus

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/uerr"
)

// Interpolation selects how Quantile resolves a rank falling between two values.
//
// Aliased from the expression IR rather than redeclared, so the kernel and the
// public API cannot drift — the same arrangement JoinKind uses.
type Interpolation = expr.Interpolation

// Closed says which endpoints of an interval belong to it: [lo, hi), (lo, hi],
// [lo, hi] or (lo, hi). Used by IsBetween and by the temporal group-by's windows.
type Closed = expr.Closed

// The four window boundary conventions. ClosedLeft is the default for a window grid
// — an instant exactly on a boundary starts the new window — because it is the only
// one under which consecutive windows tile the line exactly.
const (
	// ClosedDefault is the zero value and means "this operation's own convention":
	// both for IsBetween, left for GroupByDynamic, right for Rolling.
	ClosedDefault = expr.ClosedDefault
	ClosedLeft    = expr.ClosedLeft
	ClosedRight   = expr.ClosedRight
	ClosedBoth    = expr.ClosedBoth
	ClosedNone    = expr.ClosedNone
)

const (
	// InterpLinear interpolates proportionally between the neighbours. It is the
	// conventional default and the only option whose result need not be an input
	// value.
	InterpLinear = expr.InterpLinear

	// InterpLower and InterpHigher take the neighbour below or above the rank.
	InterpLower  = expr.InterpLower
	InterpHigher = expr.InterpHigher

	// InterpNearest takes the closer neighbour, rounding halves upward.
	InterpNearest = expr.InterpNearest

	// InterpMidpoint averages the two neighbours regardless of the rank's position
	// between them.
	InterpMidpoint = expr.InterpMidpoint
)

// --- aggregate expressions ----------------------------------------------------
//
// Every aggregate skips nulls except Len, First and Last. That is the SQL rule and
// it is applied uniformly: an aggregate that treated nulls differently from its
// neighbours would be a permanent source of quiet mistakes.

// Sum adds the non-null values.
//
// An INTEGER sum accumulates and returns Int128, following DuckDB. Summing a
// thousand Int8 values overflows an Int8 immediately, and a wrapped sum is a
// plausible-looking wrong number that nothing downstream can detect. At 128 bits
// overflow needs 2^63 maximum-magnitude rows, i.e. it cannot happen.
//
// A FLOAT sum accumulates in Float64 even when it returns Float32, because naive
// float32 accumulation stops making progress past ~2^24 elements.
//
// Sum of a group with no non-null values is NULL, not 0 — ursus follows SQL here
// rather than Polars. `0` cannot be distinguished afterwards from a genuine zero;
// NULL can be turned into 0 with FillNull if that is what you want.
func (e Expr) Sum() Expr { return e.agg(expr.AggSum) }

// Mean is the sum of non-null values divided by the COUNT of non-null values —
// never by the group size, which would silently bias the result downward whenever
// nulls are present.
//
// Mean of a group with no non-null values is NULL, not NaN: there was nothing to
// compute, as opposed to a computation that came out undefined.
func (e Expr) Mean() Expr { return e.agg(expr.AggMean) }

// Min and Max use ursus's TOTAL order, the same one Sort uses: NaN sorts above
// everything and -0.0 equals +0.0. They therefore always agree with
// Sort(...).First() / .Last(), which IEEE comparison would not.
func (e Expr) Min() Expr { return e.agg(expr.AggMin) }
func (e Expr) Max() Expr { return e.agg(expr.AggMax) }

// Count counts NON-NULL values — SQL's COUNT(col).
func (e Expr) Count() Expr { return e.agg(expr.AggCount) }

// Len counts ROWS, including nulls — SQL's COUNT(*).
//
// Count and Len are deliberately separate and are never aliased: over a column
// with nulls they give different answers, and which one was meant is not
// recoverable after the fact.
func (e Expr) Len() Expr { return e.agg(expr.AggLen) }

// NUnique counts distinct non-null values.
//
// Nulls are SKIPPED, so NUnique over an all-null group is 0. Polars counts null as
// a distinct value; ursus follows SQL and, more importantly, follows its own other
// aggregates.
func (e Expr) NUnique() Expr { return e.agg(expr.AggNUnique) }

// First and Last are POSITIONAL and may return null.
//
// They are a selection rather than a reduction: skipping nulls would mean
// First(a) and First(b) could come from different rows, which is exactly what
// people use them together for.
func (e Expr) First() Expr { return e.agg(expr.AggFirst) }
func (e Expr) Last() Expr  { return e.agg(expr.AggLast) }

// NullCount counts the NULLS in each group — the complement of Count, and like the
// other counters it is never itself null.
func (e Expr) NullCount() Expr { return e.agg(expr.AggNullCount) }

// Any and AllTrue reduce a Boolean column.
//
// AllTrue rather than All, because All is already the top-level selector for every
// column and one name cannot mean both.
//
// Nulls are SKIPPED, so a group with no non-null value gives NULL rather than
// false (for Any) or true (for AllTrue). Polars returns the vacuous answer; ursus
// returns null, because every other aggregate here does and an inconsistent one is
// a permanent trap. FillNull makes either convention available.
func (e Expr) Any() Expr     { return e.agg(expr.AggAny) }
func (e Expr) AllTrue() Expr { return e.agg(expr.AggAllTrue) }

// Product multiplies the non-null values.
//
// Unlike Sum it returns FLOAT64 for every numeric input, including integers. Sum
// widened to Int128 so that overflow became unreachable; product cannot, because
// i128 has no multiplication — and an Int64 product wraps silently after roughly
// twenty ordinary factors. Float64 is exact to 2^53 and approximate above it, which
// is the weaker guarantee, stated rather than hidden.
//
// Product of a group with no non-null values is NULL, not 1, following Sum.
func (e Expr) Product() Expr { return e.agg(expr.AggProduct) }

// ArgMin and ArgMax give the POSITION of the smallest or largest value within the
// group, as a Uint64.
//
// The index counts every row of the group, nulls included, so it lines up with
// anything else gathered from the same group — but nulls can never win, and a group
// with no non-null value has no position and returns NULL.
//
// Ties go to the earliest row, which is what makes the answer independent of batch
// size and thread count.
func (e Expr) ArgMin() Expr { return e.agg(expr.AggArgMin) }
func (e Expr) ArgMax() Expr { return e.agg(expr.AggArgMax) }

// Var and Std are the variance and standard deviation of the non-null values.
//
// ddof is the delta degrees of freedom: 0 for the population statistic, 1 for the
// sample one. It has no default because there is no defensible default — Polars and
// pandas use 1, numpy uses 0, and silently picking either produces a number that is
// wrong for half its readers.
//
// A group with ddof or fewer values returns NULL: the sample variance of a single
// observation is undefined, and 0 would claim the data has no spread rather than
// that the question has no answer.
//
// Computed with Welford's algorithm, so a column of large near-equal values gives
// the right answer rather than the zero (or negative) that E[x²]−E[x]² produces.
func (e Expr) Var(ddof int) Expr { return e.varStd(expr.AggVar, ddof) }
func (e Expr) Std(ddof int) Expr { return e.varStd(expr.AggStd, ddof) }

func (e Expr) varStd(op expr.AggOp, ddof int) Expr {
	if ddof < 0 || ddof > 255 {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, op.String(),
			"ddof must be between 0 and 255, got %d", ddof).
			Hint("0 is the population statistic and 1 the sample one")})
	}
	return e.aggP(op, expr.AggParams{DDof: uint8(ddof)})
}

// Median is the 0.5 quantile with linear interpolation, so an even-sized group
// gives the mean of the two middle values.
//
// It returns Float64 for every numeric input, and skips nulls.
func (e Expr) Median() Expr { return e.agg(expr.AggMedian) }

// Quantile returns the value at rank q, where q runs from 0 (the minimum) to 1 (the
// maximum). interp decides what happens when the rank falls between two values.
//
// The rank is q·(n−1) over the non-null values — the convention numpy, Polars and
// DuckDB share — and the result is Float64 even for InterpLower and InterpHigher,
// which do return an actual input value.
//
// Values are ordered by ursus's TOTAL order, so NaN sorts above everything and a
// high quantile of a column containing NaN is NaN, exactly as Sort would place it.
func (e Expr) Quantile(q float64, interp Interpolation) Expr {
	if !(q >= 0 && q <= 1) { // written this way so NaN is rejected too
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "quantile",
			"q must be between 0 and 1, got %v", q)})
	}
	return e.aggP(expr.AggQuantile, expr.AggParams{Q: q, Interp: interp})
}

// Implode collects every value of the group into a List, in input order.
//
//	GroupBy(Col("k")).Agg(
//	    Col("v").Implode().Alias("all"),
//	    Col("v").Implode().List().Sort().List().Head(3).Alias("smallest3"),
//	)
//
// It is what makes the .list namespace available inside a group-by: everything
// list.go can do to a list, this can now do to a group.
//
// # Nulls are kept
//
// Every other aggregate skips them — the sum of an all-null group is null, not
// zero. Implode is a selection rather than a reduction, so a null is a value that
// takes a slot, and an all-null group implodes to a list OF nulls rather than to an
// empty list or a null one.
//
// # It makes the group-by single-threaded
//
// A list's element order is its data, and the parallel driver dispatches batches
// round-robin, so a shuffled input would be a different answer rather than the same
// one computed differently. The whole Agg list runs on one worker as a result —
// including the aggregates beside this one, because a query's aggregates share one
// hash table.
//
// # Its memory is the data
//
// Implode holds every value of every group, so a group-by containing one is O(rows)
// rather than O(distinct keys), and under WithMemoryLimit it refuses once a single
// key grows past the budget. Partitioning divides the key space and cannot divide
// one key.
func (e Expr) Implode() Expr { return e.agg(expr.AggImplode) }

func (e Expr) agg(op expr.AggOp) Expr {
	return wrap(&expr.Agg{Op: op, Child: e.n})
}

func (e Expr) aggP(op expr.AggOp, p expr.AggParams) Expr {
	return wrap(&expr.Agg{Op: op, Child: e.n, Params: p})
}

// Len counts rows in the current group — SQL's COUNT(*) with no column argument.
//
// It aggregates over a literal rather than a column, because counting rows must
// not depend on any column existing: `GroupBy(k).Agg(Len())` has to work on a frame
// whose only column is the key.
func Len() Expr { return Lit(int64(1)).Len() }

// --- sort keys ----------------------------------------------------------------

// SortKey is one component of an ordering. Build one with Asc or Desc.
type SortKey struct{ k plan.SortKey }

// Asc orders ascending. Nulls come FIRST by default; call NullsLast to move them.
func Asc(e Expr) SortKey {
	return SortKey{plan.SortKey{Expr: e.n}}
}

// Desc orders descending, with nulls still FIRST by default.
//
// Null placement is deliberately independent of direction. SQL ties them together
// — nulls sort as the largest value, so they land last ascending and first
// descending — which means switching to Desc silently relocates them. Keeping the
// two orthogonal is more predictable.
func Desc(e Expr) SortKey {
	return SortKey{plan.SortKey{Expr: e.n, Descending: true}}
}

// NullsFirst places nulls at the start of the output.
func (k SortKey) NullsFirst() SortKey {
	k.k.NullsLast = false
	return k
}

// NullsLast places nulls at the END of the output, whichever direction the key
// sorts in.
func (k SortKey) NullsLast() SortKey {
	k.k.NullsLast = true
	return k
}

// --- frame operations ---------------------------------------------------------

// WithColumns adds columns, replacing any that already exist BY NAME and keeping
// their original position.
//
// Later expressions can reference columns that earlier ones added, so
// `WithColumns(a.Alias("x"), Col("x").Mul(2))` works.
func (lf *LazyFrame) WithColumns(exprs ...Expr) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	if len(exprs) == 0 {
		return lf
	}
	ns, err := nodes(exprs)
	if err != nil {
		return &LazyFrame{err: err}
	}
	return lf.derive(&plan.WithColumns{Input: lf.node, Exprs: ns})
}

// Sort orders rows by one or more keys.
//
//	lf.Sort(ursus.Desc(ursus.Col("revenue")), ursus.Asc(ursus.Col("name")))
//
// The sort is STABLE, so ties keep their input order and a second Sort refines the
// first rather than discarding it.
func errNilSortKey(i int) error {
	return uerr.New(uerr.KindValue, "sort", "sort key %d is the zero SortKey", i).
		Hint("build keys with ursus.Asc(expr) or ursus.Desc(expr)")
}

func (lf *LazyFrame) Sort(keys ...SortKey) *LazyFrame {
	if lf.err != nil || len(keys) == 0 {
		return lf
	}
	ks := make([]plan.SortKey, len(keys))
	for i, k := range keys {
		if k.k.Expr == nil {
			return &LazyFrame{err: errNilSortKey(i)}
		}
		ks[i] = k.k
	}
	return lf.derive(&plan.Sort{Input: lf.node, Keys: ks})
}

// Unique removes duplicate rows.
//
// With no arguments it compares whole rows. With column names it compares only
// those, keeping the FIRST row for each distinct combination — so the other
// columns come from that row rather than being chosen arbitrarily.
func (lf *LazyFrame) Unique(subset ...string) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	var sub []string
	if len(subset) > 0 {
		sub = append([]string(nil), subset...)
	}
	return lf.derive(&plan.Distinct{Input: lf.node, Subset: sub})
}

// GroupBy begins an aggregation.
//
//	lf.GroupBy(ursus.Col("region")).
//	    Agg(ursus.Col("revenue").Sum(), ursus.Len().Alias("n"))
//
// With no keys it is a GLOBAL aggregate producing exactly one row — including over
// an empty input, where `count(*)` is 0 rather than no rows at all.
func (lf *LazyFrame) GroupBy(keys ...Expr) *GroupBy {
	if lf.err != nil {
		return &GroupBy{lf: lf}
	}
	ns, err := nodes(keys)
	if err != nil {
		return &GroupBy{lf: &LazyFrame{err: err}}
	}
	return &GroupBy{lf: lf, keys: ns}
}

// GroupBy is the intermediate handle between GroupBy and Agg.
type GroupBy struct {
	lf      *LazyFrame
	keys    []expr.Node
	ordered bool

	// temporal is set by GroupByDynamic and Rolling. When it is, Agg builds that
	// node instead of an Aggregate — which is what lets all thirteen shorthands
	// below work on a temporal group with no second implementation of any of them.
	temporal    *plan.TemporalGroup
	temporalErr error
}

// MaintainOrder emits groups in first-appearance order.
//
// Without it the group order is unspecified — which is standard (SQL uses set
// semantics and DuckDB exploits it for parallelism) but means a test that assumes
// an order is flaky.
//
// # The flag is no longer free, and that is the point of it
//
// It USED to document a guarantee the implementation happened to give anyway. Since
// step 12 a group-by over more distinct keys than the memory limit holds partitions
// the overflow to disk, and the unordered path then emits partition-major: resident
// groups first, then partition 0's groups, then partition 1's. The order depends on
// where the freeze fell, so it depends on the limit.
//
// The CONTENT never does. Every group, every value, identical at every limit. See
// WithMemoryLimit, which states the same contract from the other side.
//
// Costs nothing when the aggregation fits in memory. When it spills it adds an
// int64 ordinal to every routed row and materialises the whole result to permute it
// — plan.Aggregate.MaintainOrder has the detail.
func (g *GroupBy) MaintainOrder() *GroupBy {
	g.ordered = true
	return g
}

// Agg reduces each group.
//
// Every expression must be an aggregation: every column it reads has to pass
// through an aggregate function. `Col("a").Sum().Add(1)` is fine;
// `Col("a").Sum().Add(Col("b"))` is not, because b is still a per-row value and
// there is no defined way to combine the two shapes.
func (g *GroupBy) Agg(exprs ...Expr) *LazyFrame {
	if g.lf.err != nil {
		return g.lf
	}
	if g.temporalErr != nil {
		return &LazyFrame{err: g.temporalErr}
	}
	ns, err := nodes(exprs)
	if err != nil {
		return &LazyFrame{err: err}
	}
	if g.temporal != nil {
		t := *g.temporal
		t.Input, t.Aggs = g.lf.node, ns
		return g.lf.derive(&t)
	}
	return g.lf.derive(&plan.Aggregate{
		Input:         g.lf.node,
		Keys:          g.keys,
		Aggs:          ns,
		MaintainOrder: g.ordered,
	})
}

// Count is shorthand for Agg(Len().Alias("count")).
func (g *GroupBy) Count() *LazyFrame {
	return g.Agg(Len().Alias("count"))
}

// --- GroupBy shorthands ---------------------------------------------------------
//
// Each applies one aggregate to EVERY non-key column, which is what makes them
// worth having: `GroupBy(k).Sum()` over a forty-column frame is one call rather
// than forty.
//
// They are sugar over Agg, and deliberately so. The selector does the work —
// `All().Exclude(keys...)` expands at plan time against the real schema — so the
// shorthands inherit expansion, type checking and projection pushdown without any
// of them being reimplemented. A column the aggregate is not defined for is
// refused by name, the same as if it had been written out.

// keyNames returns the names the group keys read, which are the columns a
// shorthand must NOT aggregate.
//
// A computed key such as `GroupBy(Col("t").Dt().Year())` reads `t`, so `t` is
// excluded — aggregating a column the grouping already consumed would produce a
// column whose meaning depends on how the key was written.
func (g *GroupBy) keyNames() []string {
	var out []string
	seen := map[string]struct{}{}
	keys := g.keys
	if g.temporal != nil {
		// The INDEX is a key too for the purpose of the shorthands: aggregating the
		// column the windows were cut from would produce a column whose meaning
		// depends on how the grouping was written, which is the argument the
		// computed-key note below already makes.
		keys = append([]expr.Node{g.temporal.Index}, keys...)
	}
	for _, k := range keys {
		for _, n := range expr.RootNames(k) {
			if _, dup := seen[n]; !dup {
				seen[n] = struct{}{}
				out = append(out, n)
			}
		}
	}
	return out
}

// numericTypes is every type arithmetic is defined on. It exists because the
// selector API takes explicit types and there is no "numeric" family matcher.
var numericTypes = []DataType{
	dtype.Int8, dtype.Int16, dtype.Int32, dtype.Int64, dtype.Int128,
	dtype.Uint8, dtype.Uint16, dtype.Uint32, dtype.Uint64,
	dtype.Float32, dtype.Float64,
}

// each applies fn to the non-key columns, restricted to numeric ones when the
// aggregate needs them.
//
// # Why the numeric ones restrict rather than refuse
//
// `GroupBy(k).Sum()` on a frame with one string column would otherwise be an error,
// and almost every real frame has one — which would make the shorthand useless
// exactly where it is most wanted. Selecting the numeric columns is not a silent
// skip either: the selector is part of the expression, so Explain shows precisely
// which columns were summed.
//
// Min, Max, First, Last and NUnique take everything, because they select or count
// rather than compute and are defined for every type.
func (g *GroupBy) each(numericOnly bool, fn func(Expr) Expr) *LazyFrame {
	if g.lf.err != nil {
		return g.lf
	}
	var inner expr.Matcher = &expr.AllMatcher{}
	if numericOnly {
		inner = &expr.DTypeMatcher{Types: numericTypes}
	}
	sel := wrap(&expr.Match{M: &expr.ExcludeMatcher{Inner: inner, Names: g.keyNames()}})
	return g.Agg(fn(sel))
}

// Sum, Mean, Min, Max, Median, NUnique, First and Last apply that aggregate to
// every non-key column.
// Sum, Mean, Median and Quantile apply to the NUMERIC non-key columns.
func (g *GroupBy) Sum() *LazyFrame    { return g.each(true, Expr.Sum) }
func (g *GroupBy) Mean() *LazyFrame   { return g.each(true, Expr.Mean) }
func (g *GroupBy) Median() *LazyFrame { return g.each(true, Expr.Median) }

// Min, Max, First, Last and NUnique apply to EVERY non-key column, because they
// select or count rather than compute.
func (g *GroupBy) Min() *LazyFrame     { return g.each(false, Expr.Min) }
func (g *GroupBy) Max() *LazyFrame     { return g.each(false, Expr.Max) }
func (g *GroupBy) NUnique() *LazyFrame { return g.each(false, Expr.NUnique) }
func (g *GroupBy) First() *LazyFrame   { return g.each(false, Expr.First) }
func (g *GroupBy) Last() *LazyFrame    { return g.each(false, Expr.Last) }

// Quantile applies one quantile to every numeric non-key column.
func (g *GroupBy) Quantile(q float64, interp Interpolation) *LazyFrame {
	return g.each(true, func(e Expr) Expr { return e.Quantile(q, interp) })
}

// Len counts the rows in each group under the given name.
//
// Distinct from Count, which uses the fixed name "count": Len takes one because a
// frame that already has a column called "count" is not unusual, and silently
// colliding would be worse than asking.
func (g *GroupBy) Len(name string) *LazyFrame {
	return g.Agg(Len().Alias(name))
}
