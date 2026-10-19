package ursus

import (
	"context"

	"github.com/advenn/ursus/internal/source/memsrc"
)

// Lazy turns a materialised frame back into a query.
//
// # The obvious spelling is wrong in three specific ways
//
// `ursus.Frame(df.batch.Columns()...)` compiles and runs, because Column is a
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
	return scan(src)
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

// UniqueKeep removes duplicate rows, keeping the row keep says of each key.
func (df *DataFrame) UniqueKeep(ctx context.Context, keep KeepStrategy, subset ...string) (*DataFrame, error) {
	return df.Lazy().UniqueKeep(keep, subset...).Collect(ctx)
}

// Reverse emits rows in the opposite order.
func (df *DataFrame) Reverse(ctx context.Context) (*DataFrame, error) {
	return df.Lazy().Reverse().Collect(ctx)
}

// Join combines two frames on keys.
func (df *DataFrame) Join(ctx context.Context, other *DataFrame, opts ...JoinOption) (*DataFrame, error) {
	return df.Lazy().Join(other.Lazy(), opts...).Collect(ctx)
}

// JoinWhere joins every pair of rows that satisfies the predicates.
func (df *DataFrame) JoinWhere(ctx context.Context, other *DataFrame, preds ...Expr) (*DataFrame, error) {
	return df.Lazy().JoinWhere(other.Lazy(), preds...).Collect(ctx)
}

// WhereExists keeps rows with at least one satisfying partner in other.
func (df *DataFrame) WhereExists(ctx context.Context, other *DataFrame, preds ...Expr) (*DataFrame, error) {
	return df.Lazy().WhereExists(other.Lazy(), preds...).Collect(ctx)
}

// WhereNotExists keeps rows with no satisfying partner in other.
func (df *DataFrame) WhereNotExists(ctx context.Context, other *DataFrame, preds ...Expr) (*DataFrame, error) {
	return df.Lazy().WhereNotExists(other.Lazy(), preds...).Collect(ctx)
}

// Unpivot turns several value columns into a variable/value pair.
func (df *DataFrame) Unpivot(ctx context.Context, opts UnpivotOptions) (*DataFrame, error) {
	return df.Lazy().Unpivot(opts).Collect(ctx)
}

// Concat stacks frames vertically.
func (df *DataFrame) Concat(ctx context.Context, others ...*DataFrame) (*DataFrame, error) {
	lfs := make([]*LazyFrame, len(others))
	for i, o := range others {
		lfs[i] = o.Lazy()
	}
	return df.Lazy().Concat(lfs...).Collect(ctx)
}

// Slice keeps length rows from offset; a negative length keeps to the end.
func (df *DataFrame) Slice(ctx context.Context, offset, length int) (*DataFrame, error) {
	return df.Lazy().Slice(offset, length).Collect(ctx)
}

// Limit keeps the first n rows, as Head does.
func (df *DataFrame) Limit(ctx context.Context, n int) (*DataFrame, error) {
	return df.Lazy().Limit(n).Collect(ctx)
}

// DropNulls removes rows with a null in subset, or in any column.
func (df *DataFrame) DropNulls(ctx context.Context, subset ...string) (*DataFrame, error) {
	return df.Lazy().DropNulls(subset...).Collect(ctx)
}

// FillNull fills the nulls of subset's columns, or every column v fits, with v.
func (df *DataFrame) FillNull[T Operand](ctx context.Context, v T, subset ...string) (*DataFrame, error) {
	return df.Lazy().FillNull(v, subset...).Collect(ctx)
}

// FillNan fills the NaNs of subset's float columns, or every float column, with v.
func (df *DataFrame) FillNan[T Operand](ctx context.Context, v T, subset ...string) (*DataFrame, error) {
	return df.Lazy().FillNan(v, subset...).Collect(ctx)
}

// TopK keeps the k rows with the largest keys and BottomK the k with the smallest,
// a null key last in both.
func (df *DataFrame) TopK(ctx context.Context, k int, by ...SortKey) (*DataFrame, error) {
	return df.Lazy().TopK(k, by...).Collect(ctx)
}

func (df *DataFrame) BottomK(ctx context.Context, k int, by ...SortKey) (*DataFrame, error) {
	return df.Lazy().BottomK(k, by...).Collect(ctx)
}

// HStack places frames side by side; VStack stacks one under this one.
func (df *DataFrame) HStack(ctx context.Context, others ...*DataFrame) (*DataFrame, error) {
	lfs := make([]*LazyFrame, len(others))
	for i, o := range others {
		lfs[i] = o.Lazy()
	}
	return df.Lazy().HStack(lfs...).Collect(ctx)
}

func (df *DataFrame) VStack(ctx context.Context, other *DataFrame) (*DataFrame, error) {
	return df.Lazy().VStack(other.Lazy()).Collect(ctx)
}

// JoinAsOf joins each row to the nearest row of other by an ordered key.
func (df *DataFrame) JoinAsOf(ctx context.Context, other *DataFrame, opts ...AsOfOption) (*DataFrame, error) {
	return df.Lazy().JoinAsOf(other.Lazy(), opts...).Collect(ctx)
}

// MergeSorted merges two frames sorted by key into one sorted by it.
func (df *DataFrame) MergeSorted(ctx context.Context, other *DataFrame, key string) (*DataFrame, error) {
	return df.Lazy().MergeSorted(other.Lazy(), key).Collect(ctx)
}

// Explode gives each element of the named List columns a row of its own; Unnest
// gives each field of the named Struct columns a column of its own.
func (df *DataFrame) Explode(ctx context.Context, names ...string) (*DataFrame, error) {
	return df.Lazy().Explode(names...).Collect(ctx)
}

func (df *DataFrame) Unnest(ctx context.Context, names ...string) (*DataFrame, error) {
	return df.Lazy().Unnest(names...).Collect(ctx)
}

// WithRowIndex adds a column numbering the rows from offset.
func (df *DataFrame) WithRowIndex(ctx context.Context, name string, offset uint32) (*DataFrame, error) {
	return df.Lazy().WithRowIndex(name, offset).Collect(ctx)
}

// Remove drops the rows where any predicate is true, or null.
func (df *DataFrame) Remove(ctx context.Context, preds ...Expr) (*DataFrame, error) {
	return df.Lazy().Remove(preds...).Collect(ctx)
}

// GroupBy, GroupByDynamic and Rolling start a grouping of the frame. Each gives the
// lazy builder, whose Agg, or Sum, Mean and the rest, is then collected:
//
//	out, err := df.GroupBy(Col("k")).Agg(Col("v").Sum()).Collect(ctx)
//
// An eager answer would need a second builder with every aggregate again, each
// taking a context; the context belongs to Collect.
func (df *DataFrame) GroupBy(keys ...Expr) *GroupBy { return df.Lazy().GroupBy(keys...) }

func (df *DataFrame) GroupByDynamic(index Expr, o DynamicOptions) *GroupBy {
	return df.Lazy().GroupByDynamic(index, o)
}

func (df *DataFrame) Rolling(index Expr, o RollingOptions) *GroupBy {
	return df.Lazy().Rolling(index, o)
}
