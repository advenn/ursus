package ursus

import (
	"context"
	"strconv"

	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/uerr"
)

// ConcatMode says how Concat puts frames together: Polars' concat(how=...), its
// eleven modes, "align" and "align_full" being one.
type ConcatMode uint8

const (
	// ConcatStrict stacks frames of the same columns in the same order — the
	// default, Polars' "vertical".
	//
	// Types still promote and nullability still widens. Neither is a relaxation:
	// refusing to stack two frames that differ only in a nullability flag would be
	// absurd, because that is the ordinary result of filtering one of them. Types
	// meet exactly, an Int32 beside an Int64 at Int64, where Polars' vertical
	// refuses them; an Int64 beside a Float64 is refused, which no type holds.
	ConcatStrict ConcatMode = iota

	// ConcatDiagonal takes the union of the columns, filling each frame's missing
	// ones with nulls: Polars' "diagonal". This is what makes stacking
	// heterogeneous files work.
	ConcatDiagonal

	// ConcatRelaxed and ConcatDiagonalRelaxed meet a column's types at Polars'
	// supertype, as its "vertical_relaxed" and "diagonal_relaxed": an Int64 beside a
	// Float64 at Float64, which may round; text beside a number, a Boolean or an
	// instant at text; a Boolean beside a number at the number; a Date beside a
	// Datetime at the Datetime.
	ConcatRelaxed
	ConcatDiagonalRelaxed

	// ConcatHorizontal places frames side by side, of one height, as HStack does:
	// Polars' "horizontal". ConcatHorizontalExtend pads a shorter frame's columns
	// with nulls: its "horizontal_extend".
	ConcatHorizontal
	ConcatHorizontalExtend

	// ConcatAlign joins frames on the columns every frame has, a full join, sorted
	// by them, as Polars' "align" and "align_full"; ConcatAlignInner,
	// ConcatAlignLeft and ConcatAlignRight join so. A column in some frames but not
	// every one is refused, as Polars' docs have it; Polars 1.44.1 keeps the first
	// frame's.
	ConcatAlign
	ConcatAlignInner
	ConcatAlignLeft
	ConcatAlignRight

	concatModes
)

var concatModeNames = [concatModes]string{
	ConcatStrict: "vertical", ConcatDiagonal: "diagonal", ConcatRelaxed: "vertical_relaxed",
	ConcatDiagonalRelaxed: "diagonal_relaxed", ConcatHorizontal: "horizontal",
	ConcatHorizontalExtend: "horizontal_extend", ConcatAlign: "align", ConcatAlignInner: "align_inner",
	ConcatAlignLeft: "align_left", ConcatAlignRight: "align_right",
}

// String is the mode's name in Polars' concat, "vertical" for ConcatStrict.
func (m ConcatMode) String() string {
	if m < concatModes {
		return concatModeNames[m]
	}
	return "ConcatMode(" + strconv.Itoa(int(m)) + ")"
}

// ConcatOption configures Concat.
type ConcatOption func(*concatCfg)

type concatCfg struct{ mode ConcatMode }

// WithConcatMode selects how Concat puts its frames together.
func WithConcatMode(m ConcatMode) ConcatOption {
	return func(c *concatCfg) { c.mode = m }
}

