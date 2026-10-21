package physical

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/uerr"
)

// planPivot lays out the pairs of a pivot's aggregate (step 303): plan.Pivot. The
// aggregate beneath streams its input and spills past a limit; the sink holds its
// pairs, a row for each index and On values that meet, and refuses past the limit.
func planPivot(ctx context.Context, p *plan.Pivot, opts Options) (Operator, error) {
	out, err := p.Schema()
	if err != nil {
		return nil, err
	}
	in, err := p.Input.Schema()
	if err != nil {
		return nil, err
	}
	child, err := planPipelined(ctx, p.Input, opts)
	if err != nil {
		return nil, err
	}
	on, err := planPipelined(ctx, p.OnColumns, opts)
	if err != nil {
		child.Close()
		return nil, err
	}
	sink := &pivotSink{p: p, in: in, out: out, on: on, mem: opts.Budget.Account("pivot")}
	return &breaker{child: child, sink: sink}, nil
}

// pivotSink holds a pivot's pairs, in the order they first appear, and lays them out
// at Finish.
type pivotSink struct {
	p       *plan.Pivot
	in, out *dtype.Schema
	on      Operator // OnColumns, read at Finish
	mem     *execopt.Account
	held    []*data.Batch
}

func (s *pivotSink) Schema() *dtype.Schema { return s.out }

// Consume holds a batch of pairs, retained, not copied, as reverseSink holds its.
func (s *pivotSink) Consume(_ context.Context, b *data.Batch) error {
	if b.Rows() == 0 {
		return nil
	}
	s.held = append(s.held, b)
	s.mem.Retain(b)
	return s.mem.Check()
}

// Merge appends other's pairs, which came later, as reverseSink appends its.
func (s *pivotSink) Merge(other Sink) error {
	o, ok := other.(*pivotSink)
	if !ok {
		return uerrInternal("pivotSink", other)
	}
	for _, b := range o.held {
		s.held = append(s.held, b)
		s.mem.Retain(b)
	}
	o.held = nil
	o.mem.Release()
	return s.mem.Check()
}

func (s *pivotSink) Close() error {
	s.mem.Release()
	s.held = nil
	if s.on == nil {
		return nil
	}
	err := s.on.Close()
	s.on = nil
	return err
}

func (s *pivotSink) Finish(ctx context.Context) (Operator, error) {
	combos, err := s.onColumns(ctx)
	if err != nil {
		return nil, err
	}
	long := make([]*data.Column, s.in.Len())
	n := 0
	for i := range long {
		parts := make([]*data.Column, len(s.held))
		for j, b := range s.held {
			parts[j] = b.Column(i)
		}
		if len(parts) == 0 {
			long[i] = emptyColumns(s.in)[i]
			continue
		}
		if long[i], err = kernel.ConcatColumns(s.in.Field(i).Name, parts); err != nil {
			return nil, err
		}
		n = long[i].Len()
	}
	b, err := pivotSpread(s.p, s.out, long, n, combos)
	if err != nil {
		return nil, err
	}
	return newBatchOperator(s.out, b), nil
}

// onColumns reads OnColumns whole: a column for each of On, a row for each new
// column of a value.
func (s *pivotSink) onColumns(ctx context.Context) ([]*data.Column, error) {
	schema := s.on.Schema()
	var batches []*data.Batch
	for {
		b, err := s.on.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		batches = append(batches, b)
	}
	cols := make([]*data.Column, schema.Len())
	for i := range cols {
		parts := make([]*data.Column, 0, len(batches))
		for _, b := range batches {
			parts = append(parts, b.Column(i))
		}
		if len(parts) == 0 {
			cols[i] = emptyColumns(schema)[i]
			continue
		}
		c, err := kernel.ConcatColumns(schema.Field(i).Name, parts)
		if err != nil {
			return nil, err
		}
		cols[i] = c
	}
	return cols, nil
}

