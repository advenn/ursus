package csv

import (
	"context"
	"io"
	"os"
	"strconv"
	"sync"

	"ursus/dtype"
	"ursus/internal/data"
	"ursus/internal/plan"
	"ursus/internal/source"
	"ursus/internal/uerr"
)

// Opener produces a fresh reader over the same bytes.
//
// A source is opened more than once — once to infer the schema, once per
// execution — and an io.Reader cannot be rewound. Taking a factory instead of a
// Reader makes that explicit and lets the same type serve a file, a byte slice, or
// an object store later, without this package knowing which.
type Opener func() (io.ReadCloser, error)

// Source is one or more CSV files read as a single table.
//
// Several files are read as several STREAMS, not as concatenated bytes. Each has
// its own header to skip and its own leading rows to skip, and gluing the bytes
// together would turn every header after the first into a data row — which for an
// inferred Int64 column is a parse error on a file that is not actually malformed.
type Source struct {
	opens []Opener
	desc  string
	opts  Options

	once   sync.Once
	schema *dtype.Schema
	err    error
}

// New builds a source over anything an Opener can produce. desc appears in
// Explain output.
func New(open Opener, desc string, o Options) *Source {
	return &Source{opens: []Opener{open}, desc: desc, opts: o.normalise()}
}

// NewMulti builds a source over several streams read in order. The schema is
// inferred from the first; the rest must match it.
func NewMulti(opens []Opener, desc string, o Options) *Source {
	return &Source{opens: opens, desc: desc, opts: o.normalise()}
}

// FromFile builds a source over a path.
func FromFile(path string, o Options) *Source {
	return New(FileOpener(path), path, o)
}

// FromFiles builds a source over several paths, read in the order given.
func FromFiles(paths []string, desc string, o Options) *Source {
	opens := make([]Opener, len(paths))
	for i, p := range paths {
		opens[i] = FileOpener(p)
	}
	return NewMulti(opens, desc, o)
}

// FileOpener returns an Opener for a path.
func FileOpener(path string) Opener {
	return func() (io.ReadCloser, error) { return os.Open(path) }
}

// --- plan.Source ---------------------------------------------------------------

func (s *Source) Name() string     { return "csv" }
func (s *Source) Describe() string { return s.desc }

// Caps reports projection support and NO predicate support.
//
// A CSV file has no statistics: no min/max, no row groups, no page index, no
// dictionary. Every fact about a value requires reading and parsing the bytes, and
// once the bytes are parsed the row is already in a batch and the engine's own
// filterOp will apply the predicate faster and — unlike a second implementation
// living here — correctly.
//
// There IS one useful thing a CSV reader could do with a predicate: parse only the
// filter columns, evaluate, and parse the rest for surviving rows only. Polars does
// this. It is not here because a source at import level 45 cannot import
// internal/physical at level 50 and therefore cannot call physical.Eval, so doing
// it would mean a second implementation of the promotion lattice and Kleene logic
// inside this package. Step 2's audit found the running-schema walk had been
// written four times and two copies had already diverged; a second evaluator would
// be that defect with higher stakes.
//
// The seam, when it is wanted, is a compiled predicate closure supplied by the
// physical planner through ScanSpec. Note that Source does not implement
// plan.PredicatePushdown at all, so plan.ClassifyPredicates returns Unsupported for
// every conjunct without this type having to say so.
//
// The one pushdown a CSV reader CAN serve, and does, is ScanSpec.MaxRows: stopping
// after N rows turns Head(10) on a 10 GB file from minutes into milliseconds.
func (s *Source) Caps() plan.Caps { return plan.Caps{Projection: true} }

// Schema infers the schema on first use and caches it. Inference reads the file,
// so it happens at most once per Source regardless of how many times a plan is
// resolved.
func (s *Source) Schema(ctx context.Context) (*dtype.Schema, error) {
	s.once.Do(func() {
		if s.opts.Schema != nil {
			s.schema = s.opts.Schema
			return
		}
		if err := ctx.Err(); err != nil {
			s.err = err
			return
		}
		// Inference reads the FIRST stream only. Reading them all would make opening
		// a thousand-part dataset cost a thousand file opens before the query starts,
		// and the parts of a partitioned dataset share a schema by construction.
		r, err := s.opens[0]()
		if err != nil {
			s.err = uerr.Wrap(err, uerr.KindIO, "scan_csv", "opening %s", s.desc)
			return
		}
		defer r.Close()
		s.schema, s.err = inferSchema(newScanner(r, s.opts), s.opts, s.nullTest())
		if s.err != nil {
			s.err = uerr.Annotate(s.err, "scan_csv", s.desc)
		}
	})
	if s.err != nil {
		// once.Do caches the failure, so a retry does not re-read a file that is
		// still broken. Returning the same error is the honest answer.
		return nil, s.err
	}
	return s.schema, nil
}

