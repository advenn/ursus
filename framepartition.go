package ursus

import (
	"context"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/uerr"
)

// A frame split by key (step 305, P3 of v0.6.7-scope.md): Polars' partition_by.

// Partition is one of PartitionBy's parts: the key its rows share, and the rows.
type Partition struct {
	// Key is the key's values: one row, a column for each of By, typed as the frame's,
	// a null key's null. Key.At[string](0, "region") reads one.
	Key *DataFrame
	// Frame is the rows holding the key, in the frame's order: every column, or every
	// column but By with DropKey.
	Frame *DataFrame
}

// PartitionOptions says how PartitionBy splits a frame.
type PartitionOptions struct {
	// By is the columns whose values key the parts: one or more.
	By []string
	// DropKey leaves By out of each part's Frame, as Polars' include_key=False does;
	// its Key holds them still.
	DropKey bool
}

// PartitionBy splits the frame into a part for each distinct key of By, in the order
// each key first appears, a null key among them, as Polars' DataFrame.partition_by
// does. Polars' as_dict maps key tuples to frames, and Go has no map by a tuple, so
// here each part's Key stands beside its Frame. Keys group as a group-by groups them,
// -0 with 0 and NaN with NaN; a List or a Struct column is no key here, where Polars
// partitions by one.
//
//	parts, err := df.PartitionBy(ctx, ursus.PartitionOptions{By: []string{"region"}})
//	for _, p := range parts {
//		region, _, _ := p.Key.At[string](0, "region")
//		fmt.Println(region, p.Frame.Height())
//	}
//
// The parts are taken from the frame it is given, and together hold as much as it
// does.
func (df *DataFrame) PartitionBy(ctx context.Context, o PartitionOptions) ([]Partition, error) {
	if len(o.By) == 0 {
		return nil, uerr.New(uerr.KindValue, "partition_by", "PartitionBy needs a column to partition by").
			Hint("name the columns whose values key the parts in By")
	}
	b := df.batch
	keys := make([]*data.Column, len(o.By))
	keyFields := make([]dtype.Field, len(o.By))
	isKey := make(map[string]bool, len(o.By))
	for i, name := range o.By {
		j := b.Schema().IndexOf(name)
		if j < 0 {
			return nil, uerr.UnknownColumn("partition_by", name, df.Columns())
		}
		if isKey[name] {
			return nil, uerr.New(uerr.KindValue, "partition_by", "%q is in By twice", name)
		}
		keys[i], keyFields[i], isKey[name] = b.Column(j), b.Schema().Field(j), true
	}
	enc, err := kernel.NewGroupKeyEncoder("partition_by", keys)
	if err != nil {
		return nil, err
	}

	// Each row's part, numbered in the order each key first appears, then each part's
	// rows, in the frame's order.
	n := b.Rows()
	part := make([]int32, n)
	seen := make(map[string]int32)
	var sizes []int
	for r := range n {
		k := enc.Encode(r)
		p, ok := seen[string(k)]
		if !ok {
			p = int32(len(sizes))
			seen[string(k)] = p
			sizes = append(sizes, 0)
		}
		part[r] = p
		sizes[p]++
	}
	rows := make([][]int32, len(sizes))
	for p, size := range sizes {
		rows[p] = make([]int32, 0, size)
	}
	for r, p := range part {
		rows[p] = append(rows[p], int32(r))
	}

	var kept []int
	for i, f := range b.Schema().All() {
		if !o.DropKey || !isKey[f.Name] {
			kept = append(kept, i)
		}
	}
	keySchema, err := dtype.NewSchema(keyFields...)
	if err != nil {
		return nil, err
	}
	fields := make([]dtype.Field, len(kept))
	for i, j := range kept {
		fields[i] = b.Schema().Field(j)
	}
	frameSchema, err := dtype.NewSchema(fields...)
	if err != nil {
		return nil, err
	}

	parts := make([]Partition, len(rows))
	for p, sel := range rows {
		if p%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		key := make([]*data.Column, len(keys))
		for i, c := range keys {
			if key[i], err = kernel.Take(c, sel[:1]); err != nil {
				return nil, err
			}
		}
		cols := make([]*data.Column, len(kept))
		for i, j := range kept {
			if cols[i], err = kernel.Take(b.Column(j), sel); err != nil {
				return nil, err
			}
		}
		parts[p] = Partition{
			Key:   &DataFrame{batch: data.NewBatchRows(keySchema, key, 1)},
			Frame: &DataFrame{batch: data.NewBatchRows(frameSchema, cols, len(sel))},
		}
	}
	return parts, nil
}
