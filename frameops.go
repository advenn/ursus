package ursus

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/uerr"
)

// --- top-k --------------------------------------------------------------------

// TopK keeps the k LARGEST rows by the given keys, and BottomK the k smallest.
//
// # Both are sugar, and that is the point
//
// Each is `Sort(...).Head(k)`, which the limit-pushdown rule turns into a bounded
// top-k: kernel.ArgTopK is O(n log k) time and O(k) memory against a full sort's
// O(n log n) and O(n), and its contract is that it returns EXACTLY the indices
// ArgSort would, ties included. So there is no separate operator to keep in step
// with Sort, and `TopK(k)` and `Sort(...).Head(k)` cannot drift apart because they
// are the same plan.
//
// # Both place nulls LAST, unlike Sort
//
// Sort keeps direction and null placement orthogonal on purpose — "`.Desc()`
// silently relocating the nulls surprises people every time" — and that argument
// does NOT transfer here. In a sort, placement is cosmetic: every row comes back
// either way. In a top-k it is SELECTION, and a null has no rank, so leaving the
// default (nulls first) would make `TopK(2)` over [3,1,4,1,5,null] return the null
// and the 5 — a row with no value in the very column being ranked.
//
// So both force nulls last, and both say so. `Sort(...).Head(k)` remains available
// for a different placement, and it is one call away.
func (lf *LazyFrame) TopK(k int, by ...SortKey) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	if len(by) == 0 {
		return &LazyFrame{err: uerr.New(uerr.KindValue, "top_k",
			"TopK requires at least one sort key")}
	}
	flipped := make([]SortKey, len(by))
	for i, s := range by {
		s.k.Descending = !s.k.Descending
		s.k.NullsLast = true
		flipped[i] = s
	}
	return lf.Sort(flipped...).Head(k)
}

// BottomK keeps the k smallest rows by the given keys. See TopK — including that
// nulls are placed last and so never appear in the result.
func (lf *LazyFrame) BottomK(k int, by ...SortKey) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	if len(by) == 0 {
		return &LazyFrame{err: uerr.New(uerr.KindValue, "bottom_k",
			"BottomK requires at least one sort key")}
	}
	keys := make([]SortKey, len(by))
	for i, s := range by {
		s.k.NullsLast = true
		keys[i] = s
	}
	return lf.Sort(keys...).Head(k)
}

// --- shape --------------------------------------------------------------------

// Drop removes columns by name.
//
// Sugar over `Select(Exclude(...))`, which is the whole implementation: Exclude is
// already an expansion-time selector, so Drop inherits its error messages, its
// interaction with projection pushdown, and its behaviour on a name that is not
// there — the selector ignores it rather than failing, matching Polars.
func (lf *LazyFrame) Drop(names ...string) *LazyFrame {
	if lf.err != nil || len(names) == 0 {
		return lf
	}
	return lf.Select(Exclude(names...))
}

// Rename changes column names, leaving everything else in place.
//
// Sugar over `Select(All().MapName(...))`. MapName is the expansion-safe renamer —
// Alias sets ONE fixed name and so cannot apply to a multi-column selection, which
// is exactly why the two are different methods.
//
// Names not present in the frame are ignored rather than an error, so a rename map
// written against a wider schema still works. Renaming two columns onto the same
// name IS an error, caught by Select's output-name uniqueness check with both
// offending expressions named.
func (lf *LazyFrame) Rename(names map[string]string) *LazyFrame {
	if lf.err != nil || len(names) == 0 {
		return lf
	}
	return lf.Select(All().MapName(func(old string) string {
		if n, ok := names[old]; ok {
			return n
		}
		return old
	}))
}

// --- missing data -------------------------------------------------------------

// DropNulls removes rows that are null in any of the named columns, or in any
// column at all when no names are given.
//
// It is `Filter(Col(c).IsNotNull(), ...)`, and being a filter rather than a
// dedicated node is what earns it Parquet row-group pruning: the pruner claims
// IsNotNull over a bare column, so a row group whose statistics say a column is
// entirely null is skipped without being read.
//
// Note the asymmetry with a hypothetical DropNans: IsNotNull is TOTAL, so the
// predicate is never itself null and the filter's own null rule never comes into
// play. `Col(c).IsNotNan()` is null on a null row, and Filter drops null
// predicates, so the obvious spelling of DropNans would silently drop nulls too.
// That is why it is not here.
func (lf *LazyFrame) DropNulls(subset ...string) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	if len(subset) == 0 {
		return lf.Filter(wrap(&expr.Match{M: &expr.AllMatcher{}}).IsNotNull())
	}
	preds := make([]Expr, len(subset))
	for i, name := range subset {
		preds[i] = Col(name).IsNotNull()
	}
	return lf.Filter(preds...)
}