// Concat stacks frames vertically, in the order given, or, in the horizontal and
// align modes, puts them side by side: Polars' concat.
//
//	ursus.Concat([]*ursus.LazyFrame{jan, feb, mar})
//	ursus.Concat(fs, ursus.WithConcatMode(ursus.ConcatDiagonal))
//	ursus.Concat([]*ursus.LazyFrame{prices, volumes}, ursus.WithConcatMode(ursus.ConcatAlign))
//
// # Why a slice rather than a variadic
//
// It was `Concat(frames ...any)` for one release, which read better and let the
// mode be passed inline. It also made `ursus.Concat(a, "oops")` compile and fail
// at run time — in a library that spends generic methods and the Operand
// constraint precisely so that `Col("x").Gt(struct{}{})` does not. Ergonomics is
// not worth the one property the type system was being paid to provide.
//
// For the common case the method form keeps both: LazyFrame.Concat is variadic
// and type-safe, so `jan.Concat(feb, mar)` needs no slice literal.
//
// # Order is preserved
//
// Every row of the first frame precedes every row of the second. That is not free —
// it is why the operator drains children serially rather than racing them — and it
// is what lets Concat compose with Head, Tail and anything else that cares which
// rows come first.
//
// # Reconciliation happens in the plan
//
// A frame missing a column, or holding one at a narrower type, is wrapped in a
// projection that supplies the null and performs the cast, so Explain shows why a
// column is null and projection pushdown still narrows each input independently.
func Concat(frames []*LazyFrame, opts ...ConcatOption) *LazyFrame {
	cfg := concatCfg{mode: ConcatStrict}
	for _, o := range opts {
		o(&cfg)
	}
	return concatOf(frames, cfg.mode)
}

// ConcatFrames is Concat of DataFrames, collected.
func ConcatFrames(ctx context.Context, frames []*DataFrame, opts ...ConcatOption) (*DataFrame, error) {
	lfs := make([]*LazyFrame, len(frames))
	for i, f := range frames {
		if f == nil {
			return nil, uerr.New(uerr.KindValue, "concat", "frame %d is nil", i)
		}
		lfs[i] = f.Lazy()
	}
	return Concat(lfs, opts...).Collect(ctx)
}

// Concat stacks other frames beneath this one.
func (lf *LazyFrame) Concat(others ...*LazyFrame) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	return concatOf(append([]*LazyFrame{lf}, others...), ConcatStrict)
}

// VStack is Concat for exactly two frames, named for the operation people look for.
//
// It is not cheaper than Concat. Polars documents vstack as "cheap (adds a chunk)",
// which relies on a chunked column layout ursus does not have — a Column here is one
// contiguous run, so stacking copies. Naming it the same and pretending otherwise
// would be the misleading part.
func (lf *LazyFrame) VStack(other *LazyFrame) *LazyFrame { return lf.Concat(other) }

func concatOf(lfs []*LazyFrame, mode ConcatMode) *LazyFrame {
	if len(lfs) == 0 {
		return &LazyFrame{err: uerr.New(uerr.KindValue, "concat",
			"concat requires at least one frame")}
	}
	nodes := make([]plan.Node, len(lfs))
	for i, f := range lfs {
		if f == nil {
			return &LazyFrame{err: uerr.New(uerr.KindValue, "concat",
				"frame %d is nil", i)}
		}
		if f.err != nil {
			return &LazyFrame{err: f.err}
		}
		nodes[i] = f.node
	}
	if len(nodes) == 1 {
		return lfs[0]
	}
	switch mode {
	case ConcatStrict:
		return lfs[0].derive(&plan.Union{Inputs: nodes, Mode: plan.ConcatStrict})
	case ConcatDiagonal:
		return lfs[0].derive(&plan.Union{Inputs: nodes, Mode: plan.ConcatDiagonal})
	case ConcatRelaxed:
		return lfs[0].derive(&plan.Union{Inputs: nodes, Mode: plan.ConcatRelaxed})
	case ConcatDiagonalRelaxed:
		return lfs[0].derive(&plan.Union{Inputs: nodes, Mode: plan.ConcatDiagonalRelaxed})
	case ConcatHorizontal, ConcatHorizontalExtend:
		return lfs[0].derive(&plan.HStack{Inputs: nodes, Extend: mode == ConcatHorizontalExtend})
	case ConcatAlign:
		return alignOf(lfs, JoinFull)
	case ConcatAlignInner:
		return alignOf(lfs, JoinInner)
	case ConcatAlignLeft:
		return alignOf(lfs, JoinLeft)
	case ConcatAlignRight:
		return alignOf(lfs, JoinRight)
	}
	return &LazyFrame{err: uerr.New(uerr.KindValue, "concat", "no concat mode %s", mode).
		Hint("one of ursus.ConcatStrict, ConcatDiagonal, ConcatRelaxed, ... ConcatAlignRight")}
}

