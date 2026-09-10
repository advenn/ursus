package physical

import (
	"context"
	"errors"
	"io"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
)

// Sink is a PIPELINE BREAKER: an operator that must consume all of its input
// before it can emit anything.
//
// # Why this is a third shape rather than just an Operator
//
// Sort and Aggregate could each be written as a plain Operator that drains its
// child on the first Next call. That works today and forfeits the property step 1
// spent its architecture budget buying: that v0.2's morsel scheduler consumes the
// same operator values unchanged.
//
// Consume/Merge/Finish is the shape a parallel scheduler needs. N workers each get
// their own Sink, consume morsels independently, and the partial results are
// merged. Written as a monolithic Operator, both would have to be rewritten the
// day the scheduler lands — which is exactly the rewrite step 1 was designed to
// avoid.
//
// # Merge is NOT the spilling seam, whatever this comment used to say
//
// Three operators spill now — sort (step 10), group_by (step 12) and join (step 13)
// — and not one of them goes through Merge. Each spills inside Consume and rejoins
// inside Finish or Probe, and all three REFUSE to Merge once they have spilled,
// because a fold that dropped the other sink's files would be silent.
//
// It said: "a Sink that runs out of memory writes its partial state out, starts
// fresh, and merges at the end." Read against the interface, every verb in that
// sentence except "merges" is missing — there is no spill, no reset, and Merge
// takes a live Sink rather than anything on disk.
//
// It is also wrong in the specific case it was written for. Merge's contract is
// that `other` consumed a LATER portion of the input, and sortSink.Merge honours
// it by concatenating in INPUT order: neither sink has sorted anything at that
// point, so the sort's Merge does the opposite of a merge. External sort therefore
// spills inside Consume and merges inside Finish (see extsort.go), and Merge keeps
// the meaning it actually has.
//
// Note also that the SEVEN implementations give the "LATER portion" clause four
// different readings — append (sort AND reverse), remap-and-append (join),
// refuse-unless-identical (group_by), refuse-always (window, temporal group, as-of
// build). That is a per-operator rule rather than a universal one, and it is
// incompatible with a radix-partitioned aggregation, whose partials are disjoint by
// hash rather than ordered by input position.
//
// This paragraph used to claim five implementations and a fifth reading — "PREPEND
// (reverse, because reversal turns later into earlier)". Both were wrong.
// reverseSink buffers untouched and reverses the whole concatenation in Finish, so
// it appends for the same reason sortSink does; prepending merely put the
// concatenation in the wrong order, and the count was never five.
//
// Nothing calls Merge concurrently yet, and it is implemented and tested anyway,
// because a Merge written later against forgotten invariants is a Merge that is
// wrong. reverseSink is the proof: it went from step 16 to step 50 with no test at
// all, and it was not merely unverified but WRONG.
type Sink interface {
	Schema() *dtype.Schema

	// Consume accumulates one batch.
	//
	// It MAY retain in without copying, and four of the five implementations do —
	// a Batch is a schema pointer plus immutable Columns, so retaining one retains
	// immutable values. This comment used to say the opposite, which contradicted
	// three sinks that state their retention in a comment and the whole of step
	// 10's accounting, whose central fact is that "every pipeline breaker retains
	// the source's own batches rather than copies".
	//
	// A sink that retains must account for what it holds through its execopt
	// Account, or a memory limit measures something other than what is held.
	Consume(ctx context.Context, in *data.Batch) error

	// Merge folds another Sink's partial state into this one. `other` must have
	// consumed a LATER portion of the input than this one, which is what makes
	// order-sensitive aggregates (First, Last) well-defined under merging.
	Merge(other Sink) error

	// Finish produces the result as an Operator. Called once, after the last
	// Consume.
	Finish(ctx context.Context) (Operator, error)

	Close() error
}

// breaker adapts a Sink into the pull chain.
//
// This is the entire cost of having chosen pull for v0.1: about 25 lines. stage,
// scanOp, limitOp, filterOp, projectOp and all three exec drivers are untouched by
// the existence of pipeline breakers.
type breaker struct {
	child Operator
	sink  Sink
	out   Operator
	done  bool
}

func (b *breaker) Schema() *dtype.Schema { return b.sink.Schema() }

func (b *breaker) Next(ctx context.Context) (*data.Batch, error) {
	if !b.done {
		if err := b.drain(ctx); err != nil {
			return nil, err
		}
		b.done = true
	}
	return b.out.Next(ctx)
}

func (b *breaker) drain(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		in, err := b.child.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if err := b.sink.Consume(ctx, in); err != nil {
			return err
		}
	}
	out, err := b.sink.Finish(ctx)
	if err != nil {
		return err
	}
	b.out = out
	return nil
}

func (b *breaker) Close() error {
	var errs []error
	if b.out != nil {
		errs = append(errs, b.out.Close())
	}
	errs = append(errs, b.sink.Close(), b.child.Close())
	return errors.Join(errs...)
}

// batchOperator serves a fixed slice of batches. It is what a Sink's Finish
// returns, and it is the only Operator in the package with no child.
type batchOperator struct {
	schema  *dtype.Schema
	batches []*data.Batch
	i       int
}

func newBatchOperator(schema *dtype.Schema, batches ...*data.Batch) Operator {
	return &batchOperator{schema: schema, batches: batches}
}

func (o *batchOperator) Schema() *dtype.Schema { return o.schema }
func (o *batchOperator) Close() error          { return nil }

func (o *batchOperator) Next(ctx context.Context) (*data.Batch, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if o.i >= len(o.batches) {
			return nil, io.EOF
		}
		b := o.batches[o.i]
		o.i++
		// An empty batch is not end-of-stream — a global aggregate over an empty
		// input legitimately produces one row, and a group-by over one produces
		// zero, and neither should be confused with "the stream is finished".
		if b.Rows() == 0 && len(o.batches) > 1 {
			continue
		}
		return b, nil
	}
}