// Slice keeps length rows starting at offset. A negative length means "to the end",
// which is how you drop a prefix without knowing the height.
//
// Streaming: it counts rows past and stops, so it never holds more than one batch.
func (lf *LazyFrame) Slice(offset, length int) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	if offset < 0 {
		return &LazyFrame{err: uerr.New(uerr.KindValue, "slice",
			"offset must not be negative, got %d", offset).
			Hint("use Tail(n) to take rows from the end")}
	}
	return lf.derive(&plan.Slice{Input: lf.node, Offset: offset, Len: length})
}

// Explode turns each row's list into one row per element, repeating every other
// column.
//
//	id | tags        ->   id | tags
//	 1 | [10, 11]          1 | 10
//	 2 | []                1 | 11
//	 3 | null              2 | null
//	 4 | [12]              3 | null
//	                       4 | 12
//
// An empty list and a null list each produce ONE row holding a null, rather than
// vanishing. Dropping them would silently lower the height — which is only
// visible to someone who checks it — and would make two columns disagree about
// how many rows a pair of empty lists gives. It is also what polars does.
//
// The consequence is worth knowing: **explode erases the difference between an
// empty list and a null list.** A List column keeps those apart deliberately;
// after this they are the same row. The distinction belongs to the list, and the
// list is what explode consumes.
//
// Several columns explode TOGETHER — zipped, not multiplied. Exploding twice in
// sequence would give the cross product instead, so the lists must be the same
// length in every row, and a row where they disagree is an error rather than a
// truncation.
func (lf *LazyFrame) Explode(names ...string) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	if len(names) == 0 {
		return &LazyFrame{err: uerr.New(uerr.KindValue, "explode",
			"explode needs at least one column").
			Hint("there is no sensible default: exploding every list column at " +
				"once would zip columns the caller never said were parallel")}
	}
	return lf.derive(&plan.Explode{Input: lf.node, Columns: names})
}

// Unnest replaces a struct column with its fields, in place.
//
//	id | person                  ->   id | age  | city
//	 1 | {age: 30, city: "NY"}         1 | 30   | "NY"
//	 2 | {age: null, city: null}       2 | null | null
//	 3 | null                          3 | null | null
//
// It is to a struct what Explode is to a list: afterwards the nested column is gone
// and the fields are ordinary columns, so every filter, aggregate, join and sort
// applies to them directly rather than through `.Struct().Field(...)`.
//
// The fields land where the struct was and keep their OWN names. A name that would
// collide with a column already in the frame is refused, naming both — renaming a
// field the caller cannot see yet is not something this can guess at.
//
// **Unnest erases the difference between a null struct and a struct of nulls**, the
// way Explode erases the difference between a null list and an empty one (rows 2 and
// 3 above). The distinction belongs to the struct, and this is what consumes it —
// `Col("person").IsNull()` is how to keep it, before unnesting.
//
// At least one column must be named. "Every struct column" is well defined here,
// unlike for Explode, but it would silently widen the frame the day a file grows a
// struct.
func (lf *LazyFrame) Unnest(names ...string) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	if len(names) == 0 {
		return &LazyFrame{err: uerr.New(uerr.KindValue, "unnest",
			"unnest needs at least one column").
			Hint("naming them keeps the frame's width a property of the query " +
				"rather than of whatever the file happens to contain")}
	}
	return lf.derive(&plan.Unnest{Input: lf.node, Columns: names})
}

// UnpivotOptions configures Unpivot.
//
// On is required; everything else has a working default, so the common call is
// UnpivotOptions{On: []string{...}}.
type UnpivotOptions struct {
	// On names the value columns to melt into the variable/value pair.
	On []string

	// Index names the columns carried through unchanged. Empty means every column
	// that is not in On.
	Index []string

	// VariableName and ValueName name the two invented columns. Empty means
	// "variable" and "value", which are Polars' names.
	VariableName string
	ValueName    string
}

// Unpivot turns several value columns into two: which column, and what it held.
//
//	sales.Unpivot(ursus.UnpivotOptions{On: []string{"jan", "feb"}})
//
//	id  jan  feb            id  variable  value
//	1   10   20     ->      1   jan       10
//	2   30   40             1   feb       20
//	                        2   jan       30
//	                        2   feb       40
//
// Known elsewhere as melt. Every melted column shares one value column, so their
// types must promote to a common one — an Int32 beside an Int64 gives an Int64, and
// two types with no common one is an error naming both.
//
// # The row order is not Polars'
//
// Polars emits every row's first variable, then every row's second. This emits
// every variable for one row, then the next row.
//
// The reason is that ursus streams: unpivot transforms one batch at a time, so
// Polars' order would interleave differently at different batch sizes — the row
// order would depend on how the query ran rather than on what it asked for. Sort
// afterwards if a particular order matters.
//
// # Index defaults; On does not
//
// Leaving Index empty keeps every column that is not melted, so adding a column to
// the source keeps it. Unnest refuses the equivalent default for the opposite
// reason: there, a new column would silently start being CONSUMED.
func (lf *LazyFrame) Unpivot(opts UnpivotOptions) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	if len(opts.On) == 0 {
		return &LazyFrame{err: uerr.New(uerr.KindValue, "unpivot",
			"unpivot needs at least one column to melt").
			Hint("On names the columns that become variable/value pairs").
			Hint("Index may be left empty; it defaults to everything else")}
	}
	return lf.derive(&plan.Unpivot{
		Input:        lf.node,
		On:           opts.On,
		Index:        opts.Index,
		VariableName: opts.VariableName,
		ValueName:    opts.ValueName,
	})
}