// alignOf joins frames on the columns every frame has, kind each join, as Polars'
// concat(how="align...") does (step 307), and sorts the rows by those columns. Its
// columns are every frame's, in the order each first appears. Ties, rows of one key,
// are in the order of the first frame's rows, then the second's: a row a frame has
// none for, first. So the order holds under a memory limit too, where a join's is
// not promised; Polars' is the joins' own, "right_left".
func alignOf(lfs []*LazyFrame, kind JoinKind) *LazyFrame {
	var order []string
	where := make(map[string][]int)
	for i, lf := range lfs {
		s, err := lf.schemaNow()
		if err != nil {
			return &LazyFrame{err: err}
		}
		for _, name := range s.Names() {
			if where[name] == nil {
				order = append(order, name)
			}
			where[name] = append(where[name], i)
		}
	}
	var keys []Expr
	for _, name := range order {
		switch w := where[name]; {
		case len(w) == len(lfs):
			keys = append(keys, Col(name))
		case len(w) > 1:
			// Polars 1.44.1 suffixes it and then selects it away: the first frame's
			// values are kept and the others' lost, though its docs say it raises.
			return &LazyFrame{err: uerr.New(uerr.KindSchema, "concat",
				"column %q is in frames %d and %d but not in every frame: align joins on the columns every frame has, and would have two of it",
				name, w[0], w[1]).
				Hint("rename it in one frame, or drop it from one")}
		}
	}
	if len(keys) == 0 {
		// Polars: 'align' strategy requires at least one common column.
		return &LazyFrame{err: uerr.New(uerr.KindSchema, "concat",
			"align joins frames on the columns every frame has, and they have none in common").
			Hint("name a key column alike in every frame")}
	}

	// Each frame's rows numbered, for ties, by names no frame has.
	taken := append([]string(nil), order...)
	sorts := make([]SortKey, 0, len(keys)+len(lfs))
	for _, k := range keys {
		sorts = append(sorts, Asc(k))
	}
	var out *LazyFrame
	for i, lf := range lfs {
		row := freshName(taken, "row"+strconv.Itoa(i))
		taken = append(taken, row)
		sorts = append(sorts, Asc(Col(row)))
		numbered := lf.WithRowIndex(row, 0)
		if out == nil {
			out = numbered
			continue
		}
		out = out.Join(numbered, JoinOn(keys...), JoinHow(kind), JoinCoalesce(true))
	}
	cols := make([]Expr, len(order))
	for i, name := range order {
		cols[i] = Col(name)
	}
	return out.Sort(sorts...).Select(cols...)
}

// HStack places frames side by side: the same rows, with the columns concatenated.
//
// # It buffers, and Concat does not
//
// Nothing in the engine aligns batch boundaries across independent pipelines — one
// frame may deliver 8192 rows at a time while another delivers 100 — and pairing
// row i of each means having row i of each in hand. So every input is materialised.
// Concat, which appends rows rather than pairing them, streams.
//
// Every frame must have the same height, and column names must not collide. There
// is no automatic suffixing: a join suffixes because it has a principled left and
// right to name the suffix after, and these inputs are peers.
func (lf *LazyFrame) HStack(others ...*LazyFrame) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	all := append([]*LazyFrame{lf}, others...)
	nodes := make([]plan.Node, len(all))
	for i, f := range all {
		if f == nil {
			return &LazyFrame{err: uerr.New(uerr.KindValue, "hstack",
				"frame %d is nil", i)}
		}
		if f.err != nil {
			return &LazyFrame{err: f.err}
		}
		nodes[i] = f.node
	}
	if len(nodes) == 1 {
		return lf
	}
	return lf.derive(&plan.HStack{Inputs: nodes})
}
