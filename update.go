package ursus

import (
	"context"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// Update (step 316, J2 of v0.6.7-scope.md): Polars' DataFrame.update.

// UpdateHow is which rows an Update keeps.
type UpdateHow uint8

const (
	// UpdateLeft keeps every row of the frame, Polars' default.
	UpdateLeft UpdateHow = iota
	// UpdateInner keeps only the rows other matches.
	UpdateInner
	// UpdateFull keeps every row of the frame, and adds other's unmatched rows after
	// them, as new rows: null in the columns other does not have.
	UpdateFull
)

// UpdateOptions configures Update.
type UpdateOptions struct {
	// On names the key columns, alike on both frames. With On, LeftOn and RightOn all
	// empty, rows are matched by their places: the frame's first row with other's
	// first, as Polars' row count is.
	On []string

	// LeftOn and RightOn name keys named apart, paired by position.
	LeftOn, RightOn []string

	// How is which rows are kept: UpdateLeft, the zero value, UpdateInner or
	// UpdateFull.
	How UpdateHow

	// IncludeNulls writes other's nulls over the frame's values; by default a null in
	// other leaves the value.
	IncludeNulls bool

	// MaintainOrder is the rows' order. JoinOrderNone, the zero value, is the
	// frame's, as JoinOrderLeft is: Polars' default for update. JoinOrderRight is
	// other's, each of its rows' matches in the frame's order, and then the frame's
	// unmatched rows.
	MaintainOrder JoinOrder
}

// Update writes other's values over this frame's, in the columns both have, matched
// by keys, as Polars' DataFrame.update does:
//
//	prices.Update(corrections, ursus.UpdateOptions{On: []string{"sku"}})
//
// A null in other leaves the value unless IncludeNulls. A column of other the frame
// does not have is ignored, and the frame's columns keep their places. A column whose
// two types differ takes the type that holds both, Polars' supertype: an Int64
// updated with a Float64 is a Float64, and with text, text. A key other holds twice
// gives its frame row twice, as a join does.
//
// It is a join, ordered, and a column each: it streams and spills as a join does,
// and keeps its order under a memory limit.
func (lf *LazyFrame) Update(other *LazyFrame, o UpdateOptions) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	if other == nil {
		return &LazyFrame{err: uerr.New(uerr.KindValue, "update", "the frame to update from is nil")}
	}
	if other.err != nil {
		return &LazyFrame{err: other.err}
	}
	kind, ok := map[UpdateHow]JoinKind{UpdateLeft: JoinLeft, UpdateInner: JoinInner, UpdateFull: JoinFull}[o.How]
	if !ok {
		return &LazyFrame{err: uerr.New(uerr.KindValue, "update", "the update kind %d is not one of the three", o.How).
			Hint("UpdateLeft, UpdateInner or UpdateFull")}
	}
	order := o.MaintainOrder
	if order == JoinOrderNone {
		order = JoinOrderLeft
	}
	ls, err := lf.schemaNow()
	if err != nil {
		return &LazyFrame{err: err}
	}
	rs, err := other.schemaNow()
	if err != nil {
		return &LazyFrame{err: err}
	}

	left, right := lf, other
	leftOn, rightOn := o.LeftOn, o.RightOn
	switch {
	case len(o.On) > 0 && (len(o.LeftOn) > 0 || len(o.RightOn) > 0):
		return &LazyFrame{err: uerr.New(uerr.KindValue, "update",
			"On names the keys of both frames, so it cannot be given with LeftOn or RightOn")}
	case len(o.On) > 0:
		leftOn, rightOn = o.On, o.On
	case len(leftOn) != len(rightOn):
		return &LazyFrame{err: uerr.New(uerr.KindValue, "update",
			"LeftOn names %d keys and RightOn %d", len(leftOn), len(rightOn)).
			Hint("keys are paired by position")}
	}
	for _, name := range leftOn {
		if !ls.Has(name) {
			return &LazyFrame{err: uerr.UnknownColumn("update", name, ls.Names())}
		}
	}
	for _, name := range rightOn {
		if !rs.Has(name) {
			return &LazyFrame{err: uerr.UnknownColumn("update", name, rs.Names())}
		}
	}
	byPlace := len(leftOn) == 0
	if byPlace {
		// Matched by place: each frame's rows numbered, and joined on the number.
		row := freshName(append(ls.Names(), rs.Names()...), "row")
		left, right = lf.WithRowIndex(row, 0), other.WithRowIndex(row, 0)
		leftOn, rightOn = []string{row}, []string{row}
	}

	// The columns other updates: its own, not a key, that the frame has too; each
	// under a name of its own on other's side, with the type that holds both.
	isKey := map[string]bool{}
	for _, k := range leftOn {
		isKey[k] = true
	}
	isRightKey := map[string]bool{}
	for _, k := range rightOn {
		isRightKey[k] = true
	}
	taken := append(ls.Names(), rs.Names()...)
	cols := []Expr{}
	for _, k := range rightOn {
		cols = append(cols, Col(k))
	}
	hidden := map[string]string{}
	types := map[string]DataType{}
	for _, f := range rs.All() {
		if isRightKey[f.Name] || isKey[f.Name] || !ls.Has(f.Name) {
			continue
		}
		mine, _ := ls.ByName(f.Name)
		t, ok := dtype.Supertype(mine.Type, f.Type)
		if !ok {
			return &LazyFrame{err: uerr.New(uerr.KindType, "update",
				"column %q is %s in the frame and %s in other, which no type holds both of", f.Name, mine.Type, f.Type)}
		}
		h := freshName(taken, "update_"+f.Name)
		taken = append(taken, h)
		hidden[f.Name], types[f.Name] = h, t
		cols = append(cols, Col(f.Name).Alias(h))
	}
	matched := freshName(taken, "matched")
	cols = append(cols, Lit(true).Alias(matched))

	keys := func(names []string) []Expr {
		out := make([]Expr, len(names))
		for i, n := range names {
			out[i] = Col(n)
		}
		return out
	}
	joined := left.Join(right.Select(cols...), JoinLeftOn(keys(leftOn)...), JoinRightOn(keys(rightOn)...),
		JoinHow(kind), JoinCoalesce(true), JoinMaintainOrder(order))

	// A key's values are the frame's own but in a full update, so it keeps the
	// frame's type, which the join may have widened, as Polars' keeps it.
	var js *dtype.Schema
	if o.How != UpdateFull && !byPlace {
		if js, err = joined.schemaNow(); err != nil {
			return &LazyFrame{err: err}
		}
	}
	out := make([]Expr, 0, ls.Len())
	for _, f := range ls.All() {
		h, ok := hidden[f.Name]
		if !ok {
			key := Col(f.Name)
			if js != nil && isKey[f.Name] {
				if jf, _ := js.ByName(f.Name); jf.Type != f.Type {
					key = key.Cast(f.Type)
				}
			}
			out = append(out, key)
			continue
		}
		mine, theirs := Col(f.Name), Col(h)
		if t := types[f.Name]; t != f.Type {
			mine = mine.Cast(t)
		}
		if rf, _ := rs.ByName(f.Name); rf.Type != types[f.Name] {
			theirs = theirs.Cast(types[f.Name])
		}
		if o.IncludeNulls {
			// A matched row takes other's value, a null too.
			out = append(out, When(Col(matched).IsNotNull()).Then(theirs).Otherwise(mine).Alias(f.Name))
		} else {
			out = append(out, Coalesce(theirs, mine).Alias(f.Name))
		}
	}
	return joined.Select(out...)
}

// Update is a DataFrame's: LazyFrame's.
func (df *DataFrame) Update(ctx context.Context, other *DataFrame, o UpdateOptions) (*DataFrame, error) {
	return df.Lazy().Update(other.Lazy(), o).Collect(ctx)
}
