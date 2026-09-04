// Package exec drives a physical operator tree to completion.
//
// It is a SERIAL driver, and it stayed one. This doc used to say "the seam where
// morsel-driven parallelism arrives in v0.2 is here and only here", and step 5 put
// parallelism somewhere else entirely: physical.parallelOp wraps the pipeline below
// this loop, so a query runs on N threads without exec knowing. The prediction was
// wrong about the place, and the reason is worth keeping — parallelOp's round-robin
// dispatch is what BUYS input order structurally, and a scheduler here would have to
// re-establish it, which is the harder problem.
//
// What is still true: nothing here knows about spilling, budgets or temporary files.
// Every one of those lives below, which is why three operators gained the ability to
// spill without this file changing.
package exec

import (
	"context"
	"errors"
	"io"
	"iter"

	"ursus/dtype"
	"ursus/internal/data"
	"ursus/internal/kernel"
	"ursus/internal/physical"
)

// Collect runs the tree to completion and concatenates every batch.
func Collect(ctx context.Context, root physical.Operator) (*data.Batch, error) {
	defer root.Close()

	var batches []*data.Batch
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		b, err := root.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		batches = append(batches, b)
	}
	return kernel.Concat(root.Schema(), batches)
}

// Batches streams results without materialising the whole frame.
//
// The iterator yields (batch, nil) until the stream ends, or (nil, err) once on
// failure and then stops. Breaking out of the range loop closes the operator tree,
// which is what makes an early `break` safe.
func Batches(ctx context.Context, root physical.Operator) iter.Seq2[*data.Batch, error] {
	return func(yield func(*data.Batch, error) bool) {
		defer root.Close()
		for {
			if err := ctx.Err(); err != nil {
				yield(nil, err)
				return
			}
			b, err := root.Next(ctx)
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				yield(nil, err)
				return
			}
			if !yield(b, nil) {
				return // consumer broke out; the deferred Close tears the tree down
			}
		}
	}
}

// Count runs the tree and returns the row count without retaining any batch.
func Count(ctx context.Context, root physical.Operator) (int64, error) {
	defer root.Close()

	var n int64
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		b, err := root.Next(ctx)
		if errors.Is(err, io.EOF) {
			return n, nil
		}
		if err != nil {
			return 0, err
		}
		n += int64(b.Rows())
	}
}

// Schema returns the tree's output schema without executing it.
func Schema(root physical.Operator) *dtype.Schema { return root.Schema() }
