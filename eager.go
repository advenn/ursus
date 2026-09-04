package ursus

import (
	"context"

	"github.com/advenn/ursus/internal/source/memsrc"
)

// Lazy turns a materialised frame back into a query.
//
// # The obvious spelling is wrong in three specific ways
//
// `ursus.Frame(df.Batch().Columns()...)` compiles and runs, because Column is a
// type alias — and it is wrong:
//
//   - It would re-derive nullability from the data. Field.Nullable is a static
//     property of the schema, not a count of nulls actually present, so a nullable
//     column that happens to hold none — the ordinary result of a filter — would
//     come back non-nullable and every downstream join and cast would reason from
//     the wrong schema.
//   - It would lose the row count of a frame with rows but no columns.
//   - It would alias the batch's own column slice, against Columns()' contract.
//
// So this goes through the batch, which already carries the declared schema and the
// row count.
//
// It is free: no copy, no re-derivation. The batch is immutable and shared.
func (df *DataFrame) Lazy() *LazyFrame {
	src, err := memsrc.FromBatch(df.batch)
	if err != nil {
		return &LazyFrame{err: err}
	}
	return Scan(src)
}

// --- eager mirrors --------------------------------------------------------------
//
// Each is Lazy().<op>().Collect(ctx). One implementation, not two: there is no
// separate eager engine to keep in step, so an eager result cannot disagree with
// the lazy one.
//
// They take a context, unlike the sketch in the API notes, which used
// context.Background(). That sketch contradicts the same document's rule that a
// context appears at every execution boundary — and these ARE execution boundaries,
// which is exactly what distinguishes them from the lazy builders.

// Select evaluates expressions and returns the result.
func (df *DataFrame) Select(ctx context.Context, exprs ...Expr) (*DataFrame, error) {
	return df.Lazy().Select(exprs...).Collect(ctx)
}

// Filter keeps rows where every predicate is true.
func (df *DataFrame) Filter(ctx context.Context, preds ...Expr) (*DataFrame, error) {
	return df.Lazy().Filter(preds...).Collect(ctx)
}

// WithColumns adds or replaces columns.
func (df *DataFrame) WithColumns(ctx context.Context, exprs ...Expr) (*DataFrame, error) {
	return df.Lazy().WithColumns(exprs...).Collect(ctx)
}

// Sort orders rows.
func (df *DataFrame) Sort(ctx context.Context, keys ...SortKey) (*DataFrame, error) {
	return df.Lazy().Sort(keys...).Collect(ctx)
}

// Head keeps the first n rows; Tail the last n.
func (df *DataFrame) Head(ctx context.Context, n int) (*DataFrame, error) {
	return df.Lazy().Head(n).Collect(ctx)
}

func (df *DataFrame) Tail(ctx context.Context, n int) (*DataFrame, error) {
	return df.Lazy().Tail(n).Collect(ctx)
}

// Drop removes columns; Rename changes their names.
func (df *DataFrame) Drop(ctx context.Context, names ...string) (*DataFrame, error) {
	return df.Lazy().Drop(names...).Collect(ctx)
}

func (df *DataFrame) Rename(ctx context.Context, names map[string]string) (*DataFrame, error) {
	return df.Lazy().Rename(names).Collect(ctx)
}

// Unique removes duplicate rows.
func (df *DataFrame) Unique(ctx context.Context, subset ...string) (*DataFrame, error) {
	return df.Lazy().Unique(subset...).Collect(ctx)
}

// Reverse emits rows in the opposite order.
func (df *DataFrame) Reverse(ctx context.Context) (*DataFrame, error) {
	return df.Lazy().Reverse().Collect(ctx)
}

// Join combines two frames on keys.
func (df *DataFrame) Join(ctx context.Context, other *DataFrame, opts ...JoinOption) (*DataFrame, error) {
	return df.Lazy().Join(other.Lazy(), opts...).Collect(ctx)
}

// Concat stacks frames vertically.
func (df *DataFrame) Concat(ctx context.Context, others ...*DataFrame) (*DataFrame, error) {
	lfs := make([]*LazyFrame, len(others))
	for i, o := range others {
		lfs[i] = o.Lazy()
	}
	return df.Lazy().Concat(lfs...).Collect(ctx)
}