// pivotSpread lays the n pairs of long out: a row for each index, in the order each
// first appears, and for each value a column for each row of combos, holding the
// cell where the index and that row's On values meet, and where they never do the
// value's cell over no values, or null without one.
func pivotSpread(p *plan.Pivot, out *dtype.Schema, long []*data.Column, n int, combos []*data.Column) (*data.Batch, error) {
	ni, no, nv := len(p.Index), len(p.On), len(p.Values)
	k := p.Width()
	if !p.Empty {
		counts := data.MustValues[uint64](long[ni+no+nv])
		for r, c := range counts {
			if c > 1 {
				return nil, uerr.New(uerr.KindValue, "pivot",
					"%d rows fall where %s meet, and Agg is not set", c, pivotMeeting(long[:ni+no], r)).
					Hint("give an Agg to reduce them, e.g. ursus.Expr.Sum or ursus.Expr.First")
			}
		}
	}
	group, first, err := groupsOf(long[:ni], n)
	if err != nil {
		return nil, err
	}

	// Each row of combos by its key, as the group-by keyed the pairs. Rows can share
	// one: 0 beside -0, each its own column, both of the pairs of either.
	keys, err := kernel.NewGroupKeyEncoder("pivot", combos)
	if err != nil {
		return nil, err
	}
	byKey := make(map[string][]int32, k)
	for u := range k {
		key := string(keys.Encode(u))
		byKey[key] = append(byKey[key], int32(u))
	}
	pairs, err := kernel.NewGroupKeyEncoder("pivot", long[ni:ni+no])
	if err != nil {
		return nil, err
	}
	rowsOf := make([][]int32, k)
	for r := range n {
		for _, u := range byKey[string(pairs.Encode(r))] {
			rowsOf[u] = append(rowsOf[u], int32(r))
		}
	}

	cols := make([]*data.Column, 0, out.Len())
	for i, name := range p.Index {
		c, err := kernel.Take(long[i], first)
		if err != nil {
			return nil, err
		}
		cols = append(cols, c.Rename(name))
	}
	g := len(first)
	pick := make([]int32, g)
	for j := range nv {
		src := long[ni+no+j]
		never := kernel.NullIndex
		if p.Empty && n > 0 {
			// The cell over no values, alike on every pair's row, after the cells.
			none, err := kernel.Take(long[ni+no+nv+j], []int32{0})
			if err != nil {
				return nil, err
			}
			if src, err = kernel.ConcatColumns(src.Name(), []*data.Column{src, none}); err != nil {
				return nil, err
			}
			never = int32(n)
		}
		for u := range k {
			for x := range pick {
				pick[x] = never
			}
			for _, r := range rowsOf[u] {
				pick[group[r]] = r
			}
			c, err := kernel.Take(src, pick)
			if err != nil {
				return nil, err
			}
			cols = append(cols, c.Rename(p.Names[j*k+u]))
		}
	}
	return data.NewBatchRows(out, cols, g), nil
}

// pivotMeeting names pair r's index and On values, for an error: k=1 and on=x.
func pivotMeeting(cols []*data.Column, r int) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		text := "null"
		if one, err := kernel.Take(c, []int32{int32(r)}); err == nil && one.IsValid(0) {
			if s, err := kernel.Cast(c.Name(), dtype.String, false, one); err == nil && s.IsValid(0) {
				text = s.Strings().Get(0)
			}
		}
		parts[i] = c.Name() + "=" + text
	}
	return strings.Join(parts, " and ")
}

// groupsOf numbers each of n rows by its values in cols, as group_by groups them, a
// null among them, in the order each group first appears; and gives each group's
// first row. No columns is one group, of n rows or none.
func groupsOf(cols []*data.Column, n int) ([]int32, []int32, error) {
	ids := make([]int32, n)
	if len(cols) == 0 {
		if n == 0 {
			return ids, nil, nil
		}
		return ids, []int32{0}, nil
	}
	enc, err := kernel.NewGroupKeyEncoder("pivot", cols)
	if err != nil {
		return nil, nil, err
	}
	seen := make(map[string]int32)
	var first []int32
	for r := range n {
		k := string(enc.Encode(r))
		id, ok := seen[k]
		if !ok {
			id = int32(len(first))
			seen[k] = id
			first = append(first, int32(r))
		}
		ids[r] = id
	}
	return ids, first, nil
}
