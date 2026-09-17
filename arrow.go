package ursus

// Arrow interop.
//
// Export shares memory rather than copying it — ursus already stores Arrow's layout,
// so a column is an Arrow array with a different Go type around the same bytes.
// internal/arrowout has the details, including the two places where the layouts
// genuinely differ and a copy is unavoidable.
//
// Import copies, always. internal/arrowin says why: sharing would tie a frame's
// lifetime to memory an IPC reader reuses on its next record, or a C producer frees
// on release, and nothing in ursus's column types could keep that memory alive.

import (
	"context"
	"errors"
	"fmt"
	"iter"

	"github.com/apache/arrow-go/v18/arrow"

	"github.com/advenn/ursus/internal/arrowin"
	"github.com/advenn/ursus/internal/arrowout"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/source/memsrc"
	"github.com/advenn/ursus/internal/uerr"
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

// ScanArrowRecords reads Arrow records already in memory.
//
// The records are COPIED before this returns, so the caller may release them, or let
// the reader they came from move on, as soon as it likes:
//
//	var recs []arrow.RecordBatch
//	for rdr.Next() {
//	    rec := rdr.RecordBatch()
//	    rec.Retain() // an IPC reader reuses the record on its next Next
//	    recs = append(recs, rec)
//	}
//	lf := ursus.ScanArrowRecords(recs...)
//	for _, rec := range recs {
//	    rec.Release()
//	}
//
// Every record must convert to the same schema. Every column is nullable whatever
// the Arrow schema declares, because arrow-go never checks that flag and producers
// routinely leave it false while writing nulls. A type ursus cannot represent — a
// 256-bit decimal, an interval, a union — is refused by name.
//
// With no records there is no schema, so that is an error too; hand over an empty
// record instead. For a stream too large to copy up front, use ScanArrow.
func ScanArrowRecords(recs ...arrow.RecordBatch) *LazyFrame {
	src, err := recordsSource(recs)
	if err != nil {
		return &LazyFrame{err: err}
	}
	return Scan(src)
}

func recordsSource(recs []arrow.RecordBatch) (*memsrc.Source, error) {
	if len(recs) == 0 {
		return nil, uerr.New(uerr.KindValue, "arrow", "ScanArrowRecords needs at least one record").
			Hint("a scan needs a schema; pass an empty record that carries one")
	}
	if recs[0] == nil {
		return nil, uerr.New(uerr.KindValue, "arrow", "record 0 is nil")
	}
	schema, err := arrowin.Schema(recs[0].Schema())
	if err != nil {
		return nil, err
	}
	check := arrowin.NewChecker(schema)
	cols := make([]int, schema.Len())
	for i := range cols {
		cols[i] = i
	}
	batches := make([]*data.Batch, 0, len(recs))
	for i, rec := range recs {
		if err := check.Check(rec); err != nil {
			return nil, inRecord(err, i)
		}
		b, err := arrowin.Batch(rec, schema, cols, 0, int(rec.NumRows()))
		if err != nil {
			return nil, inRecord(err, i)
		}
		batches = append(batches, b)
	}
	return memsrc.New(schema, batches...)
}

// inRecord says which record an import error came from, keeping its kind.
func inRecord(err error, i int) error {
	var e *uerr.Error
	if !errors.As(err, &e) {
		return err
	}
	out := *e
	out.Msg = fmt.Sprintf("record %d: %s", i, e.Msg)
	return &out
}
