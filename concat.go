package ursus

import (
	"ursus/internal/plan"
	"ursus/internal/uerr"
)

// ConcatMode says how much Concat will reconcile differing schemas.
type ConcatMode = plan.ConcatMode

const (
	// ConcatStrict requires the same columns in the same order — the default.
	//
	// Types still promote and nullability still widens. Neither is a relaxation:
	// refusing to stack two frames that differ only in a nullability flag would be
	// absurd, because that is the ordinary result of filtering one of them.
	ConcatStrict = plan.ConcatStrict

	// ConcatDiagonal takes the union of the columns, filling each frame's missing
	// ones with nulls. This is what makes stacking heterogeneous files work.
	ConcatDiagonal = plan.ConcatDiagonal
)

// ConcatOption configures Concat.
type ConcatOption func(*concatCfg)

type concatCfg struct{ mode ConcatMode }

// WithConcatMode selects strict or diagonal reconciliation.
func WithConcatMode(m ConcatMode) ConcatOption {
	return func(c *concatCfg) { c.mode = m }
}

// Concat stacks frames vertically, in the order given.
//
//	ursus.Concat([]*ursus.LazyFrame{jan, feb, mar})
//	ursus.Concat(fs, ursus.WithConcatMode(ursus.ConcatDiagonal))
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
	return lfs[0].derive(&plan.Union{Inputs: nodes, Mode: mode})
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
