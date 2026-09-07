// Package memsrc is an in-memory scan source.
//
// It is the source the walking skeleton runs against, and it is also the
// reference implementation of the source contract: anything a Parquet or CSV
// reader has to do, this does in fifty lines, so it doubles as the worked example
// for someone plugging in their own storage.
//
// It honours projection pushdown, which is what lets the end-to-end test assert
// that the optimizer's decision actually reaches the reader rather than being
// undone by a trim operator above it.
package memsrc

import (
	"context"
	"io"
	"strconv"
	"sync"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/source"
	"github.com/advenn/ursus/internal/uerr"
)

// Source is an in-memory table.
type Source struct {
	schema  *dtype.Schema
	batches []*data.Batch

	// projected records the column lists this source was actually asked for.
	// Tests assert on it to prove pushdown reached the reader instead of being
	// silently compensated for higher up.
	mu        sync.Mutex
	projected [][]string
}

// New builds a source from batches that all share a schema.
func New(schema *dtype.Schema, batches ...*data.Batch) (*Source, error) {
	for i, b := range batches {
		if !b.Schema().Equal(schema) {
			return nil, uerr.Internalf(
				"memsrc: batch %d has schema %s, want %s", i, b.Schema(), schema)
		}
	}
	return &Source{schema: schema, batches: batches}, nil
}

// FromBatch builds a single-batch source preserving the batch's DECLARED schema.
//
// # Why this is not FromColumns
//
// FromColumns derives nullability from the data — `Nullable: c.NullCount() > 0` —
// which is right when columns are all you have, and wrong when a schema already
// exists. Field.Nullable is "a static property of the schema, not a count of nulls
// actually present", so a nullable column that happens to hold no nulls (the
// ordinary result of a filter) would come back non-nullable, and every downstream
// join, aggregate and cast would reason from the wrong schema.
//
// It also preserves the ROW COUNT. A batch may legitimately have rows and no
// columns — the result of selecting nothing — and NewBatch sets rows to zero when
// there are none to count, which is why NewBatchRows exists.
func FromBatch(b *data.Batch) (*Source, error) {
	return New(b.Schema(), b)
}

// FromColumns builds a single-batch source from columns.
func FromColumns(cols ...*data.Column) (*Source, error) {
	fields := make([]dtype.Field, len(cols))
	for i, c := range cols {
		fields[i] = dtype.Field{
			Name:     c.Name(),
			Type:     c.DType(),
			Nullable: c.NullCount() > 0,
		}
	}
	schema, err := dtype.NewSchema(fields...)
	if err != nil {
		return nil, err
	}
	b, err := data.NewBatch(schema, cols)
	if err != nil {
		return nil, err
	}
	return New(schema, b)
}

// --- plan.Source -------------------------------------------------------------

func (s *Source) Name() string { return "memory" }

func (s *Source) Describe() string {
	rows := 0
	for _, b := range s.batches {
		rows += b.Rows()
	}
	return strconv.Itoa(rows) + " rows"
}

func (s *Source) Schema(context.Context) (*dtype.Schema, error) { return s.schema, nil }

func (s *Source) Caps() plan.Caps { return plan.Caps{Projection: true} }

// --- source.Openable ---------------------------------------------------------

func (s *Source) Open(_ context.Context, spec source.ScanSpec) (source.BatchSource, error) {
	out := s.schema
	if spec.Projection != nil {
		var err error
		if out, err = s.schema.Select(spec.Projection); err != nil {
			return nil, err
		}
	}

	s.mu.Lock()
	s.projected = append(s.projected, append([]string(nil), spec.Projection...))
	s.mu.Unlock()

	size := spec.BatchSize
	if size <= 0 {
		size = defaultBatchSize
	}
	return &reader{parent: s, schema: out, batches: s.batches, size: size}, nil
}

// defaultBatchSize matches the Parquet reader's, so the two sources behave the same
// when a caller does not say.
const defaultBatchSize = 8192

// Projections returns the column lists this source has been opened with.
func (s *Source) Projections() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]string(nil), s.projected...)
}

// --- source.BatchSource ------------------------------------------------------

// reader walks the source's batches, splitting any that are larger than the
// requested batch size.
//
// # Why splitting, and not just handing the batches over
//
// It used to hand them over exactly as given, and both public ways into in-memory
// data build exactly ONE batch — ursus.Frame through FromColumns, and
// DataFrame.Lazy through FromBatch. So an in-memory frame was a single batch of
// however many rows, the pipeline had one unit of work to distribute, and
// WithThreads did nothing at all: a query over a five-million-row frame ran on one
// worker whatever it was told.
//
// That also made WithBatchSize a lie on this source while CSV and Parquet both
// honoured it, and it is why step 36's join measurements had to chunk their fixture
// by hand before the fleet did anything.
//
// Splitting is cheap because data.Batch.Slice shares every payload rather than
// copying it — see Column.Slice, which stopped copying fixed-width values for this.
type reader struct {
	parent  *Source
	schema  *dtype.Schema
	batches []*data.Batch
	size    int

	// Guarded because BatchSource must be safe for concurrent Next: v0.1 has one
	// consumer, v0.2's morsel scheduler will have several.
	mu  sync.Mutex
	i   int // the batch to read from
	off int // rows already emitted from it
}

func (r *reader) Schema() *dtype.Schema { return r.schema }
func (r *reader) Close() error          { return nil }

func (r *reader) Next(ctx context.Context) (*data.Batch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	r.mu.Lock()
	// Skip past any empty batches rather than returning one: a zero-row batch is not
	// end of stream, and a consumer that treated it as one would truncate.
	for r.i < len(r.batches) && r.off >= r.batches[r.i].Rows() {
		r.i++
		r.off = 0
	}
	if r.i >= len(r.batches) {
		r.mu.Unlock()
		return nil, io.EOF
	}
	b := r.batches[r.i]
	if n := b.Rows() - r.off; n > r.size {
		b = b.Slice(r.off, r.size)
		r.off += r.size
	} else {
		if r.off > 0 {
			b = b.Slice(r.off, n)
		}
		r.i++
		r.off = 0
	}
	r.mu.Unlock()

	if r.schema.Equal(b.Schema()) {
		return b, nil
	}

	// Honour the projection: hand back only the requested columns, in the
	// requested order. This is the whole point of pushdown — a real reader would
	// never touch the other columns' bytes at all.
	cols := make([]*data.Column, r.schema.Len())
	for i, f := range r.schema.All() {
		c, ok := b.ByName(f.Name)
		if !ok {
			return nil, uerr.UnknownColumn("scan", f.Name, b.Schema().Names())
		}
		cols[i] = c
	}
	return data.NewBatch(r.schema, cols)
}
