package ursus

import (
	"context"
	"fmt"
	"iter"
	"strings"

	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// A group-by's groups as frames (step 321, tier 2 of v0.6.7-scope.md): Polars'
// iteration over a group_by and map_groups.

// Groups is each group's rows as a frame, beside its key, as Polars' `for key, df in
// df.group_by(...)` gives them: the groups in the order each first appears, each
// group's rows in the frame's order, a key computed from a column its own column.
// It runs the query once and holds its answer, as PartitionBy does, which it is.
//
//	for g, err := range lf.GroupBy(ursus.Col("region")).Groups(ctx) { ... g.Key, g.Frame ... }
func (g *GroupBy) Groups(ctx context.Context, opts ...CollectOption) iter.Seq2[Partition, error] {
	return func(yield func(Partition, error) bool) {
		parts, err := g.partitions(ctx, opts)
		if err != nil {
			yield(Partition{}, err)
			return
		}
		for _, p := range parts {
			if !yield(p, nil) {
				return
			}
		}
	}
}

// MapGroups is fn of each group's rows, the answers stacked in the groups' order, as
// Polars' map_groups: a Go function over a frame where an aggregation will not do.
// Each answer must have the first's columns and types.
//
//	top, err := lf.GroupBy(ursus.Col("region")).MapGroups(ctx, func(g *ursus.DataFrame) (*ursus.DataFrame, error) {
//		return g.Lazy().Sort(ursus.Desc(ursus.Col("amount"))).Head(3).Collect(ctx)
//	})
func (g *GroupBy) MapGroups(ctx context.Context, fn func(*DataFrame) (*DataFrame, error),
	opts ...CollectOption) (*DataFrame, error) {
	parts, err := g.partitions(ctx, opts)
	if err != nil {
		return nil, err
	}
	answers := make([]*DataFrame, 0, len(parts))
	for i, p := range parts {
		out, err := fn(p.Frame)
		if err != nil {
			return nil, uerr.Wrap(err, uerr.KindValue, "map_groups", "group %d", i)
		}
		answers = append(answers, out)
	}
	if len(answers) == 0 {
		return nil, uerr.New(uerr.KindValue, "map_groups", "there are no groups to map")
	}
	return ConcatFrames(ctx, answers)
}

// partitions is the group-by's groups: each group's rows as the frame holds them,
// its own columns only, as Polars hands a group out, and its key under the key
// expressions' names. The keys are computed into hidden columns, which PartitionBy
// splits by and drops.
func (g *GroupBy) partitions(ctx context.Context, opts []CollectOption) ([]Partition, error) {
	if g.lf.err != nil {
		return nil, g.lf.err
	}
	if g.temporal != nil || g.temporalErr != nil {
		return nil, uerr.New(uerr.KindUnsupported, "group_by",
			"a dynamic or rolling group's windows overlap, so its rows are not split into frames").
			Hint("Agg an Implode of each column, or use GroupBy")
	}
	if len(g.keys) == 0 {
		df, err := g.lf.Collect(ctx, opts...)
		if err != nil {
			return nil, err
		}
		empty, err := Frame().Collect(ctx)
		if err != nil {
			return nil, err
		}
		return []Partition{{Key: empty, Frame: df}}, nil
	}
	keys := make([]Expr, len(g.keys))
	for i, k := range g.keys {
		keys[i] = wrap(k)
	}
	ks, err := g.lf.Select(keys...).schemaNow()
	if err != nil {
		return nil, err
	}
	fs, err := g.lf.schemaNow()
	if err != nil {
		return nil, err
	}
	taken := fs.Names()
	hidden, by := make([]Expr, len(keys)), make([]string, len(keys))
	for i, k := range keys {
		by[i] = freshName(taken, fmt.Sprintf("group_key_%d", i))
		taken = append(taken, by[i])
		hidden[i] = k.Alias(by[i])
	}
	df, err := g.lf.WithColumns(hidden...).Collect(ctx, opts...)
	if err != nil {
		return nil, err
	}
	parts, err := df.PartitionBy(ctx, PartitionOptions{By: by, DropKey: true})
	if err != nil {
		return nil, err
	}
	names := ks.Names()
	for i, p := range parts {
		cols := p.Key.batch.Columns()
		named := make([]*data.Column, len(cols))
		for j, c := range cols {
			named[j] = c.Rename(names[j])
		}
		if parts[i].Key, err = frameOfColumns(named, 1); err != nil {
			return nil, err
		}
	}
	return parts, nil
}

// Extend is the frame with other's rows after its own, as Polars' extend, which ursus
// answers with a new frame where Polars changes its own: ConcatFrames of the two,
// strict, the columns' names and types the same.
func (df *DataFrame) Extend(ctx context.Context, other *DataFrame) (*DataFrame, error) {
	return ConcatFrames(ctx, []*DataFrame{df, other})
}

// WithColumnsSeq and SelectSeq are WithColumns and Select, as Polars' with_columns_seq
// and select_seq, which evaluate their expressions one after another rather than in
// parallel; ursus's evaluate them alike either way.
func (lf *LazyFrame) WithColumnsSeq(exprs ...Expr) *LazyFrame { return lf.WithColumns(exprs...) }

func (lf *LazyFrame) SelectSeq(exprs ...Expr) *LazyFrame { return lf.Select(exprs...) }

// EstimatedSize is how many bytes the frame's buffers hold, as Polars'
// estimated_size: shared buffers counted once.
func (df *DataFrame) EstimatedSize() int64 { return df.batch.NBytes() }

// Glimpse is the frame on its side, a line a column, as Polars' glimpse prints it: its
// height and width, then each column's name, type and first ten values, as the frame
// prints them, a null "null". A name past 50 characters is cut to 49 and "…":
//
//	Rows: 3
//	Columns: 2
//	$ price <Float64> 9.5, 3, null
//	$ sku    <String> "a", "b", "c"
func (df *DataFrame) Glimpse() string {
	const most, longest = 10, 50
	s := df.batch.Schema()
	names := make([]string, s.Len())
	nameW, typeW := 0, 0
	for i, f := range s.All() {
		names[i] = f.Name
		if r := []rune(f.Name); len(r) > longest {
			names[i] = string(r[:longest-1]) + "…"
		}
		nameW = max(nameW, displayWidth(names[i]))
		typeW = max(typeW, displayWidth(f.Type.String())+2)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Rows: %d\nColumns: %d\n", df.Height(), s.Len())
	for i, f := range s.All() {
		c := df.batch.Column(i)
		vals := make([]string, min(c.Len(), most))
		for r := range vals {
			vals[r] = renderCell(c, r)
		}
		t := "<" + f.Type.String() + ">"
		fmt.Fprintf(&b, "$ %s %s%s %s\n", pad(names[i], nameW), strings.Repeat(" ", typeW-displayWidth(t)), t,
			strings.Join(vals, ", "))
	}
	return b.String()
}
