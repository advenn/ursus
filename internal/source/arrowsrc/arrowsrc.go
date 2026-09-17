// Package arrowsrc scans a stream of Arrow records: an IPC stream, a Flight
// response, a C Data Interface stream — anything that is an array.RecordReader.
//
// # A factory, not a reader
//
// A source is opened more than once: once to learn the schema when a query is
// planned, and once per scan per execution — a self-join scans it twice in one
// query, and a LazyFrame can be collected again. A RecordReader cannot be rewound.
// So the source holds a function that returns a fresh, independent reader every
// time, which is the rule ScanCSVReader states for byte streams.
//
// # Copied a window at a time
//
// A reader's record is valid only until its next Next, and a C Data record is freed
// through its producer on release. Each batch is copied out of the current record
// (see internal/arrowin), and the reader is not advanced until every row of that
// record has been copied — so nothing handed to the engine ever points into Arrow
// memory, and the reader's own contract holds without a Retain.
//
// # What the engine's calls cost
//
//   - Schema opens a reader, reads its schema, and releases it — once per Source.
//     Explain and CollectSchema stop there.
//   - Open does no I/O at all. The reader is opened on the first Next, so a scan
//     that is planned and then abandoned — the left side of a join whose right side
//     failed to plan — never opens one, and has nothing to leak.
//   - Close releases the reader. A blocking Next inside the reader cannot be
//     interrupted by a context, so Close waits for it.
package arrowsrc

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sync"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/arrowin"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/source"
	"github.com/advenn/ursus/internal/uerr"
)

const op = "scan_arrow"

// defaultBatchSize matches the other sources'.
const defaultBatchSize = 8192

// Source scans the readers a factory returns.
type Source struct {
	open func() (array.RecordReader, error)

	once   sync.Once
	schema *dtype.Schema
	err    error

	mu sync.Mutex
	// probe and last are the readers most recently returned, kept to refuse a factory
	// that returns one of them again. Holding them also keeps their addresses from
	// being reused, so the comparison cannot produce a false match.
	probe, last array.RecordReader
	projected   [][]string
}

// New returns a source over the readers open returns. open must return a new,
// independent reader on every call.
func New(open func() (array.RecordReader, error)) *Source { return &Source{open: open} }

func (s *Source) Name() string     { return "arrow" }
func (s *Source) Describe() string { return "" }

// Caps reports projection only. An Arrow stream carries no statistics a predicate
// could use, and the rows arrive already decoded.
func (s *Source) Caps() plan.Caps { return plan.Caps{Projection: true} }

// Schema opens one reader, converts its schema, and releases it. The result is
// cached; a cancelled context is not, so a retry with a live one still works.
func (s *Source) Schema(ctx context.Context) (*dtype.Schema, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.once.Do(func() {
		rr, err := s.call()
		if err != nil {
			s.err = err
			return
		}
		defer rr.Release()
		s.schema, s.err = schemaOf(rr)
	})
	return s.schema, s.err
}

// Projections returns the column lists this source has been opened with.
func (s *Source) Projections() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]string(nil), s.projected...)
}