// nullTest returns the predicate for "this text means NULL".
func (s *Source) nullTest() func([]byte) bool {
	nulls := s.opts.NullValues
	if len(nulls) == 0 {
		return func([]byte) bool { return false }
	}
	return func(b []byte) bool {
		for _, n := range nulls {
			if len(b) == len(n) && string(b) == n {
				return true
			}
		}
		return false
	}
}

// --- source.Openable -----------------------------------------------------------

func (s *Source) Open(ctx context.Context, spec source.ScanSpec) (source.BatchSource, error) {
	full, err := s.Schema(ctx)
	if err != nil {
		return nil, err
	}

	out := full
	if spec.Projection != nil {
		if out, err = full.Select(spec.Projection); err != nil {
			return nil, uerr.Annotate(err, "scan_csv", s.desc)
		}
	}

	// wanted[i] is the output position of file column i, or -1 to skip it. This
	// array IS the projection pushdown: a column mapped to -1 is split past and
	// never parsed, which on a wide file is where all the time goes.
	wanted := make([]int, full.Len())
	for i := range wanted {
		wanted[i] = out.IndexOf(full.Field(i).Name)
	}

	builders := make([]colBuilder, out.Len())
	for i, f := range out.All() {
		if builders[i], err = newBuilder(f.Type); err != nil {
			return nil, uerr.Annotate(err, "scan_csv", s.desc)
		}
	}

	batchSize := spec.BatchSize
	if batchSize <= 0 {
		batchSize = 8192
	}

	r := &reader{
		src:       s,
		full:      full,
		out:       out,
		wanted:    wanted,
		builders:  builders,
		batchSize: batchSize,
		remaining: spec.MaxRows,
		isNull:    s.nullTest(),
	}
	if err := r.openNext(); err != nil {
		return nil, err
	}
	return r, nil
}

// --- source.BatchSource --------------------------------------------------------

type reader struct {
	src    *Source
	rc     io.ReadCloser
	sc     *scanner
	next   int           // index of the next stream to open
	full   *dtype.Schema // the file's columns
	out    *dtype.Schema // what this reader emits
	wanted []int         // file column -> output position, or -1
	isNull func([]byte) bool

	builders  []colBuilder
	batchSize int
	remaining int // rows left to produce; 0 means unlimited

	// row counts DATA rows across all parts, 1-based, which is the number that
	// matches the position in the resulting frame. scanner.Record counts records
	// in one file including its header, so "record 3" would point a reader at the
	// wrong line of a file with a header — and at nothing at all in part two of a
	// multi-file scan.
	row int

	mu   sync.Mutex
	done bool
}

func (r *reader) Schema() *dtype.Schema { return r.out }

// Close releases the current stream.
//
// It takes the same lock Next does. Without it a worker parsing a batch races the
// driver's teardown over r.rc — which was latent while the engine was
// single-threaded and is reachable the moment a scan has more than one consumer.
func (r *reader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closeLocked()
}

// closeLocked is Close for callers that already hold the lock — openNext closes the
// finished stream from inside Next.
func (r *reader) closeLocked() error {
	if r.rc == nil {
		return nil
	}
	err := r.rc.Close()
	r.rc = nil
	return err
}

// openNext advances to the next stream and skips its leading rows and header.
// It reports io.EOF when there are no streams left.
func (r *reader) openNext() error {
	if err := r.closeLocked(); err != nil {
		return uerr.Wrap(err, uerr.KindIO, "scan_csv", "closing a part of %s", r.src.desc)
	}
	if r.next >= len(r.src.opens) {
		return io.EOF
	}
	rc, err := r.src.opens[r.next]()
	if err != nil {
		return uerr.Wrap(err, uerr.KindIO, "scan_csv", "opening part %d of %s",
			r.next+1, r.src.desc)
	}
	r.next++
	r.rc = rc
	r.sc = newScanner(rc, r.src.opts)

	// Every stream has its own leading rows and its own header. This is the reason
	// multiple files are separate streams rather than concatenated bytes.
	for range r.src.opts.SkipRows {
		if !r.sc.Next() {
			return r.sc.Err()
		}
	}
	if r.src.opts.HasHeader && !r.sc.Next() {
		return r.sc.Err()
	}
	return nil
}

