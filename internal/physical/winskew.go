package physical

import (
	"bytes"
	"context"
	"errors"

	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
)

// A keyed window's one partition over its budget (step 209, Tier 2 of
// v0.6.2-scope.md).
//
// Routing to disk divides partitions, not the rows of one, so a partition larger
// than the limit refused, a level down, once its sub-sink held that partition
// alone. It need not, for the functions a window with no partition_by streams (step
// 197): an aggregate, a cumulative function, a shift, a limited fill, a rolling
// window of rows, and since step 221 an ewm function and a reversed cum_count. The partition is one, and its rows arrive in their order. So that
// sub-sink splits its file:
//
//   - the one key's rows go to the one-partition file, its aggregates fed as group 0,
//     and are read back a batch at a time with their bounded context (winstream.go);
//   - every other key's rows go to a fresh sink a level deeper, which may spill as
//     any does.
//
// Both answer in their rows' order, which is the ordinal's, and the two answers are
// merged on the ordinal. A function in an order of its own is answered in a sorted
// pass over the one-partition file (winsorted.go, step 235). A function that needs
// the whole partition at once, a rank, still refuses.

// skewable reports whether the sink can split its one key off: a sink a level down,
// of one keyed partitioning, holding a single key so far, every function bounded.
func (s *windowSink) skewable() bool {
	if s.level == 0 || len(s.parts) != 1 || len(s.parts[0].keys) == 0 || s.parts[0].ids.Len() != 1 {
		return false
	}
	for i := range s.specs {
		if !streamsAlone(&s.specs[i]) {
			return false
		}
	}
	return true
}

// startSkew sends the key held, and the rows held, to the one-partition file, and
// makes the sink every other key goes to.
func (s *windowSink) startSkew() error {
	rest, err := newWindowSink(s.inSchema, s.schema, s.exprs, s.budget.Account("over"))
	if err != nil {
		return err
	}
	rest.budget, rest.batch, rest.level = s.budget, s.batch, s.level+1
	rest.ord, rest.spillSchema = s.ord, s.spillSchema
	s.skewKey = append([]byte(nil), s.parts[0].ids.KeyAt(0)...)
	s.rest = rest
	return s.startOneSpill()
}

// consumeSkew splits a batch: the key's rows to the file, the rest to their sink.
func (s *windowSink) consumeSkew(ctx context.Context, in *data.Batch) error {
	keys := s.parts[0].keys
	cols := make([]*data.Column, len(keys))
	for i, k := range keys {
		c, err := evalColumn(ctx, k, in)
		if err != nil {
			return err
		}
		cols[i] = c
	}
	enc, err := kernel.NewGroupKeyEncoder("over", cols)
	if err != nil {
		return err
	}
	var mine, other []int32
	for i := range in.Rows() {
		if bytes.Equal(enc.Encode(i), s.skewKey) {
			mine = append(mine, int32(i))
		} else {
			other = append(other, int32(i))
		}
	}
	if len(mine) > 0 {
		b := in
		if len(other) > 0 {
			if b, err = takeBatch(s.inSchema, in, mine); err != nil {
				return err
			}
		}
		if err := s.consumeOneSpill(ctx, b); err != nil {
			return err
		}
	}
	if len(other) > 0 {
		b := in
		if len(mine) > 0 {
			if b, err = takeBatch(s.inSchema, in, other); err != nil {
				return err
			}
		}
		return s.rest.Consume(ctx, b)
	}
	return nil
}

// finishSkew merges the key's answer and the rest's on the ordinal.
func (s *windowSink) finishSkew(ctx context.Context) (Operator, error) {
	one, err := s.finishOneSpill(ctx)
	if err != nil {
		return nil, err
	}
	rest, err := s.rest.Finish(ctx)
	if err != nil {
		return nil, errors.Join(err, one.Close())
	}
	return mergeRuns(s.schema, s.ord, []runSource{&opRun{ctx: ctx, op: one}, &opRun{ctx: ctx, op: rest}},
		s.budget.Account("over"), s.batch)
}

// opRun is an operator read as a run, for a merge.
type opRun struct {
	ctx context.Context
	op  Operator
}

func (r *opRun) Next() (*data.Batch, error) { return r.op.Next(r.ctx) }
func (r *opRun) Close() error               { return r.op.Close() }