// call runs the factory and refuses what it must not return.
func (s *Source) call() (array.RecordReader, error) {
	rr, err := s.open()
	if err != nil {
		return nil, uerr.Wrap(err, uerr.KindIO, op, "opening the Arrow stream")
	}
	if rr == nil || (reflect.ValueOf(rr).Kind() == reflect.Pointer && reflect.ValueOf(rr).IsNil()) {
		return nil, uerr.New(uerr.KindValue, op, "open returned neither a reader nor an error")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// A factory closing over one reader looks natural and is wrong: the schema probe
	// releases it, and arrow-go's in-memory reader then reports no records and no
	// error — an empty frame, silently. An IPC reader fails differently and more
	// confusingly. Interface comparison panics on an uncomparable dynamic type, so
	// only a comparable one is compared.
	if reflect.ValueOf(rr).Comparable() && (rr == s.probe || rr == s.last) {
		return nil, uerr.New(uerr.KindValue, op,
			"open returned a RecordReader it had already returned").
			Hint("ScanArrow calls open once for the schema and once per scan; it must " +
				"return a new, independent reader every time")
	}
	if s.probe == nil {
		s.probe = rr
	} else {
		s.last = rr
	}
	return rr, nil
}

func schemaOf(rr array.RecordReader) (*dtype.Schema, error) {
	as := rr.Schema()
	if as == nil {
		if err := rr.Err(); err != nil {
			return nil, uerr.Wrap(err, uerr.KindIO, op, "reading the Arrow stream's schema")
		}
		return nil, uerr.New(uerr.KindIO, op, "the Arrow stream has no schema")
	}
	return arrowin.Schema(as)
}

func (s *Source) Open(ctx context.Context, spec source.ScanSpec) (source.BatchSource, error) {
	full, err := s.Schema(ctx)
	if err != nil {
		return nil, err
	}
	out := full
	cols := make([]int, full.Len())
	for i := range cols {
		cols[i] = i
	}
	if spec.Projection != nil {
		if out, err = full.Select(spec.Projection); err != nil {
			return nil, err
		}
		cols = cols[:0]
		for _, name := range spec.Projection {
			cols = append(cols, full.IndexOf(name))
		}
	}

	s.mu.Lock()
	s.projected = append(s.projected, append([]string(nil), spec.Projection...))
	s.mu.Unlock()

	size := spec.BatchSize
	if size <= 0 {
		size = defaultBatchSize
	}
	return &reader{src: s, full: full, out: out, cols: cols, size: size, max: spec.MaxRows}, nil
}

// reader copies one window of the current record per Next.
//
// Everything is under one mutex, conversion included. arrow-go's readers are not
// safe for concurrent Next, and the engine's dispatcher pulls from a source one
// batch at a time regardless, so nothing is lost by it.
type reader struct {
	src       *Source
	full, out *dtype.Schema
	cols      []int
	size, max int

	mu      sync.Mutex
	rr      array.RecordReader
	check   *arrowin.Checker
	rec     arrow.RecordBatch // the current record; the reader owns it
	cur     int               // rows of rec already copied
	emitted int
	done    bool
	closed  bool
}

func (r *reader) Schema() *dtype.Schema { return r.out }

func (r *reader) Next(ctx context.Context) (*data.Batch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return nil, uerr.Internalf("arrow: Next on a closed scan")
	}
	if r.done {
		return nil, io.EOF
	}
	// MaxRows is a request. Honouring it here means Head(n) never asks the stream
	// for a record past the one holding row n.
	if r.max > 0 && r.emitted >= r.max {
		return nil, r.finish(nil)
	}
	if r.rr == nil {
		if err := r.start(); err != nil {
			return nil, r.finish(err)
		}
	}

	for r.rec == nil || r.cur >= int(r.rec.NumRows()) {
		// Only now may the reader move on: every row of the previous record has been
		// copied, so nothing depends on it surviving this call.
		r.rec = nil
		if !r.rr.Next() {
			if err := r.rr.Err(); err != nil && !errors.Is(err, io.EOF) {
				return nil, r.finish(uerr.Wrap(err, uerr.KindIO, op, "reading the Arrow stream"))
			}
			return nil, r.finish(nil)
		}
		rec := r.rr.RecordBatch()
		if err := r.check.Check(rec); err != nil {
			return nil, r.finish(err)
		}
		r.rec, r.cur = rec, 0
	}

	n := min(r.size, int(r.rec.NumRows())-r.cur)
	if r.max > 0 {
		n = min(n, r.max-r.emitted)
	}
	b, err := arrowin.Batch(r.rec, r.out, r.cols, r.cur, r.cur+n)
	if err != nil {
		return nil, r.finish(err)
	}
	r.cur += n
	r.emitted += n
	return b, nil
}

// start opens this scan's reader and checks it has the schema the query was planned
// against. A reader whose columns were reordered or retyped since would otherwise
// be read under the planned schema's names and types.
func (r *reader) start() error {
	rr, err := r.src.call()
	if err != nil {
		return err
	}
	r.rr = rr
	got, err := schemaOf(rr)
	if err != nil {
		return err
	}
	if !got.Equal(r.full) {
		return uerr.New(uerr.KindSchema, op,
			"the Arrow stream opened for this scan has schema %s, but the query was "+
				"planned against %s", got, r.full).
			Hint("open must return readers with the same schema every time")
	}
	r.check = arrowin.NewChecker(r.full)
	return nil
}

// finish ends the stream, releasing the reader as soon as it is not needed rather
// than waiting for Close. It returns err, or io.EOF when err is nil.
func (r *reader) finish(err error) error {
	r.done = true
	r.rec = nil
	if r.rr != nil {
		r.rr.Release()
		r.rr = nil
	}
	if err == nil {
		return io.EOF
	}
	return err
}

func (r *reader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	r.finish(nil)
	return nil
}
