package ursus

import (
	"context"

	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/uerr"
)

// KeepStrategy is which of a key's rows UniqueKeep keeps: Polars' unique(keep=).
type KeepStrategy uint8

const (
	// KeepFirst keeps each key's first row; it is what Unique does.
	KeepFirst KeepStrategy = iota
	// KeepLast keeps each key's last row.
	KeepLast
	// KeepAny keeps one of each key's rows. Here it is the first, as Unique keeps
	// it; Polars' is whichever its hash table meets first.
	KeepAny
	// KeepNone keeps only the rows whose key no other row has.
	KeepNone
)

// UniqueKeep removes duplicate rows as Unique does, keeping the row keep says of each
// key: Polars' unique(subset, keep, maintain_order=True) (step 289). The kept rows
// are in their own order, so KeepLast of a key whose rows are 0 and 2 keeps row 2
// where row 2 was. With no subset a key is a whole row.
//
// Keys are compared as Unique and GroupBy compare them: a null with a null, NaN with
// NaN, and -0 with 0, as Polars does.
//
// KeepFirst and KeepAny are Unique, which streams, emitting a key's first row as it
// meets it. KeepLast and KeepNone cannot know a row is kept until its key's rows have
// all been read, so they are a filter by a window over the keys: a key's last row is
// the one a reversed CumCount numbers 1, and a key seen once the one whose partition's
// Len is 1. The window spills its partitions past a memory limit, as Over does.
func (lf *LazyFrame) UniqueKeep(keep KeepStrategy, subset ...string) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	switch keep {
	case KeepFirst, KeepAny:
		return lf.Unique(subset...)
	case KeepLast, KeepNone:
	default:
		return &LazyFrame{err: uerr.New(uerr.KindValue, "unique", "%d is not a KeepStrategy", keep).
			Hint("KeepFirst, KeepLast, KeepAny or KeepNone")}
	}
	names := subset
	if len(names) == 0 {
		resolved, err := plan.Resolve(context.Background(), lf.node)
		if err != nil {
			return &LazyFrame{err: err}
		}
		s, err := resolved.Schema()
		if err != nil {
			return &LazyFrame{err: err}
		}
		if names = s.Names(); len(names) == 0 {
			return lf
		}
	}
	keys := make([]Expr, len(names))
	for i, n := range names {
		keys[i] = Col(n)
	}
	kept := Len().Over(keys...).Eq(int64(1))
	if keep == KeepLast {
		kept = keys[0].CumCount(true).Over(keys...).Eq(int64(1))
	}
	return lf.Filter(kept)
}
