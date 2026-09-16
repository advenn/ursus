package ursus

// Arrow export: how anything outside this module reads ursus's output.
//
// The conversion shares memory rather than copying it — ursus already stores Arrow's
// layout, so a column is an Arrow array with a different Go type around the same
// bytes. internal/arrowout has the details, including the two places where the
// layouts genuinely differ and a copy is unavoidable.

import (
	"context"
	"iter"

	"github.com/apache/arrow-go/v18/arrow"

	"github.com/advenn/ursus/internal/arrowout"
)

// ArrowSchema returns the frame's schema as an Arrow schema.
//
// It can fail, because a few ursus types have no Arrow equivalent yet — see Record.
// Asking for the schema is how to find that out without running anything.
func (df *DataFrame) ArrowSchema() (*arrow.Schema, error) {
	return arrowout.Schema(df.batch.Schema())
}

// Record exports the frame as an Arrow record, sharing its memory.
//
// This is the zero-copy boundary to the rest of the Arrow ecosystem: hand the result
// to arrow-go's IPC writer, to Flight, to ADBC, or to any library that speaks
// arrow.Record, and no column data is copied on the way.
//
//	rec, err := df.Record()
//	if err != nil { return err }
//	defer rec.Release()
//
//	w := ipc.NewWriter(f, ipc.WithSchema(rec.Schema()))
//	w.Write(rec)
//
// # Release is optional here, and that is deliberate
//
// The record's buffers are re-wrapped so that Release on them cannot reach ursus's
// own memory. Releasing is still the right habit — every other arrow-go producer
// expects it, and the example above does it — but forgetting leaks nothing, and
// releasing twice cannot empty a frame you still hold. internal/arrowout's doc
// explains why that second guarantee needed arranging.
//
// # It can fail
//
// Array, Enum, Categorical and Uint128 are declared in ursus's type enum and are not
// exportable yet; a frame containing one is refused by name rather than exported
// wrongly. Everything else maps, including Decimal and the Int128 that every integer
// Sum produces.
func (df *DataFrame) Record() (arrow.Record, error) {
	return arrowout.Record(df.batch)
}

// CollectRecords streams the result as Arrow records without materialising the whole
// frame, which is what makes a larger-than-RAM result exportable.
//
// It is CollectBatches with the conversion applied, and it inherits every property:
// breaking out of the loop tears the operator tree down, so an early exit is safe.
//
//	for rec, err := range lf.CollectRecords(ctx) {
//	    if err != nil { return err }
//	    if err := w.Write(rec); err != nil { rec.Release(); return err }
//	    rec.Release()
//	}
//
// Each record shares the memory of the batch it came from, and the engine reuses
// nothing across batches, so a record stays valid after the next one arrives.
func (lf *LazyFrame) CollectRecords(ctx context.Context, opts ...CollectOption) iter.Seq2[arrow.Record, error] {
	return func(yield func(arrow.Record, error) bool) {
		for df, err := range lf.CollectBatches(ctx, opts...) {
			if err != nil {
				yield(nil, err)
				return
			}
			rec, err := df.Record()
			if err != nil {
				yield(nil, err)
				return
			}
			if !yield(rec, nil) {
				return
			}
		}
	}
}