func (r *reader) Next(ctx context.Context) (*data.Batch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// BatchSource must be safe for concurrent Next. Parsing is inherently
	// sequential over one file handle, so the lock covers the whole batch rather
	// than pretending otherwise.
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.done {
		return nil, io.EOF
	}

	want := r.batchSize
	if r.remaining > 0 && r.remaining < want {
		// ScanSpec.MaxRows: stop early. The Limit operator above still enforces the
		// real bound, so overshooting would be correct — but not reading is the
		// entire point of Head(n) on a large file.
		want = r.remaining
	}

	rows := 0
	for rows < want {
		if !r.sc.Next() {
			if err := r.sc.Err(); err != nil {
				return nil, uerr.Annotate(err, "scan_csv", r.src.desc)
			}
			// This stream is exhausted; roll on to the next file, if any.
			switch err := r.openNext(); {
			case err == io.EOF:
				r.done = true
			case err != nil:
				return nil, err
			default:
				continue
			}
			break
		}
		r.row++
		if err := r.appendRecord(); err != nil {
			return nil, err
		}
		rows++
	}
	if r.remaining > 0 {
		r.remaining -= rows
		if r.remaining == 0 {
			r.done = true
		}
	}
	if rows == 0 {
		return nil, io.EOF
	}

	cols := make([]*data.Column, r.out.Len())
	for i, f := range r.out.All() {
		cols[i] = r.builders[i].finish(f.Name)
	}
	return data.NewBatch(r.out, cols)
}

// appendRecord parses the current record into the builders, parsing only the
// columns in the projection.
func (r *reader) appendRecord() error {
	n := r.sc.NumFields()
	if n != len(r.wanted) && !r.src.opts.TruncateRaggedLines {
		return uerr.New(uerr.KindValue, "scan_csv",
			"row %d (line %d) has %d fields, but the schema has %d",
			r.row, r.sc.Line(), n, len(r.wanted)).
			Hint("in %s", r.src.desc).
			Hint("pass WithTruncateRaggedLines(true) to pad and truncate instead")
	}

	for i := range min(n, len(r.wanted)) {
		pos := r.wanted[i]
		if pos < 0 {
			continue // outside the projection: split past, never parsed
		}
		f := r.sc.Field(i)
		b := r.builders[pos]
		// An empty field is null for every type except String, where "" is a
		// value and losing the distinction would be unrecoverable.
		if r.isNull(f) || (len(f) == 0 && r.out.Field(pos).Type.ID() != dtype.TypeString) {
			b.appendNull()
			continue
		}
		if err := b.appendField(f); err != nil {
			return r.valueError(i, pos, f, err)
		}
	}
	// A short record pads the missing columns with nulls; only reachable with
	// TruncateRaggedLines, since the check above rejects it otherwise.
	for i := n; i < len(r.wanted); i++ {
		if pos := r.wanted[i]; pos >= 0 {
			r.builders[pos].appendNull()
		}
	}
	return nil
}

// valueError is the error the CSV reader exists to produce well.
//
// It names the DATA ROW and the physical line, the column by NAME, the text found
// and the type expected. That is the whole promise: "a bad value on row 10,001
// tells you which column, what it found, and what it wanted".
//
// Both numbers, because neither alone is enough. The row is the position in the
// resulting frame, which is what a filter or a slice refers to. The line is where
// to look in the file, and it differs from the row under a header, embedded
// newlines, comments and skipped rows — every one of which is common.
//
// It is KindValue, not KindInternal. A file that does not match its schema is
// malformed input; reporting it as "a bug in ursus; please report it" would send
// someone to the issue tracker over a stray comma in their data.
func (r *reader) valueError(fileCol, pos int, f []byte, cause error) error {
	name := r.out.Field(pos).Name
	want := r.out.Field(pos).Type
	return uerr.Wrap(cause, uerr.KindValue, "scan_csv",
		"row %d (line %d), column %q: cannot read %s as %s",
		r.row, r.sc.Line(), name, strconv.Quote(string(f)), want).
		Hint("in %s", r.src.desc).
		Hint("field %d of %d in the file", fileCol+1, len(r.wanted)).
		Hint("pass a schema override for %q, or add the text to WithNullValues", name)
}
