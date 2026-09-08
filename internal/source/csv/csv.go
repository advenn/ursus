package csv

import (
	"context"
	"io"
	"os"
	"strconv"
	"sync"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/source"
	"github.com/advenn/ursus/internal/uerr"
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
		stage:     make([]colStage, out.Len()),
		threads:   spec.Threads,
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

	// stage holds the batch's raw fields, by OUTPUT position, so conversion can
	// happen per column after the scan instead of per field inside it. See
	// colStage. startRow and lines exist only so a value error raised during the
	// deferred conversion can still name the row and the physical line.
	stage    []colStage
	lines    []int
	startRow int
	threads  int

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

	// Staging exists to let conversion go wide; with one worker it is pure cost.
	staged := r.threads > 1
	if staged {
		for i := range r.stage {
			r.stage[i].reset()
		}
		r.lines = r.lines[:0]
		r.startRow = r.row + 1
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
		if staged {
			if err := r.stageRecord(); err != nil {
				return nil, err
			}
			r.lines = append(r.lines, r.sc.Line())
		} else if err := r.appendRecord(); err != nil {
			return nil, err
		}
		rows++
	}

	// The scan is over, so every field of the batch is now held in stage and the
	// scanner's buffer may move again. This is the part that goes wide.
	if staged {
		if err := r.convertAll(); err != nil {
			return nil, err
		}
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

// appendRecord converts the current record's fields straight into the builders,
// with no staging and no copy.
//
// This is the path a serial query takes, and it is the original one. Staging
// costs a copy of every field, which buys nothing when there is no second core
// to spend it on — and WithThreads(1) is documented as reproducing the serial
// operator tree exactly, so it must not quietly get slower to make the parallel
// path tidier. Measured: staging every field cost the serial read up to 15%.
func (r *reader) appendRecord() error {
	n := r.sc.NumFields()
	if n != len(r.wanted) && !r.src.opts.TruncateRaggedLines {
		return r.raggedError(n)
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
	for i := n; i < len(r.wanted); i++ {
		if pos := r.wanted[i]; pos >= 0 {
			r.builders[pos].appendNull()
		}
	}
	return nil
}

// stageRecord copies the current record's wanted fields into the per-column
// stages, parsing nothing. Only the columns in the projection are touched: one
// outside it was split past and is never even copied.
//
// It used to convert each field here, inside the scan. Doing that made the whole
// parse serial — the conversion is two thirds of the reader's time by profile,
// and it was running on one core while the query above it ran on eight.
func (r *reader) stageRecord() error {
	n := r.sc.NumFields()
	if n != len(r.wanted) && !r.src.opts.TruncateRaggedLines {
		return r.raggedError(n)
	}

	for i := range min(n, len(r.wanted)) {
		pos := r.wanted[i]
		if pos < 0 {
			continue // outside the projection: split past, never parsed
		}
		f := r.sc.Field(i)
		// An empty field is null for every type except String, where "" is a
		// value and losing the distinction would be unrecoverable.
		isNull := r.isNull(f) ||
			(len(f) == 0 && r.out.Field(pos).Type.ID() != dtype.TypeString)
		r.stage[pos].add(f, isNull)
	}
	// A short record pads the missing columns with nulls; only reachable with
	// TruncateRaggedLines, since the check above rejects it otherwise.
	for i := n; i < len(r.wanted); i++ {
		if pos := r.wanted[i]; pos >= 0 {
			r.stage[pos].add(nil, true)
		}
	}
	return nil
}

// raggedError is the record-shape error, shared by both record paths so that the
// message cannot depend on whether the query happened to be parallel.
func (r *reader) raggedError(n int) error {
	return uerr.New(uerr.KindValue, "scan_csv",
		"row %d (line %d) has %d fields, but the schema has %d",
		r.row, r.sc.Line(), n, len(r.wanted)).
		Hint("in %s", r.src.desc).
		Hint("pass WithTruncateRaggedLines(true) to pad and truncate instead")
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

// --- staged conversion ----------------------------------------------------------

// colStage holds one column's raw field bytes for a whole batch, so that turning
// them into typed values can happen after the scan rather than inside it.
//
// # Why the bytes are copied
//
// scanner.Field aliases the scanner's read buffer, which slides as records are
// consumed — "must not be retained", as colBuilder.appendField says. Converting
// a column at a time means holding a whole batch's fields at once, so they are
// appended into one arena per column: contiguous, sequential to write, and
// sequential to read back.
//
// It is the same offs+chars shape stringBuilder already uses, which is why a
// String column costs nothing extra here — it was being copied either way.
type colStage struct {
	chars []byte
	offs  []int32 // n+1 entries, the usual leading zero
	null  []bool  // this field was null or an empty non-string
}

func (c *colStage) reset() {
	c.chars = c.chars[:0]
	c.offs = append(c.offs[:0], 0)
	c.null = c.null[:0]
}

func (c *colStage) add(f []byte, isNull bool) {
	c.chars = append(c.chars, f...)
	c.offs = append(c.offs, int32(len(c.chars)))
	c.null = append(c.null, isNull)
}

func (c *colStage) field(i int) []byte { return c.chars[c.offs[i]:c.offs[i+1]] }

// convert drains one staged column into its builder.
//
// Errors carry the row and line recorded during the scan rather than the
// scanner's current position, which by now points at the end of the batch. The
// promise valueError exists to keep — "a bad value on row 10,001 tells you which
// column, what it found, and what it wanted" — has to survive being answered
// later than it was asked.
// It returns the batch-relative row it failed on, which is what lets convertAll
// reproduce the serial path's choice of WHICH error to report.
func (r *reader) convert(pos int, st *colStage, fileCol int) (int, error) {
	b := r.builders[pos]
	for i := range st.null {
		if st.null[i] {
			b.appendNull()
			continue
		}
		if err := b.appendField(st.field(i)); err != nil {
			return i, r.stagedValueError(fileCol, pos, st.field(i), i, err)
		}
	}
	return 0, nil
}

// convertAll turns the staged batch into columns, on several goroutines when the
// query is running on several and there is more than one column to spread.
//
// The axis is COLUMNS rather than row ranges, and that is what makes it cheap:
// two columns never touch the same builder, so there is nothing to synchronise
// and nothing to merge afterwards. The ceiling is the column count, which is the
// honest limit of this design — a two-column file gets two-way parallelism.
func (r *reader) convertAll() error {
	type job struct{ pos, fileCol int }
	jobs := make([]job, 0, len(r.stage))
	for fileCol, pos := range r.wanted {
		if pos >= 0 {
			jobs = append(jobs, job{pos, fileCol})
		}
	}

	if r.threads <= 1 || len(jobs) < 2 {
		for _, j := range jobs {
			if _, err := r.convert(j.pos, &r.stage[j.pos], j.fileCol); err != nil {
				return err
			}
		}
		return nil
	}

	var wg sync.WaitGroup
	errs := make([]error, len(jobs))
	rows := make([]int, len(jobs))
	sem := make(chan struct{}, r.threads)
	for i, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rows[i], errs[i] = r.convert(j.pos, &r.stage[j.pos], j.fileCol)
		}()
	}
	wg.Wait()

	// Which error to report is a decision, and the only defensible answer is the
	// one the serial path gives: it converts row by row and left to right, so it
	// stops at the EARLIEST BAD ROW, and within a row at the leftmost column.
	//
	// Reporting the first failing column instead is what this code did first, and
	// it made the same file report a different error depending on how many threads
	// the query happened to run on. Picking by completion order would be worse
	// still — a different error on different runs of the SAME command.
	best := -1
	for i, err := range errs {
		// jobs are in file order, so > rather than >= keeps the leftmost column
		// on a tie, which is where the serial path stops too.
		if err != nil && (best < 0 || rows[i] < rows[best]) {
			best = i
		}
	}
	if best >= 0 {
		return errs[best]
	}
	return nil
}

// stagedValueError is valueError for a row that was scanned earlier in the batch.
func (r *reader) stagedValueError(fileCol, pos int, f []byte, i int, cause error) error {
	name := r.out.Field(pos).Name
	want := r.out.Field(pos).Type
	return uerr.Wrap(cause, uerr.KindValue, "scan_csv",
		"row %d (line %d), column %q: cannot read %s as %s",
		r.startRow+i, r.lines[i], name, strconv.Quote(string(f)), want).
		Hint("in %s", r.src.desc).
		Hint("field %d of %d in the file", fileCol+1, len(r.wanted)).
		Hint("pass a schema override for %q, or add the text to WithNullValues", name)
}