// Tail keeps the last n rows.
//
// Unlike Head it cannot stream to completion: which rows are the last n is unknown
// until the input ends, so it holds a ring of the most recent n. Bounded — O(n),
// not O(input) — but not free.
func (lf *LazyFrame) Tail(n int) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	if n < 0 {
		n = 0
	}
	return lf.derive(&plan.Tail{Input: lf.node, N: n})
}

// Reverse emits rows in the opposite order.
//
// A full pipeline breaker — the last input row is the first output row — so it
// holds the whole frame. It does no comparison, which is what makes it cheaper
// than sorting by a synthesised descending index.
func (lf *LazyFrame) Reverse() *LazyFrame {
	if lf.err != nil {
		return lf
	}
	return lf.derive(&plan.Reverse{Input: lf.node})
}

// WithRowIndex prepends a Uint32 column numbering the rows from offset.
//
// Row order is a property rather than an addressable label space, which is why
// there is no implicit index — this is how you ask for one when you want it.
//
// It is a counter that crosses batch boundaries, so it depends on batches arriving
// in input order, and it is deliberately not the same thing as
// `CumCount(false).Over()`: that computes identical numbers through the window
// sink, which buffers the entire frame to do it.
func (lf *LazyFrame) WithRowIndex(name string, offset uint32) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	return lf.derive(&plan.RowIndex{Input: lf.node, Name: name, Offset: offset})
}

// FillNull replaces nulls in the named columns, or in every column the value can
// fill when no subset is given.
//
//	lf.FillNull(0)                  // every numeric column
//	lf.FillNull(0, "qty", "amount") // exactly these
//
// # Why the no-subset form restricts rather than refuses
//
// Almost every real frame has a string column, and `lf.FillNull(0)` erroring on it
// would make the shorthand useless exactly where it is most wanted. So the value's
// own type chooses the columns — the same argument GroupBy(k).Sum()'s doc makes
// for restricting to numeric columns, and with the same consequence: it is not a
// silent skip, because the selector is part of the expression and Explain shows
// precisely which columns were filled.
//
// A named column whose type the value cannot fill is a plan-time error, because
// the user asked for it by name.
//
// The value is a WEAK literal, so filling an Int32 column with 0 leaves it Int32.
func (lf *LazyFrame) FillNull[T Operand](v T, subset ...string) *LazyFrame {
	return lf.fillEach(subset, fillTargets(v), func(e Expr) Expr { return e.FillNullWith(v) })
}

// FillNan replaces NaN in the named columns, or in every FLOAT column when no
// subset is given. Nulls are left alone — see Expr.FillNan.
func (lf *LazyFrame) FillNan[T Operand](v T, subset ...string) *LazyFrame {
	floats := []DataType{dtype.Float32, dtype.Float64}
	return lf.fillEach(subset, floats, func(e Expr) Expr { return e.FillNan(v) })
}

// fillEach applies fn to the named columns, or to every column of one of the given
// types when no names are supplied.
func (lf *LazyFrame) fillEach(subset []string, types []DataType, fn func(Expr) Expr) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	if len(subset) == 0 {
		var m expr.Matcher = &expr.AllMatcher{}
		if len(types) > 0 {
			m = &expr.DTypeMatcher{Types: types}
		}
		return lf.WithColumns(fn(wrap(&expr.Match{M: m})))
	}
	cols := make([]Expr, len(subset))
	for i, name := range subset {
		cols[i] = fn(Col(name))
	}
	return lf.WithColumns(cols...)
}

// fillTargets picks the column types a fill value can be applied to.
//
// Derived from the LITERAL's type rather than from a hand-maintained list per
// method: an integer or float fills the numeric columns, a string fills String, a
// bool fills Bool. An Expr operand has no type until plan time, so it falls through
// to every column and any mismatch is reported there.
//
// Note that numericTypes omits Decimal — deliberately, and for the reason its own
// comment gives — so `lf.FillNull(0)` skips a Decimal column rather than failing.
// Naming the column applies it and produces the plan-time refusal instead.
func fillTargets[T Operand](v T) []DataType {
	l, ok := litNode(any(v)).(*expr.Lit)
	if !ok {
		return nil // an Expr, or a value litNode refused: fall back to every column
	}
	switch {
	case l.DT.IsNumeric():
		return numericTypes
	case l.DT.IsString():
		return []DataType{dtype.String}
	case l.DT.IsBool():
		return []DataType{dtype.Bool}
	default:
		return []DataType{l.DT}
	}
}
